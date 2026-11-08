// Package metrics queries Prometheus-compatible metric backends (Prometheus,
// Mimir, VictoriaMetrics, Fly.io's managed Prometheus) and summarises series
// into compact evidence for the LLM.
package metrics

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Kinds lists the connector kinds that are metric sources.
var Kinds = []string{"prometheus", "fly", "gcp_monitoring"}

// Sample is one point of a series.
type Sample struct {
	Time  time.Time
	Value float64
}

// Series is one labelled time series.
type Series struct {
	Labels  map[string]string
	Samples []Sample
}

// Matchers turns a service's catalog selectors into a PromQL label matcher
// list without braces: ['app="api"', 'region="ams"'] → `app="api",region="ams"`.
// Braces around a selector are accepted and stripped.
func Matchers(selectors []string) string {
	parts := make([]string, 0, len(selectors))
	for _, s := range selectors {
		s = strings.TrimSpace(s)
		s = strings.TrimSuffix(strings.TrimPrefix(s, "{"), "}")
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ",")
}

// Expand replaces $selector in a PromQL expression with the service's label
// matchers, so `fly_instance_load_average{$selector}` targets one service.
func Expand(expr, matchers string) string {
	return strings.ReplaceAll(expr, "$selector", matchers)
}

// Preset is a named, ready-made query.
type Preset struct {
	Name        string
	Description string
	Unit        string
	// Expr uses $selector for the service's label matchers.
	Expr string
	// Brief marks presets shown in the incident brief.
	Brief bool
	// Requires is a label the service's selectors must set for the preset
	// to apply, e.g. subscription_id for Pub/Sub presets on a GCP connector
	// that also serves Cloud Run and Cloud Tasks.
	Requires string
}

// Applies reports whether the preset fits a service with these matchers.
func (p Preset) Applies(matchers string) bool {
	return p.Requires == "" || strings.Contains(matchers, p.Requires+"=")
}

// FlyPresets are queries over the metrics Fly.io exposes for every app
// (https://fly.io/docs/monitoring/metrics/), per machine (instance) and region.
var FlyPresets = []Preset{
	{
		Name: "memory_used_percent", Unit: "%", Brief: true,
		Description: "Machine memory in use (total - available) as a percentage of total, per instance.",
		Expr:        `100 * (1 - max by (instance, region) (fly_instance_memory_mem_available{$selector}) / max by (instance, region) (fly_instance_memory_mem_total{$selector}))`,
	},
	{
		Name: "memory_used_bytes", Unit: "bytes",
		Description: "Machine memory in use (total - available), per instance.",
		Expr:        `max by (instance, region) (fly_instance_memory_mem_total{$selector}) - max by (instance, region) (fly_instance_memory_mem_available{$selector})`,
	},
	{
		Name: "swap_used_bytes", Unit: "bytes",
		Description: "Swap in use, per instance. Growing swap usually means memory pressure.",
		Expr:        `max by (instance, region) (fly_instance_memory_swap_total{$selector}) - max by (instance, region) (fly_instance_memory_swap_free{$selector})`,
	},
	{
		Name: "cpu_percent", Unit: "%", Brief: true,
		Description: "CPU busy time (all modes but idle) as a percentage of all CPU time, per instance.",
		Expr:        `100 * sum by (instance, region) (rate(fly_instance_cpu{$selector, mode!="idle"}[1m])) / sum by (instance, region) (rate(fly_instance_cpu{$selector}[1m]))`,
	},
	{
		Name: "cpu_throttled_seconds_per_second", Unit: "s/s", Brief: true,
		Description: "CPU time throttled after exhausting the machine's CPU quota, per instance (shared CPUs burst above their baseline until the balance runs out). Above 0 means the machine is CPU-starved.",
		Expr:        `sum by (instance, region) (rate(fly_instance_cpu_throttle{$selector}[1m])) / 100`,
	},
	{
		Name: "load_average", Unit: "",
		Description: "1-minute load average, per instance.",
		Expr:        `max by (instance, region) (fly_instance_load_average{$selector, minutes="1"})`,
	},
	{
		Name: "disk_used_percent", Unit: "%", Brief: true,
		Description: "Root filesystem in use, per instance. The root filesystem is reset from the image when the machine restarts.",
		Expr:        `100 * (1 - max by (instance, region) (fly_instance_filesystem_blocks_avail{$selector}) / max by (instance, region) (fly_instance_filesystem_blocks{$selector}))`,
	},
	{
		Name: "volume_used_percent", Unit: "%", Brief: true,
		Description: "Fly volume space in use, per volume (id is the volume ID). Data on a volume survives restarts: a full volume needs more space or less data.",
		Expr:        `max by (id, region) (fly_volume_used_pct{$selector})`,
	},
	{
		Name: "net_recv_bytes_per_second", Unit: "bytes/s",
		Description: "Network bytes received, per instance.",
		Expr:        `sum by (instance, region) (rate(fly_instance_net_recv_bytes{$selector}[1m]))`,
	},
	{
		Name: "net_sent_bytes_per_second", Unit: "bytes/s",
		Description: "Network bytes sent, per instance.",
		Expr:        `sum by (instance, region) (rate(fly_instance_net_sent_bytes{$selector}[1m]))`,
	},
	{
		Name: "http_requests_per_second", Unit: "req/s", Brief: true,
		Description: "HTTP responses served by the Fly proxy, per region.",
		Expr:        `sum by (region) (rate(fly_app_http_responses_count{$selector}[1m]))`,
	},
	{
		Name: "http_5xx_per_second", Unit: "req/s", Brief: true,
		Description: "HTTP 5xx responses served by the Fly proxy, per region.",
		Expr:        `sum by (region) (rate(fly_app_http_responses_count{$selector, status=~"5.."}[1m]))`,
	},
	{
		Name: "http_p95_seconds", Unit: "s", Brief: true,
		Description: "95th percentile HTTP response time at the Fly proxy, per region.",
		Expr:        `histogram_quantile(0.95, sum by (le, region) (rate(fly_app_http_response_time_seconds_bucket{$selector}[1m])))`,
	},
	{
		Name: "concurrency", Unit: "",
		Description: "Concurrent connections or requests the proxy routes to each instance (compare with the app's soft/hard limits).",
		Expr:        `max by (instance, region) (fly_app_concurrency{$selector})`,
	},
	{
		Name: "oom_killed", Unit: "", Brief: true,
		Description: "1 when the instance's last exit was an out-of-memory kill, per instance.",
		Expr:        `max by (instance, region) (fly_instance_exit_oom{$selector})`,
	},
	{
		Name: "instances_up", Unit: "",
		Description: "Number of machines reporting metrics, per region. A drop means machines stopped, crashed or were replaced.",
		Expr:        `count by (region) (fly_instance_up{$selector})`,
	},
}

