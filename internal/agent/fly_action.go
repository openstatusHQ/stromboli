package agent

import (
	"regexp"
	"slices"
	"strings"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
	"github.com/openstatushq/stromboli/internal/catalog"
)

// machineActionFromCommands reads the Fly machine action out of a
// mitigation's commands, for when the model wrote `fly machine start <id>`
// but left machine_action unset. It returns nil unless every fly machine
// command uses the same verb and names at least one machine: anything else
// (fly deploy, fly scale, mixed verbs) stays a suggestion for humans.
// The result is validated like any proposed action (allow-list, machines).
func machineActionFromCommands(commands string) *stromboliv1.MachineAction {
	var out *stromboliv1.MachineAction
	for _, cmd := range commandSep.Split(commands, -1) {
		f := strings.Fields(strings.NewReplacer("`", " ", `"`, " ", "'", " ").Replace(cmd))
		i := slices.Index(f, "fly")
		if i < 0 {
			i = slices.Index(f, "flyctl")
		}
		if i < 0 || len(f) < i+3 || !slices.Contains([]string{"machine", "machines", "m"}, f[i+1]) {
			continue
		}
		verb := f[i+2]
		if !slices.Contains(catalog.MachineActions, verb) {
			if slices.Contains(machineSubcommands, verb) {
				return nil // a fly machine command we cannot run: leave it to humans
			}
			continue
		}
		ids, region := machineArgs(f[i+3:])
		if len(ids) == 0 {
			return nil // e.g. a placeholder <id>: nothing to run
		}
		if out == nil {
			out = &stromboliv1.MachineAction{Action: verb, Region: region}
		} else if out.Action != verb || out.Region != region {
			return nil
		}
		for _, id := range ids {
			if !slices.Contains(out.MachineIds, id) {
				out.MachineIds = append(out.MachineIds, id)
			}
		}
	}
	return out
}

// commandSep splits a mitigation's commands into single commands.
var commandSep = regexp.MustCompile(`\n|;|&&|\|\|`)

// machineID matches a Fly machine ID (14 hex digits today), not placeholders
// like <id>; Propose checks it is one of the app's machines.
var machineID = regexp.MustCompile(`^[0-9a-z]{4,24}$`)

// Other fly machine subcommands: their presence means the mitigation is not
// one stromboli can run.
var machineSubcommands = []string{"destroy", "remove", "rm", "kill", "update", "run", "exec", "suspend", "leases"}

// machineArgs returns the machine IDs and --region of a fly machine command's
// arguments, skipping the other flags and their values.
func machineArgs(args []string) (ids []string, region string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			name, val, inline := strings.Cut(a, "=")
			if !slices.Contains(valueFlags, name) {
				continue // boolean flag
			}
			if !inline && i+1 < len(args) {
				i++
				val = args[i]
			}
			if name == "--region" || name == "-r" {
				region = val
			}
			continue
		}
		if machineID.MatchString(a) {
			ids = append(ids, a)
		}
	}
	return ids, region
}

var valueFlags = []string{"-a", "--app", "-r", "--region", "-c", "--config", "-s", "--signal", "--timeout", "--wait-timeout", "--time", "--org", "-o"}
