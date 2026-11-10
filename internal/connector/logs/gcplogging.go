package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn"
)

// GCPLogging reads Cloud Logging through Google's remote MCP server
// (logging.googleapis.com/mcp). Selectors are Logging query language
// filters, e.g. resource.type="cloud_run_revision" AND
// resource.labels.service_name="api".
type GCPLogging struct {
	project string
	mcp     *mcpconn.Client
}

// GCPLoggingTools are the (read-only) tools the connector calls.
var GCPLoggingTools = []string{"list_log_entries", "list_log_names"}

func NewGCPLogging(project string, c *mcpconn.Client) *GCPLogging {
	return &GCPLogging{project: project, mcp: c}
}

const (
	// gcpPageSize is Cloud Logging's maximum page size.
	gcpPageSize = 1000
	// gcpMaxCount caps Count: there is no count tool, so it pages through
	// the entries. Counts above it are reported as the cap.
	gcpMaxCount = 5000
)

type gcpEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Severity  string    `json:"severity"`
	Resource  struct {
		Type   string            `json:"type"`
		Labels map[string]string `json:"labels"`
	} `json:"resource"`
	TextPayload  string          `json:"textPayload"`
	JSONPayload  json.RawMessage `json:"jsonPayload"`
	ProtoPayload json.RawMessage `json:"protoPayload"`
	HTTPRequest  *struct {
		RequestMethod string `json:"requestMethod"`
		RequestURL    string `json:"requestUrl"`
		Status        int    `json:"status"`
		Latency       string `json:"latency"`
	} `json:"httpRequest"`
}

type gcpEntries struct {
	Entries       []gcpEntry `json:"entries"`
	NextPageToken string     `json:"nextPageToken"`
}

func (g *GCPLogging) list(ctx context.Context, filter string, pageSize int, pageToken string) (gcpEntries, error) {
	args := map[string]any{
		"resourceNames": []string{"projects/" + g.project},
		"filter":        filter,
		"orderBy":       "timestamp desc",
		"pageSize":      pageSize,
	}
	if pageToken != "" {
		args["pageToken"] = pageToken
	}
	var out gcpEntries
	err := g.mcp.Call(ctx, "list_log_entries", args, &out)
	return out, err
}

func (g *GCPLogging) Query(ctx context.Context, q Query) ([]Line, error) {
	limit := q.Limit
	if limit <= 0 || limit > maxLines {
		limit = maxLines
	}
	filter := gcpFilter(q)
	var lines []Line
	token := ""
	for len(lines) < limit {
		page, err := g.list(ctx, filter, min(gcpPageSize, limit-len(lines)), token)
		if err != nil {
			return nil, err
		}
		for _, e := range page.Entries {
			lines = append(lines, e.line())
		}
		if page.NextPageToken == "" || len(page.Entries) == 0 {
			break
		}
		token = page.NextPageToken
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Time.After(lines[j].Time) })
	return lines, nil
}

func (g *GCPLogging) Count(ctx context.Context, q Query) (int64, error) {
	filter := gcpFilter(q)
	var n int64
	token := ""
	for n < gcpMaxCount {
		page, err := g.list(ctx, filter, gcpPageSize, token)
		if err != nil {
			return 0, err
		}
		n += int64(len(page.Entries))
		if page.NextPageToken == "" || len(page.Entries) == 0 {
			break
		}
		token = page.NextPageToken
	}
	return min(n, gcpMaxCount), nil
}

// gcpServiceLabels is the label that names a service, per resource type.
var gcpServiceLabels = map[string]string{
	"cloud_run_revision":  "service_name",
	"cloud_run_job":       "job_name",
	"cloud_function":      "function_name",
	"cloud_scheduler_job": "job_id",
	"cloud_tasks_queue":   "queue_id",
	"pubsub_subscription": "subscription_id",
	"pubsub_topic":        "topic_id",
	"k8s_container":       "container_name",
	"gae_app":             "module_id",
}

