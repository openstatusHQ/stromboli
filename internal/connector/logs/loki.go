package logs

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
)

// LokiConfig configures a Loki connector.
type LokiConfig struct {
	URL         string
	TenantID    string
	Username    string
	Password    string
	BearerToken string
}

type Loki struct {
	cfg  LokiConfig
	http *http.Client
}

func NewLoki(cfg LokiConfig) (*Loki, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("loki: url is required")
	}
	if _, err := url.Parse(cfg.URL); err != nil {
		return nil, fmt.Errorf("loki: invalid url: %w", err)
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	return &Loki{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

// maxLines caps a single query regardless of what the caller asks for.
const maxLines = 2000

func (l *Loki) Query(ctx context.Context, q Query) ([]Line, error) {
	limit := q.Limit
	if limit <= 0 || limit > maxLines {
		limit = maxLines
	}
	params := url.Values{
		"query":     {logQL(q)},
		"start":     {strconv.FormatInt(q.Start.UnixNano(), 10)},
		"end":       {strconv.FormatInt(q.End.UnixNano(), 10)},
		"limit":     {strconv.Itoa(limit)},
		"direction": {"backward"},
	}
	var resp struct {
		Data struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Stream map[string]string `json:"stream"`
				Values [][2]string       `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := l.get(ctx, "/loki/api/v1/query_range", params, &resp); err != nil {
		return nil, err
	}
	var lines []Line
	for _, r := range resp.Data.Result {
		for _, v := range r.Values {
			ns, err := strconv.ParseInt(v[0], 10, 64)
			if err != nil {
				continue
			}
			lines = append(lines, Line{Time: time.Unix(0, ns).UTC(), Labels: r.Stream, Text: messageText(v[1])})
		}
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].Time.After(lines[j].Time) })
	return lines, nil
}

func (l *Loki) Count(ctx context.Context, q Query) (int64, error) {
	window := q.End.Sub(q.Start)
	if window < time.Second {
		window = time.Second
	}
	expr := fmt.Sprintf("sum(count_over_time(%s [%ds]))", logQL(q), int64(window.Seconds()))
	params := url.Values{
		"query": {expr},
		"time":  {strconv.FormatInt(q.End.UnixNano(), 10)},
	}
	var resp struct {
		Data struct {
			Result []struct {
				Value [2]any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := l.get(ctx, "/loki/api/v1/query", params, &resp); err != nil {
		return 0, err
	}
	if len(resp.Data.Result) == 0 {
		return 0, nil
	}
	s, _ := resp.Data.Result[0].Value[1].(string)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("loki: unexpected count value %q", s)
	}
	return int64(f), nil
}

// serviceLabels are checked in order when discovering services.
// fly_app_name is set by Fly's log shipper (flyio/log-shipper).
var serviceLabels = []string{"service_name", "service", "app", "fly_app_name", "job", "container"}

func (l *Loki) Services(ctx context.Context) ([]Service, error) {
	end := time.Now()
	start := end.Add(-24 * time.Hour)
	params := url.Values{
		"start": {strconv.FormatInt(start.UnixNano(), 10)},
		"end":   {strconv.FormatInt(end.UnixNano(), 10)},
	}
	var labels struct {
		Data []string `json:"data"`
	}
	if err := l.get(ctx, "/loki/api/v1/labels", params, &labels); err != nil {
		return nil, err
	}
	for _, name := range serviceLabels {
		for _, have := range labels.Data {
			if have != name {
				continue
			}
			var values struct {
				Data []string `json:"data"`
			}
			if err := l.get(ctx, "/loki/api/v1/label/"+url.PathEscape(name)+"/values", params, &values); err != nil {
				return nil, err
			}
			out := make([]Service, 0, len(values.Data))
			for _, v := range values.Data {
				out = append(out, Service{Name: v, Selector: fmt.Sprintf("{%s=%q}", name, v)})
			}
			return out, nil
		}
	}
	return nil, nil
}

func (l *Loki) Check(ctx context.Context) error {
	var labels struct {
		Status string `json:"status"`
	}
	return l.get(ctx, "/loki/api/v1/labels", nil, &labels)
}

func (l *Loki) get(ctx context.Context, path string, params url.Values, out any) error {
	u := l.cfg.URL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if l.cfg.TenantID != "" {
		req.Header.Set("X-Scope-OrgID", l.cfg.TenantID)
	}
	switch {
	case l.cfg.BearerToken != "":
		req.Header.Set("Authorization", "Bearer "+l.cfg.BearerToken)
	case l.cfg.Username != "":
		req.SetBasicAuth(l.cfg.Username, l.cfg.Password)
	}
	resp, err := l.http.Do(req)
	if err != nil {
		return fmt.Errorf("loki: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("loki: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("loki: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

// logQL builds `selector |~ "filter"` with the filter safely quoted.
func logQL(q Query) string {
	if q.Filter == "" {
		return q.Selector
	}
	return q.Selector + " |~ " + strconv.Quote(q.Filter)
}
