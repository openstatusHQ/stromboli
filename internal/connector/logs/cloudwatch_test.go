package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCloudWatch speaks the CloudWatch Logs JSON 1.1 protocol. Each query is
// reported "Running" once, then "Complete" with the rows from answer.
type fakeCloudWatch struct {
	mu      sync.Mutex
	started []map[string]any
	polled  map[string]int
	answer  func(query string) string // JSON array of result rows
}

func (f *fakeCloudWatch) server(t *testing.T) *httptest.Server {
	f.polled = map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIATEST/") {
			t.Errorf("request not signed with the static keys: %q", r.Header.Get("Authorization"))
		}
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Header.Get("X-Amz-Target") {
		case "Logs_20140328.StartQuery":
			f.started = append(f.started, in)
			fmt.Fprintf(w, `{"queryId":"q%d"}`, len(f.started)-1)
		case "Logs_20140328.GetQueryResults":
			id := in["queryId"].(string)
			if f.polled[id]++; f.polled[id] == 1 {
				fmt.Fprint(w, `{"status":"Running","results":[]}`)
				return
			}
			var idx int
			fmt.Sscanf(id, "q%d", &idx)
			fmt.Fprintf(w, `{"status":"Complete","results":%s}`, f.answer(f.started[idx]["queryString"].(string)))
		case "Logs_20140328.DescribeLogGroups":
			if in["nextToken"] == nil {
				fmt.Fprint(w, `{"logGroups":[{"logGroupName":"/aws/lambda/checkout"},{"logGroupName":"/ecs/prod/api"}],"nextToken":"p2"}`)
			} else {
				fmt.Fprint(w, `{"logGroups":[{"logGroupName":"/ecs/staging/api"},{"logGroupName":"worker"}]}`)
			}
		default:
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"__type":"UnknownOperationException","message":"%s"}`, r.Header.Get("X-Amz-Target"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestCloudWatch(t *testing.T, f *fakeCloudWatch) *CloudWatch {
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
	cw, err := NewCloudWatch(context.Background(), CloudWatchConfig{
		Region: "eu-west-1", AccessKeyID: "AKIATEST", SecretAccessKey: "secret", Endpoint: f.server(t).URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	cw.poll = time.Millisecond
	return cw
}

func field(name, value string) string { return fmt.Sprintf(`{"field":%q,"value":%q}`, name, value) }

func TestCloudWatchQueryAndCount(t *testing.T) {
	f := &fakeCloudWatch{answer: func(q string) string {
		if strings.Contains(q, "stats count(*)") {
			return "[[" + field("n", "17") + "]]"
		}
		return "[[" + strings.Join([]string{
			field("@timestamp", "2026-11-02 10:00:00.000"), field("@message", "older"),
			field("@logStream", "s1"), field("@log", "123456789012:/ecs/prod"),
		}, ",") + "],[" + strings.Join([]string{
			field("@timestamp", "2026-11-02 10:01:00.500"), field("@message", "newer"),
		}, ",") + "]]"
	}}
	cw := newTestCloudWatch(t, f)
	ctx := context.Background()
	end := time.Date(2026, 11, 2, 10, 5, 0, 0, time.UTC)
	q := Query{Selector: `/ecs/prod, /ecs/prod-worker | filter service = "api"`, Filter: `(?i)a/b`, Start: end.Add(-time.Hour), End: end, Limit: 10}

	lines, err := cw.Query(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Text != "newer" || lines[1].Text != "older" {
		t.Fatalf("lines = %+v", lines)
	}
	if !lines[0].Time.Equal(time.Date(2026, 11, 2, 10, 1, 0, 5e8, time.UTC)) {
		t.Fatalf("time = %v", lines[0].Time)
	}
	if lines[1].Labels["log_group"] != "/ecs/prod" || lines[1].Labels["log_stream"] != "s1" {
		t.Fatalf("labels = %+v", lines[1].Labels)
	}
	n, err := cw.Count(ctx, q)
	if err != nil || n != 17 {
		t.Fatalf("count = %d, %v", n, err)
	}

	first := f.started[0]
	wantQuery := `fields @timestamp, @message, @logStream, @log | filter service = "api" | filter @message like /(?i)a\/b/ | sort @timestamp desc | limit 10`
	if first["queryString"] != wantQuery {
		t.Fatalf("query = %q\nwant    %q", first["queryString"], wantQuery)
	}
	if fmt.Sprint(first["logGroupIdentifiers"]) != "[/ecs/prod /ecs/prod-worker]" {
		t.Fatalf("groups = %v", first["logGroupIdentifiers"])
	}
	if first["startTime"].(float64) != float64(end.Add(-time.Hour).Unix()) || first["endTime"].(float64) != float64(end.Unix()) {
		t.Fatalf("window = %v..%v", first["startTime"], first["endTime"])
	}
	if got := f.started[1]["queryString"]; got != `filter service = "api" | filter @message like /(?i)a\/b/ | stats count(*) as n` {
		t.Fatalf("count query = %q", got)
	}
}

func TestCloudWatchServices(t *testing.T) {
	cw := newTestCloudWatch(t, &fakeCloudWatch{})
	got, err := cw.Services(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Service{
		{Name: "checkout", Selector: "/aws/lambda/checkout"},
		{Name: "ecs-prod-api", Selector: "/ecs/prod/api"},
		{Name: "ecs-staging-api", Selector: "/ecs/staging/api"},
		{Name: "worker", Selector: "worker"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("services = %v\nwant       %v", got, want)
	}
}

func TestCloudWatchSelector(t *testing.T) {
	if _, _, err := parseCloudWatchSelector(" | filter x = 1"); err == nil {
		t.Fatal("expected error for selector without log group")
	}
	groups, cmds, err := parseCloudWatchSelector("/a")
	if err != nil || fmt.Sprint(groups) != "[/a]" || cmds != "" {
		t.Fatalf("got %v %q %v", groups, cmds, err)
	}
}