// Services proposes one service per Cloud Run service, job, function,
// scheduler job, queue... seen in the last hour of logs.
func (g *GCPLogging) Services(ctx context.Context) ([]Service, error) {
	since := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	types := make([]string, 0, len(gcpServiceLabels))
	for t := range gcpServiceLabels {
		types = append(types, fmt.Sprintf("resource.type=%q", t))
	}
	sort.Strings(types)
	filter := fmt.Sprintf("timestamp>=%q AND (%s)", since, strings.Join(types, " OR "))
	page, err := g.list(ctx, filter, gcpPageSize, "")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Service
	for _, e := range page.Entries {
		label := gcpServiceLabels[e.Resource.Type]
		name := e.Resource.Labels[label]
		if label == "" || name == "" {
			continue
		}
		sel := fmt.Sprintf("resource.type=%q AND resource.labels.%s=%q", e.Resource.Type, label, name)
		if seen[sel] {
			continue
		}
		seen[sel] = true
		out = append(out, Service{Name: name, Selector: sel})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (g *GCPLogging) Check(ctx context.Context) error {
	var out struct {
		LogNames []string `json:"logNames"`
	}
	return g.mcp.Call(ctx, "list_log_names", map[string]any{"parent": "projects/" + g.project, "pageSize": 1}, &out)
}

// gcpFilter turns a query into a Logging query language filter. The regular
// expression is matched on the text payload and the JSON message; the error
// filter also matches severity ERROR and above and HTTP 5xx, which Cloud
// Logging records as fields rather than text.
func gcpFilter(q Query) string {
	parts := []string{}
	if s := strings.TrimSpace(q.Selector); s != "" {
		parts = append(parts, "("+s+")")
	}
	if !q.Start.IsZero() {
		parts = append(parts, fmt.Sprintf("timestamp>=%q", q.Start.UTC().Format(time.RFC3339Nano)))
	}
	if !q.End.IsZero() {
		parts = append(parts, fmt.Sprintf("timestamp<=%q", q.End.UTC().Format(time.RFC3339Nano)))
	}
	if q.Filter != "" {
		re := gcpString(q.Filter)
		alts := []string{"textPayload=~" + re, "jsonPayload.message=~" + re, "jsonPayload.msg=~" + re}
		if q.Filter == ErrorFilter {
			alts = append([]string{"severity>=ERROR", "httpRequest.status>=500"}, alts...)
		}
		parts = append(parts, "("+strings.Join(alts, " OR ")+")")
	}
	return strings.Join(parts, " AND ")
}

// gcpString quotes s as a Logging query language string.
func gcpString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func (e gcpEntry) line() Line {
	labels := map[string]string{"resource_type": e.Resource.Type}
	for k, v := range e.Resource.Labels {
		if k != "project_id" {
			labels[k] = v
		}
	}
	if e.Severity != "" && e.Severity != "DEFAULT" {
		labels["severity"] = e.Severity
	}
	return Line{Time: e.Timestamp.UTC(), Labels: labels, Text: e.text()}
}

// text is the entry's message, prefixed with its severity like the other
// sources' JSON lines so that patterns group the same way.
func (e gcpEntry) text() string {
	var msg string
	switch {
	case e.TextPayload != "":
		msg = messageText(e.TextPayload)
	case len(e.JSONPayload) > 0 && string(e.JSONPayload) != "null":
		msg = messageText(string(e.JSONPayload))
		if strings.HasPrefix(msg, "{") {
			msg = jsonSummary(e.JSONPayload)
		}
	case e.HTTPRequest != nil:
		msg = fmt.Sprintf("%s %s %d %s", e.HTTPRequest.RequestMethod, e.HTTPRequest.RequestURL, e.HTTPRequest.Status, e.HTTPRequest.Latency)
	case len(e.ProtoPayload) > 0:
		var p struct {
			MethodName string `json:"methodName"`
			Status     *struct {
				Message string `json:"message"`
			} `json:"status"`
		}
		_ = json.Unmarshal(e.ProtoPayload, &p)
		msg = p.MethodName
		if p.Status != nil && p.Status.Message != "" {
			msg += ": " + p.Status.Message
		}
	}
	if len(msg) > 2000 {
		msg = msg[:2000] + "…"
	}
	// Always the same prefix, so that patterns group by severity and message.
	sev := strings.ToLower(e.Severity)
	if sev != "" && sev != "default" && !strings.HasPrefix(msg, "level=") {
		msg = "level=" + sev + " " + msg
	}
	return msg
}

// jsonSummary renders a JSON payload without a message field (Google
// services' own logs, e.g. Cloud Scheduler's AttemptFinished) as its short
// type then its scalar fields, e.g. "AttemptFinished debugInfo=... url=...".
// Resource names already in the labels are left out.
func jsonSummary(raw json.RawMessage) string {
	var ev map[string]any
	if json.Unmarshal(raw, &ev) != nil {
		return string(raw)
	}
	var parts []string
	if t, ok := ev["@type"].(string); ok {
		parts = append(parts, t[strings.LastIndexAny(t, "./")+1:])
	}
	keys := make([]string, 0, len(ev))
	for k := range ev {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == "@type" || k == "jobName" || k == "scheduledTime" {
			continue
		}
		switch v := ev[k].(type) {
		case string:
			parts = append(parts, fmt.Sprintf("%s=%s", k, v))
		case float64, bool:
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		case map[string]any:
			if m, ok := v["message"].(string); ok {
				parts = append(parts, fmt.Sprintf("%s=%s", k, m))
			}
		}
	}
	return strings.Join(parts, " ")
}
