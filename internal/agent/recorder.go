package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/openstatushq/stromboli/internal/store"
	"github.com/openstatushq/stromboli/internal/store/db"
)

// recorder appends steps to an investigation's audit trail.
type recorder struct {
	store *store.Store
	id    uuid.UUID
	mu    sync.Mutex
	seq   int64
}

type step struct {
	Kind     string
	Name     string
	Input    any
	Output   string
	IsError  bool
	Duration time.Duration
}

// maxStepOutput keeps the audit trail readable; evidence is pre-aggregated anyway.
const maxStepOutput = 64 << 10

func (r *recorder) record(ctx context.Context, s step) {
	input, err := json.Marshal(s.Input)
	if err != nil || s.Input == nil {
		input = []byte("{}")
	}
	out := s.Output
	if len(out) > maxStepOutput {
		out = cut(out, maxStepOutput) + "\n… (truncated)"
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	err = r.store.InsertStep(context.WithoutCancel(ctx), db.InsertStepParams{
		InvestigationID: r.id,
		Seq:             r.seq,
		Kind:            s.Kind,
		Name:            s.Name,
		Input:           input,
		Output:          out,
		IsError:         s.IsError,
		DurationMs:      s.Duration.Milliseconds(),
	})
	if err != nil {
		slog.Error("record step", "investigation", r.id, "kind", s.Kind, "err", err)
	}
}
