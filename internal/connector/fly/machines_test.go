package fly_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openstatushq/opensre/internal/connector/fly"
	"github.com/openstatushq/opensre/internal/connector/fly/flytest"
)

func TestAuthorization(t *testing.T) {
	for in, want := range map[string]string{
		"fm2_abc":          "FlyV1 fm2_abc",
		"FlyV1 fm2_abc":    "FlyV1 fm2_abc",
		" personal-token ": "Bearer personal-token",
		"Bearer x":         "Bearer x",
	} {
		if got := fly.Authorization(in); got != want {
			t.Errorf("Authorization(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMachines(t *testing.T) {
	srv := flytest.New("api")
	defer srv.Close()
	ctx := context.Background()
	c, err := fly.NewMachines("acme", flytest.Token, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Check(ctx); err != nil {
		t.Fatalf("check: %v", err)
	}

	ms, err := c.List(ctx, "api")
	if err != nil || len(ms) != 3 {
		t.Fatalf("list = %d machines, %v", len(ms), err)
	}
	m := ms[0]
	if m.ID != "148e" || m.Region != "ams" || m.ProcessGroup() != "app" || m.Size() != "shared-cpu-1x, 512MB" ||
		m.ImageRef.Image() != "registry.fly.io/api:deployment-01" || m.HasVolumes() || !ms[2].HasVolumes() {
		t.Errorf("unexpected machine: %+v (size %q)", m, m.Size())
	}

	if err := c.Restart(ctx, "api", "148e"); err != nil {
		t.Fatal(err)
	}
	if err := c.WaitStarted(ctx, "api", "148e", time.Minute); err != nil {
		t.Fatal(err)
	}

	// A clone copies the source config exactly, in the requested region.
	nm, err := c.Clone(ctx, "api", "148e", "syd")
	if err != nil {
		t.Fatal(err)
	}
	if nm.ID != "new1" || nm.Region != "syd" || len(srv.Created) != 1 {
		t.Fatalf("unexpected clone %+v", nm)
	}
	cfg := srv.Created[0]["config"].(map[string]any)
	if cfg["image"] != "registry.fly.io/api:deployment-01" || cfg["env"].(map[string]any)["PORT"] != "8080" {
		t.Errorf("clone should copy the source config, got %v", cfg)
	}
	// Defaults to the source's region.
	if nm, err := c.Clone(ctx, "api", "2a7c", ""); err != nil || nm.Region != "ams" {
		t.Errorf("clone without region = %+v, %v", nm, err)
	}
	// Never clone a machine with a volume.
	if _, err := c.Clone(ctx, "api", "d91v", ""); !errors.Is(err, fly.ErrHasVolumes) {
		t.Errorf("clone of a machine with a volume: %v", err)
	}

	want := []string{"POST 148e/restart", "POST create", "POST create"}
	if got := srv.Calls(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want %v", got, want)
	}

	// API errors carry Fly's message.
	if err := c.Restart(ctx, "api", "nope"); err == nil || !strings.Contains(err.Error(), "machine not found") {
		t.Errorf("restart of unknown machine: %v", err)
	}
	bad, _ := fly.NewMachines("acme", "wrong", srv.URL)
	if _, err := bad.List(ctx, "api"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("bad token: %v", err)
	}
}
