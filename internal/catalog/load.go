package catalog

import (
	"context"
	"fmt"

	opensrev1 "github.com/openstatushq/opensre/gen/opensre/v1"
	"github.com/openstatushq/opensre/internal/store/db"
)

// Load reads the whole catalog from the database.
func Load(ctx context.Context, q *db.Queries) ([]*opensrev1.Service, *Graph, error) {
	rows, err := q.ListServices(ctx)
	if err != nil {
		return nil, nil, err
	}
	services := make([]*opensrev1.Service, 0, len(rows))
	for _, r := range rows {
		s, err := UnmarshalSpec(r.Spec)
		if err != nil {
			return nil, nil, fmt.Errorf("service %q: %w", r.Name, err)
		}
		services = append(services, s)
	}
	return services, NewGraph(services), nil
}
