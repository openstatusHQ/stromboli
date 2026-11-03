package catalog

import (
	"testing"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
)

func TestFlyApp(t *testing.T) {
	for _, c := range []struct {
		svc   *opensrev1.Service
		app   string
		isFly bool
	}{
		{&opensrev1.Service{Name: "checker", Runtime: "fly.io"}, "checker", true},
		{&opensrev1.Service{Name: "checker", Metrics: &opensrev1.MetricsConfig{Selectors: []string{`region="ams", app="openstatus-checker"`}}}, "openstatus-checker", false},
		{&opensrev1.Service{Name: "checker", Fly: &opensrev1.FlyConfig{App: "chk"}, Metrics: &opensrev1.MetricsConfig{Selectors: []string{`app="other"`}}}, "chk", true},
		{&opensrev1.Service{Name: "api", Runtime: "kubernetes"}, "api", false},
	} {
		if got := FlyApp(c.svc); got != c.app {
			t.Errorf("FlyApp(%v) = %q, want %q", c.svc, got, c.app)
		}
		if got := IsFly(c.svc); got != c.isFly {
			t.Errorf("IsFly(%v) = %v", c.svc, got)
		}
	}
	if got := AllowedMachineActions(&opensrev1.Service{}); len(got) != 4 || got[0] != "restart" {
		t.Errorf("default allow-list = %v", got)
	}
}

func TestValidateFly(t *testing.T) {
	svc := &opensrev1.Service{Name: "api", Owners: []string{"@x"}, Fly: &opensrev1.FlyConfig{Actions: "fly-metrics", Allow: []string{"restart", "destroy"}}}
	issues := Validate([]*opensrev1.Service{svc}, ValidateOptions{Connectors: map[string]string{"fly-metrics": "fly"}})
	var allow, actions bool
	for _, i := range issues {
		allow = allow || (i.Field == "fly.allow" && !i.Warning)
		actions = actions || (i.Field == "fly.actions" && !i.Warning)
	}
	if !allow || !actions {
		t.Errorf("expected errors for an unknown action and a non fly_machines connector, got %v", issues)
	}
}
