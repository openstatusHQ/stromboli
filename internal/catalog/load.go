package catalog

import (
	"context"
	"fmt"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
	"github.com/openstatushq/stromboli/internal/store/db"
)

// Load reads the whole catalog from the database.
func Load(ctx context.Context, q *db.Queries) ([]*stromboliv1.Service, *Graph, error) {
	rows, err := q.ListServices(ctx)
	if err != nil {
		return nil, nil, err
	}
	services := make([]*stromboliv1.Service, 0, len(rows))
	for _, r := range rows {
		s, err := UnmarshalSpec(r.Spec)
		if err != nil {
			return nil, nil, fmt.Errorf("service %q: %w", r.Name, err)
		}
		services = append(services, s)
	}
	return services, NewGraph(services), nil
}
