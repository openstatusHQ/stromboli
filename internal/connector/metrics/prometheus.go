package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openstatushq/opensre/internal/connector/fly"
)

// PrometheusConfig configures a Prometheus HTTP API client.
type PrometheusConfig struct {
	URL         string
	TenantID    string
	Username    string
	Password    string
	BearerToken string
	// Authorization, when set, is sent verbatim as the Authorization header.
	Authorization string
	// ServiceLabel is the label discovery proposes one service per value of.
	// Defaults to the first of serviceLabels that has values.
	ServiceLabel string
}

// Prometheus queries any backend speaking the Prometheus HTTP API.
type Prometheus struct {
	cfg  PrometheusConfig
	http *http.Client
}

func NewPrometheus(cfg PrometheusConfig) (*Prometheus, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("prometheus: url is required")
	}
	if _, err := url.Parse(cfg.URL); err != nil {
		return nil, fmt.Errorf("prometheus: invalid url: %w", err)
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	return &Prometheus{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

// FlyURL is Fly.io's managed Prometheus endpoint for an organisation.
const FlyURL = "https://api.fly.io/prometheus/"

// NewFly returns a client for Fly.io's managed Prometheus, which holds the
// metrics of every machine in the organisation. Tokens from `fly tokens
// create` start with "FlyV1 " and are sent as is; others (from `fly auth
// token`) are sent as bearer tokens.
func NewFly(org, token, baseURL string) (*Prometheus, error) {
	if org == "" || token == "" {
		return nil, fmt.Errorf("fly: org and token are required")
	}
	if baseURL == "" {
		baseURL = FlyURL
	}
	return NewPrometheus(PrometheusConfig{
		URL:           strings.TrimRight(baseURL, "/") + "/" + url.PathEscape(org),
		Authorization: fly.Authorization(token),
		ServiceLabel:  "app",
	})
}

// maxSeries and maxPoints bound a range query's response.
const (
	maxSeries = 50
	maxPoints = 500
)

// Range evaluates expr over [start, end]. The step is chosen so each series
// has at most maxPoints samples, and at least 15s.
func (p *Prometheus) Range(ctx context.Context, expr string, start, end time.Time) ([]Series, error) {
	step := max(end.Sub(start)/maxPoints, 15*time.Second).Truncate(time.Second)
	params := url.Values{
		"query": {expr},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {strconv.FormatInt(int64(step.Seconds()), 10)},
	}
	var resp struct {
		Data struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Values [][2]any          `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := p.get(ctx, "/api/v1/query_range", params, &resp); err != nil {
		return nil, err
	}
	out := make([]Series, 0, len(resp.Data.Result))
	for _, r := range resp.Data.Result {
		s := Series{Labels: r.Metric}
		for _, v := range r.Values {
			ts, ok := v[0].(float64)
			str, _ := v[1].(string)
			f, err := strconv.ParseFloat(str, 64)
			if !ok || err != nil {
				continue
			}
			s.Samples = append(s.Samples, Sample{Time: time.Unix(0, int64(ts*1e9)).UTC(), Value: f})
		}
		out = append(out, s)
	}
	if len(out) > maxSeries {
		out = topSeries(out, maxSeries)
	}
	return out, nil
}

// topSeries keeps the n series with the highest peak.
func topSeries(series []Series, n int) []Series {
	peak := func(s Series) float64 {
		m := 0.0
		for i, p := range s.Samples {
			if i == 0 || p.Value > m {
				m = p.Value
			}
		}
		return m
	}
	sort.SliceStable(series, func(i, j int) bool { return peak(series[i]) > peak(series[j]) })
	return series[:n]
}

// MetricNames lists the metric names that have series matching the label
// matchers in [start, end].
func (p *Prometheus) MetricNames(ctx context.Context, matchers string, start, end time.Time) ([]string, error) {
	params := url.Values{
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
	}
	if matchers != "" {
		params.Set("match[]", "{"+matchers+"}")
	}
	var resp struct {
		Data []string `json:"data"`
	}
	if err := p.get(ctx, "/api/v1/label/__name__/values", params, &resp); err != nil {
		return nil, err
	}
	sort.Strings(resp.Data)
	return resp.Data, nil
}

// serviceLabels are checked in order when discovering services.
var serviceLabels = []string{"service_name", "service", "app", "job"}

// Service is a service discovered in a metrics backend, with the label
// matcher that selects its series.
type Service struct {
	Name     string
	Selector string
}

// Services proposes one service per value of the service label.
func (p *Prometheus) Services(ctx context.Context) ([]Service, error) {
	end := time.Now()
	params := url.Values{
		"start": {strconv.FormatInt(end.Add(-24*time.Hour).Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
	}
	labels := serviceLabels
	if p.cfg.ServiceLabel != "" {
		labels = []string{p.cfg.ServiceLabel}
	}
	for _, name := range labels {
		var values struct {
			Data []string `json:"data"`
		}
		if err := p.get(ctx, "/api/v1/label/"+url.PathEscape(name)+"/values", params, &values); err != nil {
			return nil, err
		}
		if len(values.Data) == 0 {
			continue
		}
		out := make([]Service, 0, len(values.Data))
		for _, v := range values.Data {
			out = append(out, Service{Name: v, Selector: fmt.Sprintf("%s=%q", name, v)})
		}
		return out, nil
	}
	return nil, nil
}

// Check verifies connectivity and credentials with a trivial query.
func (p *Prometheus) Check(ctx context.Context) error {
	var resp struct {
		Status string `json:"status"`
	}
	return p.get(ctx, "/api/v1/query", url.Values{"query": {"vector(1)"}}, &resp)
}

func (p *Prometheus) get(ctx context.Context, path string, params url.Values, out any) error {
	u := p.cfg.URL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if p.cfg.TenantID != "" {
		req.Header.Set("X-Scope-OrgID", p.cfg.TenantID)
	}
	switch {
	case p.cfg.Authorization != "":
		req.Header.Set("Authorization", p.cfg.Authorization)
	case p.cfg.BearerToken != "":
		req.Header.Set("Authorization", "Bearer "+p.cfg.BearerToken)
	case p.cfg.Username != "":
		req.SetBasicAuth(p.cfg.Username, p.cfg.Password)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return fmt.Errorf("prometheus: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("prometheus: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return fmt.Errorf("prometheus: %s: %s", resp.Status, e.Error)
		}
		return fmt.Errorf("prometheus: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}
