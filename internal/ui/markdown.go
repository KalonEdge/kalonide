package ui

import (
	"regexp"
	"strings"
)

// mdState carries what one line of markdown needs to know about earlier
// lines: whether it is inside a fenced code block.
type mdState struct {
	inCode bool
}

var (
	reHeading  = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	reBullet   = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	reNumbered = regexp.MustCompile(`^(\s*)(\d+[.)])\s+(.*)$`)
	reBold     = regexp.MustCompile(`\*\*([^*\n]+)\*\*|__([^_\n]+)__`)
	reCode     = regexp.MustCompile("`([^`\n]+)`")
	reLink     = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^)\s]+)\)`)
	reRule     = regexp.MustCompile(`^\s*(?:-{3,}|\*{3,}|_{3,})\s*$`)
	reString   = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'` + "|`[^`]*`")
)

// styleLine renders one complete line of markdown for the terminal. It
// returns the line unchanged when color is off.
func styleLine(line string, st *mdState) string {
	if !colorOn {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			st.inCode = !st.inCode
		}
		return line
	}
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "```") {
		st.inCode = !st.inCode
		return Dim(line)
	}
	if st.inCode {
		return styleCode(line)
	}
	switch {
	case reHeading.MatchString(line):
		m := reHeading.FindStringSubmatch(line)
		return Bold(Bright(inline(m[2])))
	case reRule.MatchString(line):
		return Dim(strings.Repeat("─", min(Width()-4, 60)))
	case strings.HasPrefix(trimmed, "> "):
		return Dim("│ ") + Italic(inline(strings.TrimPrefix(trimmed, "> ")))
	case reBullet.MatchString(line):
		m := reBullet.FindStringSubmatch(line)
		return m[1] + Accent("•") + " " + inline(m[2])
	case reNumbered.MatchString(line):
		m := reNumbered.FindStringSubmatch(line)
		return m[1] + Accent(m[2]) + " " + inline(m[3])
	}
	return inline(line)
}

// inline styles **bold**, `code` and [links](url) inside a line.
func inline(s string) string {
	// Code first, so markup inside code spans is left alone.
	var spans []string
	s = reCode.ReplaceAllStringFunc(s, func(m string) string {
		spans = append(spans, Accent(m[1:len(m)-1]))
		return "\x00" + string(rune('0'+len(spans)-1)) + "\x00"
	})
	s = reBold.ReplaceAllStringFunc(s, func(m string) string {
		return Bold(strings.Trim(m, "*_"))
	})
	s = reLink.ReplaceAllString(s, "$1"+Dim(" ($2)"))
	for i, sp := range spans {
		if i > 9 {
			break
		}
		s = strings.Replace(s, "\x00"+string(rune('0'+i))+"\x00", sp, 1)
	}
	return s
}

// styleCode lightly highlights a line inside a code block: comments dim,
// strings green.
func styleCode(line string) string {
	code, comment := line, ""
	for _, marker := range []string{"//", "#", "--"} {
		if i := commentStart(line, marker); i >= 0 {
			code, comment = line[:i], line[i:]
			break
		}
	}
	code = reString.ReplaceAllStringFunc(code, Green)
	if comment != "" {
		comment = Dim(comment)
	}
	return code + comment
}

// commentStart finds a comment marker that is not inside a string and, for
// "#" and "--", starts the line or follows a space (so "#include" in C
// and "x--" are left alone).
func commentStart(line, marker string) int {
	inStr := byte(0)
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case inStr != 0:
			if c == '\\' {
				i++
			} else if c == inStr {
				inStr = 0
			}
		case c == '"' || c == '\'' || c == '`':
			inStr = c
		case strings.HasPrefix(line[i:], marker):
			if marker == "//" {
				return i
			}
			if marker == "--" && strings.TrimSpace(line[:i]) != "" {
				continue // "--" only starts a comment at the beginning of a line (SQL, Lua), never a flag like --all
			}
			if (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') && (marker != "#" || !strings.HasPrefix(line[i:], "#include")) {
				return i
			}
		}
	}
	return -1
}
