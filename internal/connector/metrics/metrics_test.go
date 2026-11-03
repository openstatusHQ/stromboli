package metrics

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMatchersAndExpand(t *testing.T) {
	m := Matchers([]string{`{app="checker"}`, ` region="ams" `, ""})
	if m != `app="checker",region="ams"` {
		t.Fatalf("Matchers = %q", m)
	}
	p, _ := FindPreset("fly", "memory_used_percent")
	expr := Expand(p.Expr, m)
	if strings.Contains(expr, "$selector") || !strings.Contains(expr, `fly_instance_memory_mem_available{app="checker",region="ams"}`) {
		t.Fatalf("Expand = %s", expr)
	}
	for _, p := range FlyPresets {
		if !strings.Contains(p.Expr, "{$selector") {
			t.Errorf("preset %s does not scope to the service", p.Name)
		}
	}
	if _, ok := FindPreset("prometheus", "memory_used_percent"); ok {
		t.Error("prometheus connectors have no presets")
	}
}

func TestSummarize(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	series := func(inst string, vals ...float64) Series {
		s := Series{Labels: map[string]string{"instance": inst}}
		for i, v := range vals {
			s.Samples = append(s.Samples, Sample{Time: t0.Add(time.Duration(i) * time.Minute), Value: v})
		}
		return s
	}
	cur := []Series{series("a", 40, 50, 60), series("b", 70, 90, 98), series("c")}
	base := []Series{series("b", 30, 30)}
	out := Summarize(cur, base, 5, 2)
	if len(out) != 2 {
		t.Fatalf("empty series should be dropped, got %d", len(out))
	}
	b := out[0]
	if b.Labels["instance"] != "b" || b.Max != 98 || b.Min != 70 || b.Last != 98 || b.Avg != 86 {
		t.Errorf("unexpected summary %+v", b)
	}
	if b.Baseline == nil || *b.Baseline != 30 {
		t.Errorf("baseline not matched: %v", b.Baseline)
	}
	if out[1].Baseline != nil {
		t.Error("series without a baseline should have none")
	}
	if len(b.Points) != 2 || b.Points[0] != 70 || b.Points[1] != 94 {
		t.Errorf("points = %v", b.Points)
	}
}

func TestFormatValue(t *testing.T) {
	for _, c := range []struct {
		v    float64
		unit string
		want string
	}{
		{512, "bytes", "512 B"},
		{3.5 * 1024 * 1024 * 1024, "bytes", "3.5 GiB"},
		{2048, "bytes/s", "2.0 KiB/s"},
		{93.456, "%", "93.5%"},
		{0.12345, "s", "0.1235 s"},
		{3, "", "3"},
	} {
		if got := FormatValue(c.v, c.unit); got != c.want {
			t.Errorf("FormatValue(%v, %q) = %q, want %q", c.v, c.unit, got, c.want)
		}
	}
}

// fakeFly serves the Prometheus API under /prometheus/<org>.
func fakeFly(t *testing.T, wantAuth string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != wantAuth {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"status":"error","error":"unauthorized"}`)
			return
		}
		q := r.URL.Query()
		switch r.URL.Path {
		case "/prometheus/acme/api/v1/query":
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		case "/prometheus/acme/api/v1/query_range":
			if !strings.Contains(q.Get("query"), `app="checker"`) {
				t.Errorf("query not scoped: %s", q.Get("query"))
			}
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[
				{"metric":{"instance":"148e","region":"ams"},"values":[[1700000000,"91.5"],[1700000060,"97.25"]]}]}}`)
		case "/prometheus/acme/api/v1/label/app/values":
			fmt.Fprint(w, `{"status":"success","data":["checker","web"]}`)
		case "/prometheus/acme/api/v1/label/__name__/values":
			if q.Get("match[]") != `{app="checker"}` {
				t.Errorf("match[] = %q", q.Get("match[]"))
			}
			fmt.Fprint(w, `{"status":"success","data":["fly_instance_up","fly_instance_memory_mem_total"]}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestFly(t *testing.T) {
	ctx := context.Background()
	srv := fakeFly(t, "FlyV1 fm2_abc")
	defer srv.Close()

	// Tokens from `fly tokens create` get the FlyV1 scheme, with or without the prefix.
	for _, tok := range []string{"FlyV1 fm2_abc", "fm2_abc"} {
		p, err := NewFly("acme", tok, srv.URL+"/prometheus/")
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Check(ctx); err != nil {
			t.Fatalf("token %q: %v", tok, err)
		}
	}
	p, _ := NewFly("acme", "personal-token", srv.URL+"/prometheus")
	if err := p.Check(ctx); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("a non-macaroon token is sent as Bearer, got %v", err)
	}

	p, _ = NewFly("acme", "fm2_abc", srv.URL+"/prometheus")
	pr, _ := FindPreset("fly", "memory_used_percent")
	series, err := p.Range(ctx, Expand(pr.Expr, `app="checker"`), time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || series[0].Labels["instance"] != "148e" || len(series[0].Samples) != 2 || series[0].Samples[1].Value != 97.25 {
		t.Fatalf("unexpected series %+v", series)
	}

	svcs, err := p.Services(ctx)
	if err != nil || len(svcs) != 2 || svcs[0].Selector != `app="checker"` {
		t.Fatalf("Services = %+v, %v", svcs, err)
	}
	names, err := p.MetricNames(ctx, `app="checker"`, time.Now().Add(-time.Hour), time.Now())
	if err != nil || len(names) != 2 || names[0] != "fly_instance_memory_mem_total" {
		t.Fatalf("MetricNames = %v, %v", names, err)
	}
}
