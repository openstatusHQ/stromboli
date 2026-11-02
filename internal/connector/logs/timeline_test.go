package logs

import (
	"context"
	"sync"
	"testing"
	"time"
)

// countSource counts one line per minute; it has no Histogram.
type countSource struct {
	mu    sync.Mutex
	calls int
}

func (s *countSource) Query(context.Context, Query) ([]Line, error) { return nil, nil }
func (s *countSource) Services(context.Context) ([]Service, error)  { return nil, nil }
func (s *countSource) Check(context.Context) error                  { return nil }
func (s *countSource) Count(_ context.Context, q Query) (int64, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return int64(q.End.Sub(q.Start) / time.Minute), nil
}

// histSource answers with one bucket per call, and must not be counted.
type histSource struct {
	countSource
	got  Query
	step time.Duration
}

func (s *histSource) Histogram(_ context.Context, q Query, step time.Duration) ([]Bucket, error) {
	s.got, s.step = q, step
	return []Bucket{
		{Start: q.Start.Add(step), Count: 7},
		{Start: q.Start.Add(-step), Count: 99}, // outside the window: ignored
	}, nil
}

func TestTimelineFallsBackToCount(t *testing.T) {
	src := &countSource{}
	start := time.Date(2026, 11, 2, 10, 2, 30, 0, time.UTC)
	got, step, err := Timeline(context.Background(), src, Query{Start: start, End: start.Add(time.Hour)}, 12)
	if err != nil {
		t.Fatal(err)
	}
	if step != 5*time.Minute {
		t.Fatalf("step = %v", step)
	}
	// Aligned to 10:00, so 13 buckets cover 10:02:30-11:02:30.
	if len(got) != 13 || !got[0].Start.Equal(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)) || got[0].Count != 5 {
		t.Fatalf("buckets = %+v", got)
	}
	if src.calls != 13 {
		t.Fatalf("calls = %d", src.calls)
	}
}

func TestTimelineUsesHistogram(t *testing.T) {
	src := &histSource{}
	start := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	got, step, err := Timeline(context.Background(), src, Query{Selector: "s", Start: start, End: start.Add(time.Hour)}, 12)
	if err != nil {
		t.Fatal(err)
	}
	if src.calls != 0 || src.step != step || !src.got.Start.Equal(start) || !src.got.End.Equal(start.Add(time.Hour)) {
		t.Fatalf("calls=%d step=%v query=%+v", src.calls, src.step, src.got)
	}
	if len(got) != 12 || got[1].Count != 7 || got[0].Count != 0 {
		t.Fatalf("buckets = %+v", got)
	}
}
