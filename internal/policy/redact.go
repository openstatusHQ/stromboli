// Package policy holds the guardrails applied before data reaches the LLM.
package policy

import "regexp"

var redactions = []struct {
	re   *regexp.Regexp
	repl string
}{
	// Authorization headers and bearer tokens.
	{regexp.MustCompile(`(?i)(bearer|basic)\s+[a-z0-9._~+/=-]{8,}`), "$1 [REDACTED]"},
	// JWTs.
	{regexp.MustCompile(`eyJ[a-zA-Z0-9_-]{8,}\.[a-zA-Z0-9_-]{8,}\.[a-zA-Z0-9_-]{8,}`), "[REDACTED_JWT]"},
	// Common provider keys.
	{regexp.MustCompile(`\b(AKIA|ASIA)[A-Z0-9]{16}\b`), "[REDACTED_AWS_KEY]"},
	{regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`), "[REDACTED_GITHUB_TOKEN]"},
	{regexp.MustCompile(`\b(sk|pk|rk)_(live|test)_[A-Za-z0-9]{10,}\b`), "[REDACTED_STRIPE_KEY]"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`), "[REDACTED_API_KEY]"},
	{regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}\b`), "[REDACTED_SLACK_TOKEN]"},
	// key=value / "key": "value" pairs with sensitive names.
	{regexp.MustCompile(`(?i)("?(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|client[_-]?secret|authorization|cookie)"?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;&"']+)`), "$1[REDACTED]"},
	// Credentials in URLs.
	{regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^\s:/@]+:[^\s@/]+@`), "$1[REDACTED]@"},
	// Email addresses.
	{regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), "[REDACTED_EMAIL]"},
}

// Redact removes secrets and personal data from text before it is sent to
// the LLM or stored as evidence.
func Redact(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}
