package tools

import "regexp"

type secretRule struct {
	re   *regexp.Regexp
	repl string
}

// secretRules match common credentials. Matches are replaced before tool
// output is shown or sent to the model.
var secretRules = []secretRule{
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`), "[REDACTED PRIVATE KEY]"},
	{regexp.MustCompile(`\bsk-(?:proj-|ant-|or-v1-)?[A-Za-z0-9_\-]{20,}`), "[REDACTED]"},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`), "[REDACTED]"},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{40,}`), "[REDACTED]"},
	{regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,}`), "[REDACTED]"},
	{regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9\-]{10,}`), "[REDACTED]"},
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "[REDACTED]"},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`), "[REDACTED]"},
	{regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._\-]{20,}`), "${1}[REDACTED]"},
	// user:password@host in URLs: keep the user and the host.
	{regexp.MustCompile(`(://[^:/\s@]+:)[^@/\s]{3,}(@)`), "${1}[REDACTED]${2}"},
	// api_key = "…", password: …: keep the key name.
	{regexp.MustCompile(`(?i)\b((?:api[_-]?key|secret|access[_-]?token|auth[_-]?token|password|passwd)["']?\s*[:=]\s*["']?)[A-Za-z0-9._\-/+]{12,}`), "${1}[REDACTED]"},
}

// Redact hides credentials in s.
func Redact(s string) string {
	for _, r := range secretRules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}
