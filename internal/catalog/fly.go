package catalog

import (
	"regexp"
	"slices"
	"strings"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
)

// MachineActions are the Fly machine actions stromboli can run once approved.
var MachineActions = []string{"restart", "clone", "cordon", "uncordon", "stop", "start"}

// DefaultMachineActions are allowed when a service's fly.allow is empty. Stop
// and start are opt-in: stopping a machine removes capacity.
var DefaultMachineActions = []string{"restart", "clone", "cordon", "uncordon"}

// IsFly reports whether the service runs on Fly.io machines.
func IsFly(s *stromboliv1.Service) bool {
	if s.Fly != nil {
		return true
	}
	switch strings.ToLower(s.Runtime) {
	case "fly", "fly.io", "flyio":
		return true
	}
	return false
}

var appMatcher = regexp.MustCompile(`^\s*app\s*=\s*"([^"]+)"\s*$`)

// FlyApp is the service's Fly app: fly.app, else the app="..." metrics
// selector, else the service name.
func FlyApp(s *stromboliv1.Service) string {
	if s.Fly != nil && s.Fly.App != "" {
		return s.Fly.App
	}
	if s.Metrics != nil {
		for _, sel := range s.Metrics.Selectors {
			for _, part := range strings.Split(sel, ",") {
				if m := appMatcher.FindStringSubmatch(part); m != nil {
					return m[1]
				}
			}
		}
	}
	return s.Name
}

// AllowedMachineActions is the service's fly.allow, or the defaults.
func AllowedMachineActions(s *stromboliv1.Service) []string {
	if s.Fly != nil && len(s.Fly.Allow) > 0 {
		return slices.Clone(s.Fly.Allow)
	}
	return slices.Clone(DefaultMachineActions)
}
