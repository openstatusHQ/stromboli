package agent

import (
	"slices"
	"testing"
)

func TestMachineActionFromCommands(t *testing.T) {
	for _, c := range []struct {
		commands, action, region string
		ids                      []string
	}{
		// The case seen in production: two starts, one per line.
		{"fly machine start 683e46ea064408 -a statuscode-faithful-pebble-9940\nfly machine start 286d732b361238 -a statuscode-faithful-pebble-9940",
			"start", "", []string{"683e46ea064408", "286d732b361238"}},
		{"`fly machine restart 148e -a acme-api`", "restart", "", []string{"148e"}},
		{"fly m restart -a acme-api 148e && fly machine restart 2a7c --app acme-api", "restart", "", []string{"148e", "2a7c"}},
		{"flyctl machines cordon 148e -a acme-api", "cordon", "", []string{"148e"}},
		{"fly machine clone 148e -a acme-api --region fra", "clone", "fra", []string{"148e"}},
		{"fly machine clone 148e -a acme-api --region=fra", "clone", "fra", []string{"148e"}},

		// Not something opensre can run: stays a suggestion.
		{"fly machine restart <id> -a acme-api", "", "", nil},
		{"fly machine start 148e -a acme-api\nfly machine restart 2a7c -a acme-api", "", "", nil},
		{"fly machine stop 148e -a acme-api; fly machine destroy 2a7c -a acme-api --force", "", "", nil},
		{"fly deploy -a acme-api --image registry.fly.io/acme-api:v41", "", "", nil},
		{"fly scale memory 512 -a acme-api", "", "", nil},
		{"fly apps restart acme-api", "", "", nil},
		{"kubectl rollout restart deploy/api", "", "", nil},
	} {
		got := machineActionFromCommands(c.commands)
		if c.action == "" {
			if got != nil {
				t.Errorf("%q: want no machine action, got %v", c.commands, got)
			}
			continue
		}
		if got == nil || got.Action != c.action || got.Region != c.region || !slices.Equal(got.MachineIds, c.ids) {
			t.Errorf("%q: got %v, want %s %v region %q", c.commands, got, c.action, c.ids, c.region)
		}
	}
}
