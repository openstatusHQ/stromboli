package logs

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Bucket is the number of matching lines in [Start, Start+step).
type Bucket struct {
	Start time.Time `json:"start"`
	Count int64     `json:"count"`
}

// Histogrammer is implemented by sources that count matches per time bucket
// in a single query. Buckets are aligned to multiples of step since the Unix
// epoch; empty buckets may be omitted.
type Histogrammer interface {
	Histogram(ctx context.Context, q Query, step time.Duration) ([]Bucket, error)
}

// Timeline counts q's matches in about n equal buckets covering [Start, End].
// Bucket boundaries are aligned to whole multiples of the step (rounded to
// seconds), so the first bucket may start slightly before q.Start. Sources
// that implement Histogrammer answer in one query; others get one Count per
// bucket.
func Timeline(ctx context.Context, src Source, q Query, n int) ([]Bucket, time.Duration, error) {
	if n < 1 {
		n = 1
	}
	step := (q.End.Sub(q.Start) / time.Duration(n)).Round(time.Second)
	if step < time.Second {
		step = time.Second
	}
	secs := int64(step / time.Second)
	start := time.Unix(q.Start.Unix()/secs*secs, 0).UTC()
	count := int((q.End.Sub(start) + step - 1) / step)
	buckets := make([]Bucket, count)
	for i := range buckets {
		buckets[i].Start = start.Add(time.Duration(i) * step)
	}

	if h, ok := src.(Histogrammer); ok {
		hq := q
		hq.Start, hq.End = start, start.Add(time.Duration(count)*step)
		got, err := h.Histogram(ctx, hq, step)
		if err != nil {
			return nil, 0, err
		}
		for _, b := range got {
			i := int(b.Start.Sub(start) / step)
			if i >= 0 && i < count {
				buckets[i].Count += b.Count
			}
		}
		return buckets, step, nil
	}

	errs := make([]error, count)
	var wg sync.WaitGroup
	for i := range buckets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bq := q
			bq.Start, bq.End = buckets[i].Start, buckets[i].Start.Add(step)
			buckets[i].Count, errs[i] = src.Count(ctx, bq)
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, 0, err
	}
	return buckets, step, nil
}
