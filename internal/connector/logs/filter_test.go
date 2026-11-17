package logs

import (
	"regexp"
	"strings"
	"testing"
)

// Filter is documented as case-insensitive, but no backend is by default:
// the flag is added once, and never twice.
func TestCaseInsensitive(t *testing.T) {
	for in, want := range map[string]string{
		"":              "",
		"timeout":       "(?i)timeout",
		"(?i)timeout":   "(?i)timeout",
		ErrorFilter:     ErrorFilter,
		"a/b|refused":   "(?i)a/b|refused",
		"(?s)multiline": "(?i)(?s)multiline",
	} {
		if got := caseInsensitive(in); got != want {
			t.Errorf("caseInsensitive(%q) = %q, want %q", in, got, want)
		}
	}
	if !regexp.MustCompile(caseInsensitive("timeout")).MatchString("upstream Timeout after 30s") {
		t.Error("the filter should match regardless of case")
	}
}

func TestFiltersAreCaseInsensitive(t *testing.T) {
	q := Query{Selector: `{app="api"}`, Filter: "timeout"}
	if got := logQL(q); got != `{app="api"} |~ "(?i)timeout"` {
		t.Errorf("loki: %s", got)
	}
	if got := gcpFilter(q); !strings.Contains(got, `textPayload=~"(?i)timeout"`) {
		t.Errorf("gcp: %s", got)
	}
	if got := insightsQuery("", "", q.Filter); got != "filter @message like /(?i)timeout/" {
		t.Errorf("cloudwatch: %s", got)
	}
	// The error filter keeps its severity shortcut on Cloud Logging.
	if got := gcpFilter(Query{Filter: ErrorFilter}); !strings.Contains(got, "severity>=ERROR") {
		t.Errorf("gcp error filter: %s", got)
	}
}