// PresetsFor returns the presets available for a connector kind.
func PresetsFor(kind string) []Preset {
	switch kind {
	case "fly":
		return FlyPresets
	case "gcp_monitoring":
		return GCPPresets
	}
	return nil
}

// FindPreset looks up a preset by name.
func FindPreset(kind, name string) (Preset, bool) {
	for _, p := range PresetsFor(kind) {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}

// Summary condenses one series for the LLM.
type Summary struct {
	Labels   map[string]string `json:"labels,omitempty"`
	Min      float64           `json:"min"`
	Max      float64           `json:"max"`
	Avg      float64           `json:"avg"`
	Last     float64           `json:"last"`
	LastTime time.Time         `json:"last_time"`
	// Baseline is the average over the preceding window, when known.
	Baseline *float64 `json:"baseline_avg,omitempty"`
	// Points is the series downsampled to a few evenly spaced averages.
	Points []float64 `json:"points,omitempty"`
}

// Summarize reduces series to per-series stats, sorted by max descending and
// capped at limit. baseline series are matched by labels.
func Summarize(series, baseline []Series, limit, points int) []Summary {
	base := map[string]float64{}
	for _, s := range baseline {
		if len(s.Samples) > 0 {
			base[labelKey(s.Labels)] = stats(s.Samples).Avg
		}
	}
	out := make([]Summary, 0, len(series))
	for _, s := range series {
		if len(s.Samples) == 0 {
			continue
		}
		sum := stats(s.Samples)
		sum.Labels = s.Labels
		if b, ok := base[labelKey(s.Labels)]; ok {
			sum.Baseline = &b
		}
		sum.Points = downsample(s.Samples, points)
		out = append(out, sum)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Max > out[j].Max })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func stats(samples []Sample) Summary {
	s := Summary{Min: math.Inf(1), Max: math.Inf(-1)}
	var total float64
	for _, p := range samples {
		s.Min = math.Min(s.Min, p.Value)
		s.Max = math.Max(s.Max, p.Value)
		total += p.Value
	}
	last := samples[len(samples)-1]
	s.Avg = round(total / float64(len(samples)))
	s.Min, s.Max, s.Last, s.LastTime = round(s.Min), round(s.Max), round(last.Value), last.Time
	return s
}

func downsample(samples []Sample, n int) []float64 {
	if n <= 0 {
		return nil
	}
	if len(samples) <= n {
		out := make([]float64, len(samples))
		for i, p := range samples {
			out[i] = round(p.Value)
		}
		return out
	}
	out := make([]float64, n)
	for i := range n {
		lo, hi := i*len(samples)/n, (i+1)*len(samples)/n
		var t float64
		for _, p := range samples[lo:hi] {
			t += p.Value
		}
		out[i] = round(t / float64(hi-lo))
	}
	return out
}

// round keeps 3 significant decimals so output stays short.
func round(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	a := math.Abs(v)
	switch {
	case a >= 1000:
		return math.Round(v)
	case a >= 1:
		return math.Round(v*100) / 100
	default:
		return math.Round(v*10000) / 10000
	}
}

func labelKey(l map[string]string) string {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%q,", k, l[k])
	}
	return b.String()
}

// FormatLabels renders labels as `k=v k=v`, or "all" when empty.
func FormatLabels(l map[string]string) string {
	if len(l) == 0 {
		return "all"
	}
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + l[k]
	}
	return strings.Join(parts, " ")
}

// FormatValue renders a value with its unit, scaling bytes.
func FormatValue(v float64, unit string) string {
	if math.IsNaN(v) {
		return "NaN"
	}
	switch unit {
	case "bytes", "bytes/s":
		suffix := strings.TrimPrefix(unit, "bytes")
		for _, u := range []string{"B", "KiB", "MiB", "GiB", "TiB"} {
			if math.Abs(v) < 1024 || u == "TiB" {
				if u == "B" {
					return fmt.Sprintf("%.0f %s%s", v, u, suffix)
				}
				return fmt.Sprintf("%.1f %s%s", v, u, suffix)
			}
			v /= 1024
		}
	case "%":
		return fmt.Sprintf("%.1f%%", v)
	case "":
		return fmt.Sprintf("%g", round(v))
	}
	return fmt.Sprintf("%g %s", round(v), unit)
}
