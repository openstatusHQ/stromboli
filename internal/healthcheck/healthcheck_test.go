package healthcheck

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
	"github.com/openstatushq/stromboli/internal/agent"
	"github.com/openstatushq/stromboli/internal/connector/metrics"
	"github.com/openstatushq/stromboli/internal/store/db"
)

// fakeSource answers each query with the series of the first preset whose
// metric name appears in it.
type fakeSource struct {
	metrics.Source
	series map[string][]metrics.Series // by preset name
	fail   string
	start  time.Time
	end    time.Time
}

var presetMetric = map[string]string{
	"volume_used_percent": "fly_volume_used_pct",
	"disk_used_percent":   "fly_instance_filesystem_blocks",
	"memory_used_percent": "fly_instance_memory_mem_available",
	"cpu_percent":         "fly_instance_cpu{",
}

func (f *fakeSource) Range(_ context.Context, expr string, start, end time.Time) ([]metrics.Series, error) {
	f.start, f.end = start, end
	for preset, m := range presetMetric {
		if strings.Contains(expr, m) {
			if preset == f.fail {
				return nil, errors.New("boom")
			}
			if !strings.Contains(expr, `app="api"`) {
				return nil, errors.New("selector not expanded: " + expr)
			}
			return f.series[preset], nil
		}
	}
	return nil, nil
}

type fakeStarter struct {
	reqs   []agent.StartRequest
	reused bool
	err    error
}

func (f *fakeStarter) Start(_ context.Context, req agent.StartRequest) (db.Investigation, bool, error) {
	f.reqs = append(f.reqs, req)
	return db.Investigation{ID: uuid.MustParse("00000000-0000-0000-0000-000000000001")}, f.reused, f.err
}

func series(labels map[string]string, values ...float64) metrics.Series {
	s := metrics.Series{Labels: labels}
	t := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	for i, v := range values {
		s.Samples = append(s.Samples, metrics.Sample{Time: t.Add(time.Duration(i) * time.Minute), Value: v})
	}
	return s
}

func newChecker(src *fakeSource, st *fakeStarter, services ...*stromboliv1.Service) *Checker {
	c := New(time.Hour,
		func(context.Context) ([]*stromboliv1.Service, error) { return services, nil },
		func(_ context.Context, name string) (metrics.Source, string, error) {
			if name != "fly" {
				return nil, "", errors.New("unknown connector " + name)
			}
			return src, "fly", nil
		},
		st)
	c.now = func() time.Time { return time.Date(2026, 11, 8, 10, 15, 0, 0, time.UTC) }
	return c
}

func flySvc(name string) *stromboliv1.Service {
	return &stromboliv1.Service{Name: name, Metrics: &stromboliv1.MetricsConfig{Source: "fly", Selectors: []string{`app="api"`}}}
}

func TestRunOnceStartsInvestigation(t *testing.T) {
	src := &fakeSource{series: map[string][]metrics.Series{
		// Full volume: the last value counts, even though the average is lower.
		"volume_used_percent": {
			series(map[string]string{"id": "vol_123", "region": "fra"}, 80, 88, 93),
			series(map[string]string{"id": "vol_456", "region": "ams"}, 40, 41, 42),
		},
		"memory_used_percent": {series(map[string]string{"instance": "148e", "region": "ams"}, 60, 61)},
		// One CPU spike: the average stays under the threshold.
		"cpu_percent": {series(map[string]string{"instance": "148e", "region": "ams"}, 20, 99, 20)},
	}}
	st := &fakeStarter{}
	noMetrics := &stromboliv1.Service{Name: "worker"}
	results := newChecker(src, st, flySvc("api"), noMetrics).RunOnce(context.Background())

	if len(results) != 1 || results[0].Service != "api" {
		t.Fatalf("results = %+v, want one for api (worker has no metrics)", results)
	}
	if got := src.end.Sub(src.start); got != window {
		t.Errorf("window = %s, want %s", got, window)
	}
	r := results[0]
	if len(r.Findings) != 1 || r.Findings[0].Check != "volume_used_percent" || r.Findings[0].Value != 93 ||
		r.Findings[0].Labels["id"] != "vol_123" {
		t.Fatalf("findings = %+v, want vol_123 at 93%%", r.Findings)
	}
	if len(st.reqs) != 1 {
		t.Fatalf("started %d investigations, want 1", len(st.reqs))
	}
	req := st.reqs[0]
	if req.Source != Source || req.Services[0] != "api" || req.DedupeWindow != Renotify ||
		req.Fingerprint != "healthcheck:api:volume_used_percent" {
		t.Errorf("unexpected request: %+v", req)
	}
	for _, want := range []string{"volume_used_percent 93", "vol_123", "fra"} {
		if !strings.Contains(req.Title, want) {
			t.Errorf("title %q is missing %q", req.Title, want)
		}
	}
	if !strings.Contains(req.Description, "does not free space on restart") {
		t.Errorf("description is missing the hint: %s", req.Description)
	}
	if r.InvestigationID == "" || r.Deduplicated {
		t.Errorf("result = %+v, want a new investigation", r)
	}
}

