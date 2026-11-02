// Package logs defines the log source interface and backend-neutral helpers
// that turn raw lines into compact evidence for the LLM.
package logs

import (
	"context"
	"time"
)

// Query selects log lines for one service.
type Query struct {
	// Selector is the service's backend-specific selector from the catalog.
	Selector string
	// Filter is an optional case-insensitive regular expression on the line.
	Filter string
	Start  time.Time
	End    time.Time
	Limit  int
}

type Line struct {
	Time   time.Time         `json:"time"`
	Labels map[string]string `json:"labels,omitempty"`
	Text   string            `json:"text"`
}

// Service is a service discovered in a log backend, with the selector that
// returns its logs.
type Service struct {
	Name     string
	Selector string
}

// Kinds lists the connector kinds that are log sources.
var Kinds = []string{"loki", "axiom", "cloudwatch"}

// Source is a log backend. Implementations must bound their responses.
type Source interface {
	Query(ctx context.Context, q Query) ([]Line, error)
	// Count returns the number of lines matching q in [Start, End].
	Count(ctx context.Context, q Query) (int64, error)
	// Services proposes catalog services found in the backend, for onboarding.
	Services(ctx context.Context) ([]Service, error)
	// Check verifies connectivity and credentials.
	Check(ctx context.Context) error
}

// ErrorFilter matches lines that look like errors across common log formats.
const ErrorFilter = `(?i)(\berror\b|\berr\b|\bfatal\b|\bpanic\b|\bexception\b|level=(error|fatal|crit)|"level":\s*"(error|fatal|critical)"|\bstatus[=:]\s*5\d\d\b|\btimeout\b|\btimed out\b)`
