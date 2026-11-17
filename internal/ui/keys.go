package ui

import (
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// KeyKind identifies a key press.
type KeyKind int

const (
	KeyRune KeyKind = iota
	KeyEnter
	KeyNewline // Alt+Enter: insert a line break
	KeyBackspace
	KeyDelete
	KeyLeft
	KeyRight
	KeyUp
	KeyDown
	KeyHome
	KeyEnd
	KeyWordLeft
	KeyWordRight
	KeyPageUp
	KeyPageDown
	KeyTab
	KeyShiftTab
	KeyEsc
	KeyCtrlC
	KeyCtrlD
	KeyCtrlK
	KeyCtrlU
	KeyCtrlW
	KeyCtrlL
	KeyPaste
	KeyEOF // input closed
)

// Key is one decoded key press.
type Key struct {
	Kind KeyKind
	Rune rune
	Text string // pasted text for KeyPaste
}

// keyReader owns stdin: one goroutine reads bytes into a channel so that
// escape sequences can be told apart from a lone Esc with a short timeout.
type keyReader struct {
	ch chan byte
}

var (
	readerOnce sync.Once
	reader     *keyReader
)

func keys() *keyReader {
	readerOnce.Do(func() {
		reader = &keyReader{ch: make(chan byte, 4096)}
		go func() {
			buf := make([]byte, 1024)
			for {
				n, err := os.Stdin.Read(buf)
				for _, b := range buf[:n] {
					reader.ch <- b
				}
				if err != nil {
					close(reader.ch)
					return
				}
			}
		}()
	})
	return reader
}

func (k *keyReader) byteWait(d time.Duration) (byte, bool) {
	if d <= 0 {
		b, ok := <-k.ch
		return b, ok
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case b, ok := <-k.ch:
		return b, ok
	case <-t.C:
		return 0, false
	}
}

// ReadKey blocks for the next key press. The terminal must be in raw mode.
func ReadKey() Key {
	k := keys()
	b, ok := k.byteWait(0)
	if !ok {
		return Key{Kind: KeyEOF}
	}
	switch b {
	case '\r':
		return Key{Kind: KeyEnter}
	case 0x7f, 0x08:
		return Key{Kind: KeyBackspace}
	case '\t':
		return Key{Kind: KeyTab}
	case 0x01:
		return Key{Kind: KeyHome}
	case 0x05:
		return Key{Kind: KeyEnd}
	case 0x02:
		return Key{Kind: KeyLeft}
	case 0x06:
		return Key{Kind: KeyRight}
	case 0x10:
		return Key{Kind: KeyUp}
	case 0x0e:
		return Key{Kind: KeyDown}
	case 0x03:
		return Key{Kind: KeyCtrlC}
	case 0x04:
		return Key{Kind: KeyCtrlD}
	case 0x0b:
		return Key{Kind: KeyCtrlK}
	case 0x15:
		return Key{Kind: KeyCtrlU}
	case 0x17:
		return Key{Kind: KeyCtrlW}
	case 0x0c:
		return Key{Kind: KeyCtrlL}
	case 0x0a:
		return Key{Kind: KeyNewline}
	case 0x1b:
		return readEscape(k)
	}
	if b < 0x20 {
		return Key{Kind: KeyRune, Rune: 0} // ignored control byte
	}
	return Key{Kind: KeyRune, Rune: readRune(k, b)}
}

func readRune(k *keyReader, first byte) rune {
	if first < utf8.RuneSelf {
		return rune(first)
	}
	need := 0
	switch {
	case first&0xE0 == 0xC0:
		need = 1
	case first&0xF0 == 0xE0:
		need = 2
	case first&0xF8 == 0xF0:
		need = 3
	}
	buf := []byte{first}
	for i := 0; i < need; i++ {
		b, ok := k.byteWait(50 * time.Millisecond)
		if !ok {
			break
		}
		buf = append(buf, b)
	}
	r, _ := utf8.DecodeRune(buf)
	return r
}

func readEscape(k *keyReader) Key {
	b, ok := k.byteWait(40 * time.Millisecond)
	if !ok {
		return Key{Kind: KeyEsc}
	}
	switch b {
	case '[':
		return readCSI(k)
	case 'O':
		c, _ := k.byteWait(40 * time.Millisecond)
		switch c {
		case 'A':
			return Key{Kind: KeyUp}
		case 'B':
			return Key{Kind: KeyDown}
		case 'C':
			return Key{Kind: KeyRight}
		case 'D':
			return Key{Kind: KeyLeft}
		case 'H':
			return Key{Kind: KeyHome}
		case 'F':
			return Key{Kind: KeyEnd}
		}
	case '\r', '\n':
		return Key{Kind: KeyNewline}
	case 'b', 'B':
		return Key{Kind: KeyWordLeft}
	case 'f', 'F':
		return Key{Kind: KeyWordRight}
	case 0x7f:
		return Key{Kind: KeyCtrlW}
	}
	return Key{Kind: KeyEsc}
}

func readCSI(k *keyReader) Key {
	var params strings.Builder
	for i := 0; i < 16; i++ {
		b, ok := k.byteWait(40 * time.Millisecond)
		if !ok {
			return Key{Kind: KeyEsc}
		}
		if b >= 0x40 && b <= 0x7e {
			return csiKey(k, params.String(), b)
		}
		params.WriteByte(b)
	}
	return Key{Kind: KeyEsc}
}

func csiKey(k *keyReader, params string, final byte) Key {
	mod := ""
	if i := strings.Index(params, ";"); i >= 0 {
		mod = params[i+1:]
	}
	word := mod == "3" || mod == "5" || mod == "9"
	switch final {
	case 'A':
		return Key{Kind: KeyUp}
	case 'B':
		return Key{Kind: KeyDown}
	case 'C':
		if word {
			return Key{Kind: KeyWordRight}
		}
		return Key{Kind: KeyRight}
	case 'D':
		if word {
			return Key{Kind: KeyWordLeft}
		}
		return Key{Kind: KeyLeft}
	case 'H':
		return Key{Kind: KeyHome}
	case 'F':
		return Key{Kind: KeyEnd}
	case 'Z':
		return Key{Kind: KeyShiftTab}
	case '~':
		switch strings.SplitN(params, ";", 2)[0] {
		case "1", "7":
			return Key{Kind: KeyHome}
		case "4", "8":
			return Key{Kind: KeyEnd}
		case "3":
			return Key{Kind: KeyDelete}
		case "5":
			return Key{Kind: KeyPageUp}
		case "6":
			return Key{Kind: KeyPageDown}
		case "200":
			return Key{Kind: KeyPaste, Text: readPaste(k)}
		}
	}
	return Key{Kind: KeyRune, Rune: 0}
}

// readPaste collects bracketed-paste text up to the end marker.
func readPaste(k *keyReader) string {
	const end = "\x1b[201~"
	var buf []byte
	for len(buf) < 4<<20 {
		b, ok := k.byteWait(2 * time.Second)
		if !ok {
			break
		}
		buf = append(buf, b)
		if len(buf) >= len(end) && string(buf[len(buf)-len(end):]) == end {
			buf = buf[:len(buf)-len(end)]
			break
		}
	}
	s := strings.ReplaceAll(string(buf), "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\t", "    ")
	// Drop other control characters that would corrupt the display.
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' {
			return -1
		}
		return r
	}, s)
}