func TestRunOnceHealthy(t *testing.T) {
	src := &fakeSource{series: map[string][]metrics.Series{
		"volume_used_percent": {series(map[string]string{"id": "vol_123"}, 50)},
		// NaN points are skipped: the average is 52.5.
		"cpu_percent": {series(map[string]string{"instance": "148e"}, 95, math.NaN(), 10)},
	}}
	st := &fakeStarter{}
	results := newChecker(src, st, flySvc("api")).RunOnce(context.Background())
	if len(results) != 1 || len(results[0].Findings) != 0 || len(results[0].Errors) != 0 {
		t.Fatalf("results = %+v, want healthy", results)
	}
	if len(st.reqs) != 0 {
		t.Errorf("started %d investigations for a healthy service", len(st.reqs))
	}
}

func TestRunOnceSeveralChecksAndErrors(t *testing.T) {
	src := &fakeSource{
		series: map[string][]metrics.Series{
			"memory_used_percent": {series(map[string]string{"instance": "148e"}, 97)},
			"cpu_percent":         {series(map[string]string{"instance": "148e"}, 95, 96)},
		},
		fail: "disk_used_percent",
	}
	st := &fakeStarter{reused: true}
	r := newChecker(src, st, flySvc("api")).RunOnce(context.Background())[0]
	if len(r.Findings) != 2 || r.Findings[0].Check != "memory_used_percent" || r.Findings[1].Check != "cpu_percent" {
		t.Fatalf("findings = %+v, want memory then cpu", r.Findings)
	}
	if len(r.Errors) != 1 || !strings.Contains(r.Errors[0], "disk_used_percent: boom") {
		t.Errorf("errors = %v, want the disk query failure", r.Errors)
	}
	if st.reqs[0].Fingerprint != "healthcheck:api:cpu_percent,memory_used_percent" {
		t.Errorf("fingerprint = %q, want sorted checks", st.reqs[0].Fingerprint)
	}
	if !strings.HasSuffix(st.reqs[0].Title, "(+1 more)") {
		t.Errorf("title = %q", st.reqs[0].Title)
	}
	if !r.Deduplicated {
		t.Error("want the reused investigation reported as deduplicated")
	}
}

func TestRunOnceStartError(t *testing.T) {
	src := &fakeSource{series: map[string][]metrics.Series{
		"disk_used_percent": {series(map[string]string{"instance": "148e"}, 99)},
	}}
	st := &fakeStarter{err: errors.New("no LLM is configured")}
	r := newChecker(src, st, flySvc("api")).RunOnce(context.Background())[0]
	if len(r.Findings) != 1 || r.InvestigationID != "" {
		t.Fatalf("result = %+v", r)
	}
	if len(r.Errors) != 1 || !strings.Contains(r.Errors[0], "no LLM is configured") {
		t.Errorf("errors = %v", r.Errors)
	}
}

type fakeSummary struct{ calls [][]string }

func (f *fakeSummary) Healthy(_ context.Context, services []string) error {
	f.calls = append(f.calls, services)
	return nil
}

func TestRunOnceSummary(t *testing.T) {
	healthy := map[string][]metrics.Series{"volume_used_percent": {series(nil, 50)}}
	tests := []struct {
		name     string
		src      *fakeSource
		services []*stromboliv1.Service
		want     []string
	}{
		{"all healthy", &fakeSource{series: healthy}, []*stromboliv1.Service{flySvc("api"), flySvc("web"), {Name: "worker"}}, []string{"api", "web"}},
		{"unhealthy", &fakeSource{series: map[string][]metrics.Series{"volume_used_percent": {series(nil, 99)}}}, []*stromboliv1.Service{flySvc("api")}, nil},
		{"check failed", &fakeSource{series: healthy, fail: "cpu_percent"}, []*stromboliv1.Service{flySvc("api")}, nil},
		{"nothing checked", &fakeSource{}, []*stromboliv1.Service{{Name: "worker"}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sum := &fakeSummary{}
			newChecker(tt.src, &fakeStarter{}, tt.services...).WithSummary(sum).RunOnce(context.Background())
			if tt.want == nil {
				if len(sum.calls) != 0 {
					t.Errorf("summary posted: %v", sum.calls)
				}
				return
			}
			if len(sum.calls) != 1 || strings.Join(sum.calls[0], ",") != strings.Join(tt.want, ",") {
				t.Errorf("summary calls = %v, want one with %v", sum.calls, tt.want)
			}
		})
	}
}
