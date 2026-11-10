package trigger

import (
	"slices"
	"testing"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
	"github.com/openstatushq/stromboli/internal/catalog"
)

var res = resolver{graph: catalog.NewGraph([]*stromboliv1.Service{
	{Name: "api", Monitors: []string{"openstatus:1"}},
	{Name: "checker"},
})}

func TestParseOpenstatus(t *testing.T) {
	alerts, err := parseOpenstatus([]byte(`{
		"monitor": {"id": 1, "name": "API", "url": "https://api.example.com"},
		"cronTimestamp": 1744023705307,
		"status": "error",
		"statusCode": 500,
		"errorMessage": "Internal Server Error"
	}`), res)
	if err != nil {
		t.Fatal(err)
	}
	a := alerts[0]
	if !slices.Equal(a.Services, []string{"api"}) || a.Fingerprint != "openstatus:1" || a.Resolved {
		t.Errorf("unexpected alert: %+v", a)
	}
	if a.At.UnixMilli() != 1744023705307 {
		t.Errorf("At = %v", a.At)
	}

	alerts, _ = parseOpenstatus([]byte(`{"monitor": {"id": 1, "name": "API", "url": "x"}, "cronTimestamp": 1, "status": "recovered"}`), res)
	if !alerts[0].Resolved {
		t.Error("recovered should be resolved")
	}

	alerts, _ = parseOpenstatus([]byte(`{"monitor": {"id": 99, "name": "other", "url": "x"}, "cronTimestamp": 1, "status": "error"}`), res)
	if len(alerts[0].Services) != 0 {
		t.Error("unknown monitor should not map to a service")
	}
}

func TestParseAlertmanager(t *testing.T) {
	alerts, err := parseAlertmanager([]byte(`{
		"alerts": [
			{"status": "firing", "labels": {"alertname": "HighErrorRate", "app": "checker"},
			 "annotations": {"summary": "5xx above 5%"}, "startsAt": "2026-11-02T10:00:00Z", "fingerprint": "abc"},
			{"status": "resolved", "labels": {"alertname": "HighLatency", "service": "api"}, "startsAt": "2026-11-02T09:00:00Z"}
		]
	}`), res)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 2 {
		t.Fatalf("got %d alerts", len(alerts))
	}
	if !slices.Equal(alerts[0].Services, []string{"checker"}) || alerts[0].Title != "HighErrorRate: 5xx above 5%" || alerts[0].Fingerprint != "alertmanager:abc" {
		t.Errorf("unexpected first alert: %+v", alerts[0])
	}
	if !alerts[1].Resolved || !slices.Equal(alerts[1].Services, []string{"api"}) {
		t.Errorf("unexpected second alert: %+v", alerts[1])
	}
}

func TestParseGenericRequiresFields(t *testing.T) {
	if _, err := parseGeneric([]byte(`{"title": "x"}`), res); err == nil {
		t.Error("expected an error without service")
	}
}
