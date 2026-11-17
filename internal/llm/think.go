package llm

import "strings"

// thinkSplitter separates <think>…</think> reasoning that some models put
// inline in their answer text. Tags may arrive split across stream chunks,
// so a possible partial tag at the end of a chunk is held back.
type thinkSplitter struct {
	inThink bool
	pending string
}

// feed consumes a chunk and returns answer text and reasoning text.
func (s *thinkSplitter) feed(chunk string) (content, reasoning string) {
	buf := s.pending + chunk
	s.pending = ""
	var c, r strings.Builder
	for buf != "" {
		tag := "<think>"
		if s.inThink {
			tag = "</think>"
		}
		i := strings.Index(buf, tag)
		if i < 0 {
			// Hold back a suffix that could be the start of the tag.
			hold := partialSuffix(buf, tag)
			emit := buf[:len(buf)-hold]
			s.pending = buf[len(buf)-hold:]
			if s.inThink {
				r.WriteString(emit)
			} else {
				c.WriteString(emit)
			}
			break
		}
		before := buf[:i]
		if s.inThink {
			r.WriteString(before)
		} else {
			c.WriteString(before)
		}
		buf = buf[i+len(tag):]
		s.inThink = !s.inThink
		if !s.inThink {
			buf = strings.TrimLeft(buf, "\r\n")
		}
	}
	return c.String(), r.String()
}

// flush returns anything still held back at end of stream.
func (s *thinkSplitter) flush() (content, reasoning string) {
	p := s.pending
	s.pending = ""
	if s.inThink {
		return "", p
	}
	return p, ""
}

// partialSuffix returns the length of the longest suffix of s that is a
// proper prefix of tag.
func partialSuffix(s, tag string) int {
	for n := min(len(tag)-1, len(s)); n > 0; n-- {
		if strings.HasSuffix(s, tag[:n]) {
			return n
		}
	}
	return 0
}
