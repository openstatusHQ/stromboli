package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openstatushq/opensre/internal/connector/fly"
	"github.com/openstatushq/opensre/internal/connector/fly/flytest"
)

func TestRenderFly(t *testing.T) {
	srv := flytest.New("checker")
	defer srv.Close()
	c, _ := fly.NewMachines("acme", flytest.Token, srv.URL)
	machines, err := c.List(context.Background(), "checker")
	if err != nil {
		t.Fatal(err)
	}

	out := renderFly("checker", machines, nil, flyExec{allowed: []string{"restart", "cordon"}})
	for _, want := range []string{
		"App `checker`. 3 machine(s)",
		"| 148e | ams | started | app | shared-cpu-1x, 512MB | registry.fly.io/api:deployment-01 |",
		"| d91v (volume) | fra |",
		"do not clone them",
		"`fly machine restart <id> -a checker`",
		"`fly machine clone <id> -a checker --region <region>` · undo: fly machine destroy <new-id> -a checker --force",
		"`fly machine cordon <id> -a checker` · undo: fly machine uncordon <id> -a checker",
		"`fly apps restart checker`",
		"opensre can run these machine actions for this service after a human approves them: restart, cordon.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// The table is sorted by region, then ID.
	if strings.Index(out, "| 148e") > strings.Index(out, "| 2a7c") || strings.Index(out, "| 2a7c") > strings.Index(out, "| d91v") {
		t.Errorf("machines should be sorted by region and ID:\n%s", out)
	}

	out = renderFly("checker", nil, errors.New("401 unauthorized"), flyExec{reason: "machine actions are disabled"})
	if !strings.Contains(out, "Machines unavailable: 401 unauthorized") ||
		!strings.Contains(out, "cannot run machine actions for this service (machine actions are disabled): leave machine_action unset") ||
		!strings.Contains(out, "fly machine restart <id> -a checker") {
		t.Errorf("unexpected section without machines:\n%s", out)
	}
}
