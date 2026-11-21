package ui

import (
	"strings"
	"testing"
)

func TestStyleLine(t *testing.T) {
	saved := colorOn
	defer SetColor(saved)
	SetColor(true)
	var st mdState
	plain := func(s string) string { return stripANSI(s) }

	cases := []struct{ in, want string }{
		{"## Setup", "Setup"},
		{"- item with `code`", "• item with code"},
		{"  * nested **bold**", "  • nested bold"},
		{"1. first", "1. first"},
		{"> quoted", "│ quoted"},
		{"see [docs](https://x.dev/a)", "see docs (https://x.dev/a)"},
		{"`a **not bold** b`", "a **not bold** b"},
		{"plain text", "plain text"},
	}
	for _, c := range cases {
		if got := plain(styleLine(c.in, &st)); got != c.want {
			t.Errorf("%q → %q, want %q", c.in, got, c.want)
		}
	}
	if !strings.Contains(styleLine("**x**", &st), "\x1b[1m") {
		t.Error("bold not applied")
	}

	// Code fences: content is kept verbatim, comments dimmed, strings colored.
	styleLine("```go", &st)
	if !st.inCode {
		t.Fatal("fence did not open")
	}
	got := styleLine(`x := "a // b" // note`, &st)
	if plain(got) != `x := "a // b" // note` || !strings.Contains(got, Dim("// note")) || !strings.Contains(got, Green(`"a // b"`)) {
		t.Fatalf("code line: %q", got)
	}
	if plain(styleLine("- not a bullet in code", &st)) != "- not a bullet in code" {
		t.Error("markdown applied inside code")
	}
	if plain(styleLine("ls --all # list", &st)) != "ls --all # list" || !strings.Contains(styleLine("ls --all # list", &st), Dim("# list")) {
		t.Error("shell comment / flag handling")
	}
	styleLine("```", &st)
	if st.inCode {
		t.Fatal("fence did not close")
	}

	SetColor(false)
	if styleLine("## raw", &st) != "## raw" {
		t.Error("no-color output must be unchanged")
	}
}
