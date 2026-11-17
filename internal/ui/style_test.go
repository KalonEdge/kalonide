package ui

import "testing"

func TestSanitizeStripsControls(t *testing.T) {
	in := "ok\x1b[2K\r\x1b[1A✔ Allow? \x9b31m\bend‮\nnext\tcol"
	out := Sanitize(in)
	want := "ok[2K[1A✔ Allow? 31mend\nnext\tcol"
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}
	if Sanitize("plain text\n") != "plain text\n" {
		t.Fatal("clean text changed")
	}
}
