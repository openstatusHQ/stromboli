package catalog

import (
	"slices"
	"sort"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
)

// Graph is the dependency graph of the catalog. Edges point from a service to
// what it depends on (downstream = what I call, upstream = who calls me).
type Graph struct {
	services map[string]*opensrev1.Service
	deps     map[string][]string
	rdeps    map[string][]string
}

func NewGraph(services []*opensrev1.Service) *Graph {
	g := &Graph{
		services: map[string]*opensrev1.Service{},
		deps:     map[string][]string{},
		rdeps:    map[string][]string{},
	}
	for _, s := range services {
		g.services[s.Name] = s
	}
	for _, s := range services {
		for _, d := range s.DependsOn {
			g.deps[s.Name] = append(g.deps[s.Name], d)
			g.rdeps[d] = append(g.rdeps[d], s.Name)
		}
	}
	for _, m := range []map[string][]string{g.deps, g.rdeps} {
		for k := range m {
			sort.Strings(m[k])
		}
	}
	return g
}

func (g *Graph) Service(name string) (*opensrev1.Service, bool) {
	s, ok := g.services[name]
	return s, ok
}

// Downstream returns the services name depends on.
func (g *Graph) Downstream(name string) []string { return slices.Clone(g.deps[name]) }

// Upstream returns the services that depend on name.
func (g *Graph) Upstream(name string) []string { return slices.Clone(g.rdeps[name]) }

// BlastRadius returns every service that transitively depends on name.
func (g *Graph) BlastRadius(name string) []string {
	seen := map[string]bool{name: true}
	queue := []string{name}
	var out []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, up := range g.rdeps[cur] {
			if !seen[up] {
				seen[up] = true
				out = append(out, up)
				queue = append(queue, up)
			}
		}
	}
	sort.Strings(out)
	return out
}

// FindByMonitor returns the service that lists monitor (e.g. "openstatus:42").
func (g *Graph) FindByMonitor(monitor string) (*opensrev1.Service, bool) {
	for _, s := range g.services {
		if slices.Contains(s.Monitors, monitor) {
			return s, true
		}
	}
	return nil, false
}

// Cycles returns each dependency cycle once.
func (g *Graph) Cycles() [][]string {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack []string
	var cycles [][]string

	names := make([]string, 0, len(g.services))
	for n := range g.services {
		names = append(names, n)
	}
	sort.Strings(names)

	var visit func(string)
	visit = func(n string) {
		color[n] = grey
		stack = append(stack, n)
		for _, d := range g.deps[n] {
			switch color[d] {
			case white:
				if _, ok := g.services[d]; ok {
					visit(d)
				}
			case grey:
				i := slices.Index(stack, d)
				cycles = append(cycles, append(slices.Clone(stack[i:]), d))
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
	}
	for _, n := range names {
		if color[n] == white {
			visit(n)
		}
	}
	return cycles
}
