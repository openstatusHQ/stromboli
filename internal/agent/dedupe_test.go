package agent

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/openstatushq/stromboli/internal/config"
	"github.com/openstatushq/stromboli/internal/store"
	"github.com/openstatushq/stromboli/internal/store/db"
)

// Concurrent repeats of an alert (Alertmanager replicas) must start one
// investigation, not one each.
func TestCreateOrReuseConcurrent(t *testing.T) {
	dbURL := os.Getenv("STROMBOLI_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("set STROMBOLI_TEST_DB_URL to run")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	fp := fmt.Sprintf("dedupe-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = st.Pool.Exec(context.Background(), "DELETE FROM investigations WHERE fingerprint = $1", fp)
	})

	e := &Engine{cfg: config.Config{DedupeWindow: time.Minute}, store: st}
	const n = 10
	var wg sync.WaitGroup
	ids := make([]uuid.UUID, n)
	created := make([]bool, n)
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			inv, reused, err := e.createOrReuse(ctx, StartRequest{Fingerprint: fp}, db.CreateInvestigationParams{
				ID: uuid.New(), Title: "t", Source: "test", Status: StatusRunning, Alert: []byte(`{}`),
				Fingerprint: pgtype.Text{String: fp, Valid: true},
			})
			ids[i], created[i], errs[i] = inv.ID, !reused, err
		})
	}
	wg.Wait()

	nCreated := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if created[i] {
			nCreated++
		}
		if ids[i] != ids[0] {
			t.Errorf("call %d got investigation %s, want %s", i, ids[i], ids[0])
		}
	}
	if nCreated != 1 {
		t.Errorf("%d investigations created, want 1", nCreated)
	}
}
