package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgconn"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/connector"
	"github.com/openstatushq/opensre/internal/connector/logs"
	"github.com/openstatushq/opensre/internal/secrets"
	"github.com/openstatushq/opensre/internal/store"
	"github.com/openstatushq/opensre/internal/store/db"
)

type ConnectorService struct {
	store      *store.Store
	box        *secrets.Box
	connectors *connector.Registry
}

func NewConnectorService(s *store.Store, box *secrets.Box, reg *connector.Registry) *ConnectorService {
	return &ConnectorService{store: s, box: box, connectors: reg}
}

func (c *ConnectorService) CreateConnector(ctx context.Context, req *connect.Request[opensrev1.CreateConnectorRequest]) (*connect.Response[opensrev1.CreateConnectorResponse], error) {
	m := req.Msg
	if m.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name is required"))
	}
	if err := connector.Validate(m.Kind, m.Config); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	public, sealed, err := connector.Split(c.box, m.Kind, m.Config)
	if err != nil {
		return nil, internal(err)
	}
	if m.Replace {
		err = c.store.ReplaceConnector(ctx, db.ReplaceConnectorParams{Name: m.Name, Kind: m.Kind, Config: public, Secrets: sealed})
	} else {
		err = c.store.Queries.CreateConnector(ctx, db.CreateConnectorParams{Name: m.Name, Kind: m.Kind, Config: public, Secrets: sealed})
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("connector %q already exists (set replace to overwrite)", m.Name))
		}
		return nil, internal(err)
	}
	row, err := c.store.GetConnector(ctx, m.Name)
	if err != nil {
		return nil, internal(err)
	}
	return connect.NewResponse(&opensrev1.CreateConnectorResponse{Connector: toConnector(row)}), nil
}

func (c *ConnectorService) ListConnectors(ctx context.Context, _ *connect.Request[opensrev1.ListConnectorsRequest]) (*connect.Response[opensrev1.ListConnectorsResponse], error) {
	rows, err := c.store.Queries.ListConnectors(ctx)
	if err != nil {
		return nil, internal(err)
	}
	resp := &opensrev1.ListConnectorsResponse{}
	for _, r := range rows {
		resp.Connectors = append(resp.Connectors, toConnector(r))
	}
	return connect.NewResponse(resp), nil
}

func (c *ConnectorService) DeleteConnector(ctx context.Context, req *connect.Request[opensrev1.DeleteConnectorRequest]) (*connect.Response[opensrev1.DeleteConnectorResponse], error) {
	n, err := c.store.Queries.DeleteConnector(ctx, req.Msg.Name)
	if err != nil {
		return nil, internal(err)
	}
	if n == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("connector %q not found", req.Msg.Name))
	}
	return connect.NewResponse(&opensrev1.DeleteConnectorResponse{}), nil
}

func (c *ConnectorService) TestConnector(ctx context.Context, req *connect.Request[opensrev1.TestConnectorRequest]) (*connect.Response[opensrev1.TestConnectorResponse], error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := c.connectors.Check(ctx, req.Msg.Name); err != nil {
		return connect.NewResponse(&opensrev1.TestConnectorResponse{Ok: false, Message: err.Error()}), nil
	}
	return connect.NewResponse(&opensrev1.TestConnectorResponse{Ok: true, Message: "ok"}), nil
}

// toConnector returns public settings and lists secret keys with empty values.
func toConnector(r db.Connector) *opensrev1.Connector {
	cfg := map[string]string{}
	_ = json.Unmarshal(r.Config, &cfg)
	if len(r.Secrets) > 0 {
		for _, k := range []string{"password", "bearer_token", "token"} {
			if connector.IsSecret(r.Kind, k) {
				cfg[k] = ""
			}
		}
	}
	return &opensrev1.Connector{Name: r.Name, Kind: r.Kind, Config: cfg}
}

// checkSelector runs a cheap count query to verify a service's log selector.
func checkSelector(ctx context.Context, reg *connector.Registry, s *opensrev1.Service) error {
	src, err := reg.Logs(ctx, s.Logs.Source)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	end := time.Now()
	n, err := src.Count(ctx, logs.Query{Selector: s.Logs.Selector, Start: end.Add(-time.Hour), End: end})
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("selector matched no log lines in the last hour")
	}
	return nil
}
