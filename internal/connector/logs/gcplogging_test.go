package logs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn/mcptest"
)

func TestGCPLoggingQuery(t *testing.T) {
	page := 0
	srv := mcptest.New(t, map[string]mcptest.Handler{
		"list_log_entries": func(args map[string]any) (any, error) {
			page++
			if page == 1 {
				return map[string]any{
					"entries": []map[string]any{
						{"timestamp": "2026-11-03T09:40:22Z", "severity": "ERROR", "resource": map[string]any{"type": "cloud_run_revision", "labels": map[string]string{"service_name": "api", "project_id": "p"}},
							"jsonPayload": map[string]any{"message": "db timeout", "level": "error"}},
						{"timestamp": "2026-11-03T09:40:21Z", "severity": "INFO", "resource": map[string]any{"type": "cloud_scheduler_job", "labels": map[string]string{"job_id": "1m"}},
							"jsonPayload": map[string]any{"@type": "type.googleapis.com/google.cloud.scheduler.logging.AttemptFinished", "debugInfo": "URL_CRAWLED. code = 200", "jobName": "projects/p/jobs/1m"}},
					},
					"nextPageToken": "next",
				}, nil
			}
			return map[string]any{"entries": []map[string]any{
				{"timestamp": "2026-11-03T09:40:20Z", "severity": "WARNING", "textPayload": "slow query", "resource": map[string]any{"type": "cloud_run_revision"}},
			}}, nil
		},
		"list_log_names": func(map[string]any) (any, error) { return map[string]any{"logNames": []string{"x"}}, nil },
	}, false)
	src := NewGCPLogging("p", srv.Client(t, GCPLoggingTools...))

	end := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	lines, err := src.Query(context.Background(), Query{
		Selector: `resource.type="cloud_run_revision"`, Filter: ErrorFilter, Start: end.Add(-time.Hour), End: end, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"level=error db timeout",
		"level=info AttemptFinished debugInfo=URL_CRAWLED. code = 200",
		"level=warning slow query",
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %+v", len(lines), len(want), lines)
	}
	for i, w := range want {
		if lines[i].Text != w {
			t.Errorf("line %d = %q, want %q", i, lines[i].Text, w)
		}
	}
	if lines[0].Labels["service_name"] != "api" || lines[0].Labels["project_id"] != "" {
		t.Errorf("labels = %v", lines[0].Labels)
	}

	calls := srv.Calls()
	if len(calls) != 2 || calls[1].Args["pageToken"] != "next" {
		t.Fatalf("calls = %+v, want two pages", calls)
	}
	filter, _ := calls[0].Args["filter"].(string)
	for _, part := range []string{
		`(resource.type="cloud_run_revision")`,
		`timestamp>="2026-11-03T09:00:00Z"`,
		`severity>=ERROR`, `httpRequest.status>=500`, `textPayload=~"(?i)(\\berror\\b`,
	} {
		if !strings.Contains(filter, part) {
			t.Errorf("filter %s\n does not contain %s", filter, part)
		}
	}
	if rn, _ := calls[0].Args["resourceNames"].([]any); len(rn) != 1 || rn[0] != "projects/p" {
		t.Errorf("resourceNames = %v", calls[0].Args["resourceNames"])
	}

	if err := src.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestGCPLoggingServices(t *testing.T) {
	srv := mcptest.New(t, map[string]mcptest.Handler{
		"list_log_entries": func(map[string]any) (any, error) {
			return map[string]any{"entries": []map[string]any{
				{"resource": map[string]any{"type": "cloud_scheduler_job", "labels": map[string]string{"job_id": "1m-cron"}}},
				{"resource": map[string]any{"type": "cloud_scheduler_job", "labels": map[string]string{"job_id": "1m-cron"}}},
				{"resource": map[string]any{"type": "cloud_run_revision", "labels": map[string]string{"service_name": "api"}}},
				{"resource": map[string]any{"type": "audited_resource", "labels": map[string]string{"service": "x"}}},
			}}, nil
		},
		"list_log_names": func(map[string]any) (any, error) { return nil, nil },
	}, true)
	src := NewGCPLogging("p", srv.Client(t, GCPLoggingTools...))
	got, err := src.Services(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Service{
		{Name: "1m-cron", Selector: `resource.type="cloud_scheduler_job" AND resource.labels.job_id="1m-cron"`},
		{Name: "api", Selector: `resource.type="cloud_run_revision" AND resource.labels.service_name="api"`},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("service %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
