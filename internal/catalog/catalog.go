// Package catalog holds the service catalog: YAML encoding, validation and
// the dependency graph.
package catalog

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"

	"google.golang.org/protobuf/encoding/protojson"
	yamlv3 "gopkg.in/yaml.v3"
	"sigs.k8s.io/yaml"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/connector/logs"
)

var (
	jsonOut = protojson.MarshalOptions{UseProtoNames: true}
	jsonIn  = protojson.UnmarshalOptions{DiscardUnknown: false}
)

// MarshalSpec encodes a service for storage.
func MarshalSpec(s *opensrev1.Service) ([]byte, error) { return jsonOut.Marshal(s) }

// UnmarshalSpec decodes a stored service.
func UnmarshalSpec(b []byte) (*opensrev1.Service, error) {
	s := &opensrev1.Service{}
	if err := jsonIn.Unmarshal(b, s); err != nil {
		return nil, err
	}
	return s, nil
}

// ParseYAML decodes a YAML list of services.
func ParseYAML(b []byte) ([]*opensrev1.Service, error) {
	raw, err := yaml.YAMLToJSON(b)
	if err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("services file must be a YAML list: %w", err)
	}
	out := make([]*opensrev1.Service, 0, len(items))
	for i, item := range items {
		s := &opensrev1.Service{}
		if err := jsonIn.Unmarshal(item, s); err != nil {
			return nil, fmt.Errorf("service #%d: %w", i+1, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// MarshalYAML encodes services as a YAML list, sorted by name.
func MarshalYAML(services []*opensrev1.Service) ([]byte, error) {
	sorted := slices.Clone(services)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	items := make([]json.RawMessage, 0, len(sorted))
	for _, s := range sorted {
		b, err := jsonOut.Marshal(s)
		if err != nil {
			return nil, err
		}
		items = append(items, b)
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	// Decode through a yaml.v3 node rather than a map to keep the proto field
	// order (name first) instead of sorting keys alphabetically.
	var doc yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	blockStyle(&doc)
	return yamlv3.Marshal(&doc)
}

// blockStyle turns JSON's flow style into regular block YAML.
func blockStyle(n *yamlv3.Node) {
	n.Style &^= yamlv3.FlowStyle | yamlv3.DoubleQuotedStyle
	for _, c := range n.Content {
		blockStyle(c)
	}
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ValidateOptions carries what validation can check beyond the services themselves.
type ValidateOptions struct {
	Runbooks   map[string]bool   // known runbook paths
	Connectors map[string]string // name -> kind
}

// Validate checks a full catalog. Errors block a write; warnings do not.
func Validate(services []*opensrev1.Service, opts ValidateOptions) []*opensrev1.ValidationIssue {
	var issues []*opensrev1.ValidationIssue
	add := func(svc, field, msg string, warn bool) {
		issues = append(issues, &opensrev1.ValidationIssue{Service: svc, Field: field, Message: msg, Warning: warn})
	}

	names := map[string]bool{}
	for _, s := range services {
		if !nameRe.MatchString(s.Name) {
			add(s.Name, "name", "must be lowercase letters, digits, '.', '_' or '-' (max 63 chars)", false)
		}
		if names[s.Name] {
			add(s.Name, "name", "duplicate service name", false)
		}
		names[s.Name] = true
	}

	for _, s := range services {
		for _, dep := range s.DependsOn {
			if dep == s.Name {
				add(s.Name, "depends_on", "service depends on itself", false)
			} else if !names[dep] {
				add(s.Name, "depends_on", fmt.Sprintf("unknown service %q (add it to the catalog, even as an external dependency)", dep), true)
			}
		}
		if s.Logs == nil || s.Logs.Source == "" || s.Logs.Selector == "" {
			add(s.Name, "logs", "no log source: investigations will have no logs for this service", true)
		} else if opts.Connectors != nil {
			if kind, ok := opts.Connectors[s.Logs.Source]; !ok {
				add(s.Name, "logs.source", fmt.Sprintf("unknown connector %q", s.Logs.Source), true)
			} else if !slices.Contains(logs.Kinds, kind) {
				add(s.Name, "logs.source", fmt.Sprintf("connector %q is a %s connector, not a log connector", s.Logs.Source, kind), false)
			}
		}
		if d := s.Deploys; d != nil && d.Source != "" {
			if d.Repo == "" {
				add(s.Name, "deploys.repo", "required when deploys.source is set", false)
			}
			if opts.Connectors != nil {
				if kind, ok := opts.Connectors[d.Source]; !ok {
					add(s.Name, "deploys.source", fmt.Sprintf("unknown connector %q", d.Source), true)
				} else if kind != "github" {
					add(s.Name, "deploys.source", fmt.Sprintf("connector %q is a %s connector, not a deploy connector", d.Source, kind), false)
				}
			}
		}
		if opts.Runbooks != nil {
			for _, rb := range s.Runbooks {
				if !opts.Runbooks[rb] {
					add(s.Name, "runbooks", fmt.Sprintf("runbook %q not found", rb), true)
				}
			}
		}
		if len(s.Owners) == 0 {
			add(s.Name, "owners", "no owners", true)
		}
	}

	for _, cycle := range NewGraph(services).Cycles() {
		add(cycle[0], "depends_on", fmt.Sprintf("dependency cycle: %v", cycle), true)
	}
	return issues
}

// HasErrors reports whether any issue is blocking.
func HasErrors(issues []*opensrev1.ValidationIssue) bool {
	return slices.ContainsFunc(issues, func(i *opensrev1.ValidationIssue) bool { return !i.Warning })
}
