package policy

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := map[string]string{
		`Authorization: Bearer abcdefghijklmnop`:             "abcdefghijklmnop",
		`password=hunter22 user=bob`:                         "hunter22",
		`{"api_key": "s3cr3t-value"}`:                        "s3cr3t-value",
		`connecting to postgres://app:pa55w0rd@db:5432/main`: "pa55w0rd",
		`user alice@example.com logged in`:                   "alice@example.com",
		`token ghp_abcdefghijklmnopqrstuvwxyz0123`:           "ghp_abcdefghijklmnopqrstuvwxyz0123",
		`key AKIAABCDEFGHIJKLMNOP used`:                      "AKIAABCDEFGHIJKLMNOP",
	}
	for in, secret := range cases {
		out := Redact(in)
		if strings.Contains(out, secret) {
			t.Errorf("Redact(%q) = %q, still contains %q", in, out, secret)
		}
	}

	keep := `GET /api/v1/monitors 500 in 1532ms`
	if got := Redact(keep); got != keep {
		t.Errorf("Redact changed a harmless line: %q", got)
	}
}
