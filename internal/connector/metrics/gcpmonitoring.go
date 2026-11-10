package metrics

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn"
)

// GCPMonitoring queries Cloud Monitoring with PromQL through Google's remote
// MCP server (monitoring.googleapis.com/mcp). Metric names follow Cloud
// Monitoring's PromQL mapping: pubsub.googleapis.com/subscription/
// num_undelivered_messages is pubsub_googleapis_com:subscription_num_undelivered_messages,
// and resource labels (subscription_id, service_name, queue_id...) are labels.
type GCPMonitoring struct {
	project string
	mcp     *mcpconn.Client
}

// GCPMonitoringTools are the (read-only) tools the connector calls.
var GCPMonitoringTools = []string{"query_range", "list_metric_descriptors"}

var _ Source = (*GCPMonitoring)(nil)

func NewGCPMonitoring(project string, c *mcpconn.Client) *GCPMonitoring {
	return &GCPMonitoring{project: project, mcp: c}
}

func (g *GCPMonitoring) Range(ctx context.Context, expr string, start, end time.Time) ([]Series, error) {
	var resp struct {
		matrixResponse
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	err := g.mcp.Call(ctx, "query_range", map[string]any{
		"name":     "projects/" + g.project,
		"location": "global",
		"query":    expr,
		"start":    start.UTC().Format(time.RFC3339),
		"end":      end.UTC().Format(time.RFC3339),
		"step":     fmt.Sprintf("%ds", int64(rangeStep(start, end).Seconds())),
	}, &resp)
	if err != nil {
		return nil, unknownMetricHint(err, expr)
	}
	if resp.Status == "error" {
		return nil, fmt.Errorf("cloud monitoring: %s", resp.Error)
	}
	return resp.series(), nil
}

// unknownMetricHint explains Cloud Monitoring's empty error for a query on
// a metric that has no data in the project.
func unknownMetricHint(err error, expr string) error {
	var te *mcpconn.ToolError
	if ok := asToolError(err, &te); ok && te.Message == "" {
		return fmt.Errorf("cloud monitoring rejected the query without a reason; usually a metric in %q has no data in this project (see list_metrics)", expr)
	}
	return err
}

// gcpDomains maps a selector's resource label to the metric prefix of the
// product it belongs to, to keep list_metrics relevant.
var gcpDomains = map[string]string{
	"subscription_id": "pubsub.googleapis.com/subscription/",
	"topic_id":        "pubsub.googleapis.com/topic/",
	"queue_id":        "cloudtasks.googleapis.com/",
	"service_name":    "run.googleapis.com/",
	"job_id":          "cloudscheduler.googleapis.com/",
}

// maxMetricNames bounds MetricNames.
const maxMetricNames = 500

// MetricNames lists the metrics with recent data, as PromQL names. When the
// matchers name a resource label (subscription_id, queue_id...), only that
// product's metrics are listed. Distributions are listed with _bucket, for
// histogram_quantile.
func (g *GCPMonitoring) MetricNames(ctx context.Context, matchers string, _, _ time.Time) ([]string, error) {
	args := map[string]any{"name": "projects/" + g.project, "activeOnly": true, "pageSize": maxMetricNames}
	for label, prefix := range gcpDomains {
		if strings.Contains(matchers, label+"=") {
			args["filter"] = fmt.Sprintf("metric.type = starts_with(%q)", prefix)
			break
		}
	}
	var resp struct {
		MetricDescriptors []struct {
			Type      string `json:"type"`
			ValueType string `json:"valueType"`
		} `json:"metricDescriptors"`
	}
	if err := g.mcp.Call(ctx, "list_metric_descriptors", args, &resp); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(resp.MetricDescriptors))
	for _, d := range resp.MetricDescriptors {
		n := PromQLName(d.Type)
		if d.ValueType == "DISTRIBUTION" {
			n += "_bucket"
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// PromQLName maps a Cloud Monitoring metric type to its PromQL name:
// run.googleapis.com/request_count → run_googleapis_com:request_count, and
// prometheus.googleapis.com/<name>/<kind> → <name>.
func PromQLName(metricType string) string {
	domain, rest, ok := strings.Cut(metricType, "/")
	if !ok {
		return metricType
	}
	if domain == "prometheus.googleapis.com" {
		if i := strings.LastIndex(rest, "/"); i >= 0 {
			return rest[:i]
		}
		return rest
	}
	r := strings.NewReplacer(".", "_", "/", "_")
	return r.Replace(domain) + ":" + r.Replace(rest)
}

// gcpDiscovery lists, per product, a query whose series name the services.
var gcpDiscovery = []struct{ label, expr string }{
	{"service_name", `count by (service_name) (run_googleapis_com:container_instance_count)`},
	{"subscription_id", `count by (subscription_id) (pubsub_googleapis_com:subscription_num_undelivered_messages)`},
	{"queue_id", `count by (queue_id) (cloudtasks_googleapis_com:queue_depth)`},
}

// Services proposes one service per Cloud Run service, Pub/Sub subscription
// and Cloud Tasks queue with data in the last hour. Products without data
// are skipped.
func (g *GCPMonitoring) Services(ctx context.Context) ([]Service, error) {
	end := time.Now()
	var out []Service
	var errs []string
	for _, d := range gcpDiscovery {
		series, err := g.Range(ctx, d.expr, end.Add(-time.Hour), end)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		for _, s := range series {
			if v := s.Labels[d.label]; v != "" {
				out = append(out, Service{Name: v, Selector: fmt.Sprintf("%s=%q", d.label, v)})
			}
		}
	}
	if len(out) == 0 && len(errs) == len(gcpDiscovery) {
		return nil, fmt.Errorf("cloud monitoring: %s", strings.Join(errs, "; "))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (g *GCPMonitoring) Check(ctx context.Context) error {
	_, err := g.Range(ctx, "vector(1)", time.Now().Add(-5*time.Minute), time.Now())
	return err
}

// GCPPresets cover Cloud Run, Pub/Sub and Cloud Tasks. Each needs its
// product's resource label in the service's selectors (Requires).
var GCPPresets = []Preset{
	// Pub/Sub subscriptions.
	{
		Name: "pubsub_backlog_messages", Unit: "", Brief: true, Requires: "subscription_id",
		Description: "Messages not yet acknowledged by subscribers, per subscription. Growing means consumers are slow, failing or down.",
		Expr:        `max by (subscription_id) (pubsub_googleapis_com:subscription_num_undelivered_messages{$selector})`,
	},
	{
		Name: "pubsub_oldest_unacked_seconds", Unit: "s", Brief: true, Requires: "subscription_id",
		Description: "Age of the oldest unacknowledged message, per subscription. Rising steadily means messages are stuck.",
		Expr:        `max by (subscription_id) (pubsub_googleapis_com:subscription_oldest_unacked_message_age{$selector})`,
	},
	{
		Name: "pubsub_acks_per_second", Unit: "msg/s", Brief: true, Requires: "subscription_id",
		Description: "Messages acknowledged per second, per subscription: the consumers' throughput.",
		Expr:        `sum by (subscription_id) (rate(pubsub_googleapis_com:subscription_ack_message_count{$selector}[5m]))`,
	},
	{
		Name: "pubsub_sent_per_second", Unit: "msg/s", Requires: "subscription_id",
		Description: "Messages delivered to subscribers per second, per subscription (includes redeliveries).",
		Expr:        `sum by (subscription_id) (rate(pubsub_googleapis_com:subscription_sent_message_count{$selector}[5m]))`,
	},
	{
		Name: "pubsub_expired_ack_deadlines_per_second", Unit: "msg/s", Brief: true, Requires: "subscription_id",
		Description: "Messages whose ack deadline expired per second, per subscription: consumers took too long or crashed, so the messages are redelivered.",
		Expr:        `sum by (subscription_id) (rate(pubsub_googleapis_com:subscription_expired_ack_deadlines_count{$selector}[5m]))`,
	},
	{
		Name: "pubsub_dead_letter_per_second", Unit: "msg/s", Brief: true, Requires: "subscription_id",
		Description: "Messages forwarded to the dead-letter topic per second, per subscription: they failed every delivery attempt.",
		Expr:        `sum by (subscription_id) (rate(pubsub_googleapis_com:subscription_dead_letter_message_count{$selector}[5m]))`,
	},
	{
		Name: "pubsub_nacks", Unit: "", Requires: "subscription_id",
		Description: "Negative acknowledgements, per subscription: consumers rejecting messages.",
		Expr:        `max by (subscription_id) (pubsub_googleapis_com:subscription_nack_requests{$selector})`,
	},
	// Cloud Tasks queues.
	{
		Name: "tasks_queue_depth", Unit: "", Brief: true, Requires: "queue_id",
		Description: "Tasks waiting in the queue, per queue.",
		Expr:        `max by (queue_id) (cloudtasks_googleapis_com:queue_depth{$selector})`,
	},
	{
		Name: "tasks_attempts_per_second", Unit: "req/s", Brief: true, Requires: "queue_id",
		Description: "Task attempts per second, per queue and response code. Non-OK codes are failing targets.",
		Expr:        `sum by (queue_id, response_code) (rate(cloudtasks_googleapis_com:queue_task_attempt_count{$selector}[5m]))`,
	},
	{
		Name: "tasks_attempt_delay_p95_seconds", Unit: "s", Requires: "queue_id",
		Description: "95th percentile delay between a task's schedule time and its attempt, per queue.",
		Expr:        `histogram_quantile(0.95, sum by (le, queue_id) (rate(cloudtasks_googleapis_com:queue_task_attempt_delays_bucket{$selector}[5m]))) / 1000`,
	},
	// Cloud Run services.
	{
		Name: "http_requests_per_second", Unit: "req/s", Brief: true, Requires: "service_name",
		Description: "Requests served per second, per revision.",
		Expr:        `sum by (revision_name) (rate(run_googleapis_com:request_count{$selector}[5m]))`,
	},
	{
		Name: "http_5xx_per_second", Unit: "req/s", Brief: true, Requires: "service_name",
		Description: "5xx responses per second, per revision.",
		Expr:        `sum by (revision_name) (rate(run_googleapis_com:request_count{$selector, response_code_class="5xx"}[5m]))`,
	},
	{
		Name: "http_p95_seconds", Unit: "s", Brief: true, Requires: "service_name",
		Description: "95th percentile request latency, per revision.",
		Expr:        `histogram_quantile(0.95, sum by (le, revision_name) (rate(run_googleapis_com:request_latencies_bucket{$selector}[5m]))) / 1000`,
	},
	{
		Name: "instances", Unit: "", Brief: true, Requires: "service_name",
		Description: "Container instances, per revision and state (active or idle). Flat at max-instances means the service cannot scale further.",
		Expr:        `sum by (revision_name, state) (run_googleapis_com:container_instance_count{$selector})`,
	},
	{
		Name: "cpu_utilization_p95_percent", Unit: "%", Brief: true, Requires: "service_name",
		Description: "95th percentile CPU utilization of the instances, per revision.",
		Expr:        `100 * histogram_quantile(0.95, sum by (le, revision_name) (rate(run_googleapis_com:container_cpu_utilizations_bucket{$selector}[5m])))`,
	},
	{
		Name: "memory_utilization_p95_percent", Unit: "%", Brief: true, Requires: "service_name",
		Description: "95th percentile memory utilization of the instances, per revision. Near 100% leads to OOM kills.",
		Expr:        `100 * histogram_quantile(0.95, sum by (le, revision_name) (rate(run_googleapis_com:container_memory_utilizations_bucket{$selector}[5m])))`,
	},
	{
		Name: "startup_latency_p95_seconds", Unit: "s", Requires: "service_name",
		Description: "95th percentile container startup latency, per revision (cold starts).",
		Expr:        `histogram_quantile(0.95, sum by (le, revision_name) (rate(run_googleapis_com:container_startup_latencies_bucket{$selector}[5m]))) / 1000`,
	},
}

func asToolError(err error, target **mcpconn.ToolError) bool { return errors.As(err, target) }
