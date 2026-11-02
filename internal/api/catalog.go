// Package api implements the ConnectRPC services.
package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/catalog"
	"github.com/openstatushq/opensre/internal/connector"
	"github.com/openstatushq/opensre/internal/store"
	"github.com/openstatushq/opensre/internal/store/db"
)

type CatalogService struct {
	store      *store.Store
	connectors *connector.Registry
}

func NewCatalogService(s *store.Store, reg *connector.Registry) *CatalogService {
	return &CatalogService{store: s, connectors: reg}
}

func (c *CatalogService) ListServices(ctx context.Context, _ *connect.Request[opensrev1.ListServicesRequest]) (*connect.Response[opensrev1.ListServicesResponse], error) {
	services, _, err := catalog.Load(ctx, c.store.Queries)
	if err != nil {
		return nil, internal(err)
	}
	return connect.NewResponse(&opensrev1.ListServicesResponse{Services: services}), nil
}

func (c *CatalogService) GetService(ctx context.Context, req *connect.Request[opensrev1.GetServiceRequest]) (*connect.Response[opensrev1.GetServiceResponse], error) {
	_, graph, err := catalog.Load(ctx, c.store.Queries)
	if err != nil {
		return nil, internal(err)
	}
	svc, ok := graph.Service(req.Msg.Name)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("service %q not found", req.Msg.Name))
	}
	return connect.NewResponse(&opensrev1.GetServiceResponse{
		Service:    svc,
		Upstream:   graph.Upstream(svc.Name),
		Downstream: graph.Downstream(svc.Name),
	}), nil
}

func (c *CatalogService) UpsertService(ctx context.Context, req *connect.Request[opensrev1.UpsertServiceRequest]) (*connect.Response[opensrev1.UpsertServiceResponse], error) {
	svc := req.Msg.Service
	if svc == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("service is required"))
	}
	services, _, err := catalog.Load(ctx, c.store.Queries)
	if err != nil {
		return nil, internal(err)
	}
	services = slices.DeleteFunc(services, func(s *opensrev1.Service) bool { return s.Name == svc.Name })
	services = append(services, svc)

	opts, err := c.validateOptions(ctx, nil)
	if err != nil {
		return nil, internal(err)
	}
	issues := onlyFor(catalog.Validate(services, opts), svc.Name)
	if catalog.HasErrors(issues) {
		return nil, invalid(issues)
	}
	spec, err := catalog.MarshalSpec(svc)
	if err != nil {
		return nil, internal(err)
	}
	if err := c.store.UpsertService(ctx, db.UpsertServiceParams{Name: svc.Name, Spec: spec}); err != nil {
		return nil, internal(err)
	}
	return connect.NewResponse(&opensrev1.UpsertServiceResponse{Service: svc, Issues: issues}), nil
}

func (c *CatalogService) DeleteService(ctx context.Context, req *connect.Request[opensrev1.DeleteServiceRequest]) (*connect.Response[opensrev1.DeleteServiceResponse], error) {
	n, err := c.store.Queries.DeleteService(ctx, req.Msg.Name)
	if err != nil {
		return nil, internal(err)
	}
	if n == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("service %q not found", req.Msg.Name))
	}
	return connect.NewResponse(&opensrev1.DeleteServiceResponse{}), nil
}

