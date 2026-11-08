package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/catalog"
	"github.com/openstatushq/opensre/internal/connector/fly"
)

// maxMachinesInBrief keeps the machine table short on large apps.
const maxMachinesInBrief = 20

// flyExec says whether opensre can run machine actions for a service.
type flyExec struct {
	allowed []string
	reason  string // why not, when allowed is empty
}

// briefFly is the brief section for a service on Fly.io: its machines (IDs
// and regions to put in commands) and the Fly command for each mitigation.
func (e *Engine) briefFly(ctx context.Context, svc *opensrev1.Service) string {
	app := catalog.FlyApp(svc)
	machines, listErr := e.flyMachines(ctx, svc, app)
	ex := flyExec{reason: "machine actions are not configured"}
	if e.actions != nil {
		if _, allowed, err := e.actions.Executable(svc); err != nil {
			ex.reason = err.Error()
		} else {
			ex.allowed = allowed
		}
	}
	return renderFly(app, machines, listErr, ex)
}

// flyMachines lists the app's machines with the fly.actions connector, or
// else the fly metrics connector's token.
func (e *Engine) flyMachines(ctx context.Context, svc *opensrev1.Service, app string) ([]fly.Machine, error) {
	conn := ""
	if svc.Fly != nil && svc.Fly.Actions != "" {
		conn = svc.Fly.Actions
	} else if svc.Metrics != nil && svc.Metrics.Source != "" {
		if c, err := e.store.GetConnector(ctx, svc.Metrics.Source); err == nil && c.Kind == "fly" {
			conn = c.Name
		}
	}
	if conn == "" {
		return nil, fmt.Errorf("no fly or fly_machines connector for %s: set fly.actions, or a fly metrics source", svc.Name)
	}
	client, _, err := e.connectors.FlyMachines(ctx, conn)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return client.List(ctx, app)
}

func renderFly(app string, machines []fly.Machine, listErr error, ex flyExec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "App `%s`.", app)
	switch {
	case listErr != nil:
		fmt.Fprintf(&b, " _Machines unavailable: %v._ Machine IDs are the `instance` label of the Fly metrics.\n\n", listErr)
	case len(machines) == 0:
		b.WriteString(" _No machines._\n\n")
	default:
		sort.Slice(machines, func(i, j int) bool {
			if machines[i].Region != machines[j].Region {
				return machines[i].Region < machines[j].Region
			}
			return machines[i].ID < machines[j].ID
		})
		volumes := false
		fmt.Fprintf(&b, " %d machine(s); the `instance` label of the Fly metrics is the machine ID.\n\n", len(machines))
		b.WriteString("| machine | region | state | process | size | image | updated |\n|---|---|---|---|---|---|---|\n")
		for i, m := range machines {
			if i == maxMachinesInBrief {
				fmt.Fprintf(&b, "| … %d more | | | | | | |\n", len(machines)-i)
				break
			}
			id := m.ID
			if m.HasVolumes() {
				id += " (volume)"
				volumes = true
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s |\n", id, m.Region, m.State, dash(m.ProcessGroup()),
				dash(m.Size()), dash(m.ImageRef.Image()), m.UpdatedAt.UTC().Format(time.RFC3339))
		}
		if volumes {
			b.WriteString("\nVolumes (the `id` label of volume_used_percent is the volume ID):\n")
			for i, m := range machines {
				if i == maxMachinesInBrief {
					break
				}
				for _, v := range m.Volumes() {
					size := ""
					if v.SizeGB > 0 {
						size = fmt.Sprintf(", %d GB", v.SizeGB)
					}
					fmt.Fprintf(&b, "- %s on machine %s (%s) at %s%s\n", v.Volume, m.ID, m.Region, dash(v.Path), size)
				}
			}
			b.WriteString("Machines with a volume hold state: do not clone them (the clone gets an empty volume); restart or scale count instead. " +
				"A restart does not free space on a volume: when one is full, extend it or delete data.\n")
		}
		b.WriteString("\n")
	}

	a := "-a " + app
	b.WriteString("Fly commands per mitigation (put in the machine IDs and regions above):\n")
	for _, c := range [][3]string{
		{"restart one machine (stuck, leaking, OOM)", "fly machine restart <id> " + a, "none needed: same machine, image and config"},
		{"restart every machine (rolling)", "fly apps restart " + app, "none needed"},
		{"scale_up: one more machine like an existing one", "fly machine clone <id> " + a + " --region <region>", "fly machine destroy <new-id> " + a + " --force"},
		{"scale_up: more machines", "fly scale count <n> " + a + " [--region <region>] [--process-group <group>]", "fly scale count <previous n> " + a},
		{"scale_up: more memory (OOM kills, memory at the limit)", "fly scale memory <MB> " + a, "fly scale memory <previous MB> " + a},
		{"drain one bad machine, keep it running for debugging", "fly machine cordon <id> " + a, "fly machine uncordon <id> " + a},
		{"drain by stopping it", "fly machine stop <id> " + a, "fly machine start <id> " + a},
		{"scale_up: more disk when a volume is full (volume_used_percent near 100, \"no space left on device\")", "fly volumes extend <vol-id> -s <new size in GB> " + a, "none: a volume cannot shrink; check the size with `fly volumes list " + a + "`, and restart the machine if `df` does not show the new size"},
		{"free space on a full volume or root filesystem: open a shell, then delete or rotate data (logs, temp files, old backups)", "fly ssh console " + a + " --machine <id>", "none: deleted data is gone, so prefer extending the volume first"},
		{"rollback a bad deploy", "fly releases " + a + " --image` to find the previous image, then `fly deploy " + a + " --image <previous image>", "fly deploy " + a + " --image <bad image>"},
	} {
		fmt.Fprintf(&b, "- %s: `%s` · undo: %s\n", c[0], c[1], c[2])
	}
	b.WriteString("Verify with `fly status " + a + "` and the metrics of the machine.\n\n")

	if len(ex.allowed) > 0 {
		fmt.Fprintf(&b, "opensre can run these machine actions for this service after a human approves them: %s. "+
			"To offer one, set machine_action in submit_mitigation with the machine IDs from the table; the command above still goes in action for humans.\n",
			strings.Join(ex.allowed, ", "))
	} else {
		fmt.Fprintf(&b, "opensre cannot run machine actions for this service (%s): leave machine_action unset, the commands are for humans.\n", ex.reason)
	}
	return b.String()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
