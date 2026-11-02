package logs

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Events as written by Fly's log shipper (flyio/log-shipper).
const (
	flyEvent     = `{"event":{"provider":"app"},"fly":{"app":{"instance":"148e272b123489","name":"checker"},"region":"ams"},"host":"a1b2","log":{"level":"error"},"message":"dial tcp 10.0.0.1:443: i/o timeout","timestamp":"2026-11-02T10:00:00.123Z"}`
	flyLokiEvent = `{"fly":{"app":{"name":"checker"},"region":"syd"},"host":"c3d4","level":"info","message":"request done"}`
)

func TestMessageText(t *testing.T) {
	for in, want := range map[string]string{
		flyEvent:                                "level=error dial tcp 10.0.0.1:443: i/o timeout",
		flyLokiEvent:                            "level=info request done",
		`{"level":"error","msg":"error: boom"}`: "error: boom", // level already in the message
		`level=error msg="plain logfmt"`:        `level=error msg="plain logfmt"`,
		`{"no":"message field"}`:                `{"no":"message field"}`,
		`{not json`:                             `{not json`,
	} {
		if got := messageText(in); got != want {
			t.Errorf("messageText(%s) = %q, want %q", in, got, want)
		}
	}
	// Same call site in two regions/instances groups into one pattern.
	other := strings.NewReplacer("ams", "syd", "148e272b123489", "9f8e7d6c5b4a39", "a1b2", "zz99").Replace(flyEvent)
	if Template(messageText(flyEvent)) != Template(messageText(other)) {
		t.Error("fly events from different regions should share a template")
	}
}

func TestLokiDiscoversFlyApps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/loki/api/v1/labels":
			fmt.Fprint(w, `{"data":["event_provider","fly_app_instance","fly_app_name","fly_region","host","level"]}`)
		case "/loki/api/v1/label/fly_app_name/values":
			fmt.Fprint(w, `{"data":["checker","web"]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	l, _ := NewLoki(LokiConfig{URL: srv.URL})
	got, err := l.Services(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Service{{"checker", `{fly_app_name="checker"}`}, {"web", `{fly_app_name="web"}`}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("services = %v", got)
	}
}

func TestCloudWatchDiscoversFlyApps(t *testing.T) {
	f := &fakeCloudWatch{answer: func(q string) string {
		if q != `filter ispresent(fly.app.name) | stats count(*) as n by @log, fly.app.name` {
			t.Errorf("unexpected query %q", q)
		}
		row := func(group, app string) string {
			return "[" + field("@log", "123456789012:"+group) + "," + field("fly.app.name", app) + "," + field("n", "10") + "]"
		}
		return "[" + row("/ecs/prod/api", "web") + "," + row("/ecs/prod/api", "checker") + "," + row("/ecs/staging/api", "checker") + "]"
	}}
	cw := newTestCloudWatch(t, f)
	cw.serviceField = "fly.app.name"
	got, err := cw.Services(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Service{
		{"checkout", "/aws/lambda/checkout"},
		{"checker", `/ecs/prod/api | filter fly.app.name = "checker"`},
		{"web", `/ecs/prod/api | filter fly.app.name = "web"`},
		{"checker-ecs-staging-api", `/ecs/staging/api | filter fly.app.name = "checker"`},
		{"worker", "worker"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("services =\n%v\nwant\n%v", got, want)
	}
}

func TestCloudWatchRejectsBadServiceField(t *testing.T) {
	if _, err := NewCloudWatch(context.Background(), CloudWatchConfig{Region: "eu-west-1", ServiceField: "x | delete"}); err == nil {
		t.Fatal("expected an error")
	}
}