func (c *CatalogService) ImportCatalog(ctx context.Context, req *connect.Request[opensrev1.ImportCatalogRequest]) (*connect.Response[opensrev1.ImportCatalogResponse], error) {
	m := req.Msg
	services, err := catalog.ParseYAML(m.ServicesYaml)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Without prune, the imported services are merged into the existing catalog.
	full := services
	if !m.Prune {
		existing, _, err := catalog.Load(ctx, c.store.Queries)
		if err != nil {
			return nil, internal(err)
		}
		imported := map[string]bool{}
		for _, s := range services {
			imported[s.Name] = true
		}
		for _, s := range existing {
			if !imported[s.Name] {
				full = append(full, s)
			}
		}
	}
	opts, err := c.validateOptions(ctx, m.Runbooks)
	if err != nil {
		return nil, internal(err)
	}
	issues := catalog.Validate(full, opts)
	resp := &opensrev1.ImportCatalogResponse{Issues: issues}
	if catalog.HasErrors(issues) {
		return nil, invalid(issues)
	}
	if m.DryRun {
		return connect.NewResponse(resp), nil
	}

	err = c.store.Tx(ctx, func(q *db.Queries) error {
		names := make([]string, 0, len(services))
		for _, s := range services {
			spec, err := catalog.MarshalSpec(s)
			if err != nil {
				return err
			}
			if err := q.UpsertService(ctx, db.UpsertServiceParams{Name: s.Name, Spec: spec}); err != nil {
				return err
			}
			names = append(names, s.Name)
		}
		resp.ServicesUpserted = int32(len(services))

		paths := make([]string, 0, len(m.Runbooks))
		for _, rb := range m.Runbooks {
			if err := q.UpsertRunbook(ctx, db.UpsertRunbookParams{Path: rb.Path, Content: rb.Content}); err != nil {
				return err
			}
			paths = append(paths, rb.Path)
		}
		resp.RunbooksUpserted = int32(len(m.Runbooks))

		if m.Prune {
			n, err := q.DeleteServicesNotIn(ctx, names)
			if err != nil {
				return err
			}
			resp.ServicesDeleted = int32(n)
			if _, err := q.DeleteRunbooksNotIn(ctx, paths); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, internal(err)
	}
	return connect.NewResponse(resp), nil
}

func (c *CatalogService) ExportCatalog(ctx context.Context, _ *connect.Request[opensrev1.ExportCatalogRequest]) (*connect.Response[opensrev1.ExportCatalogResponse], error) {
	services, _, err := catalog.Load(ctx, c.store.Queries)
	if err != nil {
		return nil, internal(err)
	}
	out, err := catalog.MarshalYAML(services)
	if err != nil {
		return nil, internal(err)
	}
	rows, err := c.store.ListRunbooks(ctx)
	if err != nil {
		return nil, internal(err)
	}
	resp := &opensrev1.ExportCatalogResponse{ServicesYaml: out}
	for _, r := range rows {
		resp.Runbooks = append(resp.Runbooks, &opensrev1.Runbook{Path: r.Path, Content: r.Content})
	}
	return connect.NewResponse(resp), nil
}

func (c *CatalogService) ValidateCatalog(ctx context.Context, req *connect.Request[opensrev1.ValidateCatalogRequest]) (*connect.Response[opensrev1.ValidateCatalogResponse], error) {
	services, _, err := catalog.Load(ctx, c.store.Queries)
	if err != nil {
		return nil, internal(err)
	}
	opts, err := c.validateOptions(ctx, nil)
	if err != nil {
		return nil, internal(err)
	}
	issues := catalog.Validate(services, opts)
	if req.Msg.CheckConnectors {
		issues = append(issues, c.checkLogSelectors(ctx, services)...)
	}
	return connect.NewResponse(&opensrev1.ValidateCatalogResponse{Issues: issues}), nil
}

func (c *CatalogService) DiscoverServices(ctx context.Context, req *connect.Request[opensrev1.DiscoverServicesRequest]) (*connect.Response[opensrev1.DiscoverServicesResponse], error) {
	src, err := c.connectors.Logs(ctx, req.Msg.Connector)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	found, err := src.Services(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	services, _, err := catalog.Load(ctx, c.store.Queries)
	if err != nil {
		return nil, internal(err)
	}
	known := map[string]bool{}
	for _, s := range services {
		known[s.Name] = true
	}
	resp := &opensrev1.DiscoverServicesResponse{}
	for _, f := range found {
		name := strings.ToLower(f.Name)
		if name == "" || known[name] {
			continue
		}
		known[name] = true
		resp.Candidates = append(resp.Candidates, &opensrev1.Service{
			Name: name,
			Logs: &opensrev1.LogsConfig{Source: req.Msg.Connector, Selector: f.Selector},
		})
	}
	return connect.NewResponse(resp), nil
}

func (c *CatalogService) validateOptions(ctx context.Context, extraRunbooks []*opensrev1.Runbook) (catalog.ValidateOptions, error) {
	opts := catalog.ValidateOptions{Runbooks: map[string]bool{}, Connectors: map[string]string{}}
	rbs, err := c.store.ListRunbooks(ctx)
	if err != nil {
		return opts, err
	}
	for _, r := range rbs {
		opts.Runbooks[r.Path] = true
	}
	for _, r := range extraRunbooks {
		opts.Runbooks[r.Path] = true
	}
	conns, err := c.store.ListConnectors(ctx)
	if err != nil {
		return opts, err
	}
	for _, cn := range conns {
		opts.Connectors[cn.Name] = cn.Kind
	}
	return opts, nil
}

func (c *CatalogService) checkLogSelectors(ctx context.Context, services []*opensrev1.Service) []*opensrev1.ValidationIssue {
	var issues []*opensrev1.ValidationIssue
	for _, s := range services {
		if s.Logs == nil || s.Logs.Source == "" || s.Logs.Selector == "" {
			continue
		}
		if err := checkSelector(ctx, c.connectors, s); err != nil {
			issues = append(issues, &opensrev1.ValidationIssue{Service: s.Name, Field: "logs.selector", Message: err.Error(), Warning: true})
		}
	}
	return issues
}

func onlyFor(issues []*opensrev1.ValidationIssue, service string) []*opensrev1.ValidationIssue {
	return slices.DeleteFunc(issues, func(i *opensrev1.ValidationIssue) bool { return i.Service != service })
}

func invalid(issues []*opensrev1.ValidationIssue) error {
	var msgs []string
	for _, i := range issues {
		if !i.Warning {
			msgs = append(msgs, fmt.Sprintf("%s.%s: %s", i.Service, i.Field, i.Message))
		}
	}
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("catalog is invalid: %s", strings.Join(msgs, "; ")))
}

func internal(err error) error {
	return connect.NewError(connect.CodeInternal, err)
}
