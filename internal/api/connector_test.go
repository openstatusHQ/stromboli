package api

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/openstatushq/stromboli/internal/store/db"
)

func TestToConnectorSecretKeys(t *testing.T) {
	for _, c := range []struct {
		kind, config string
		want         string
	}{
		{"axiom", `{"org_id":"o"}`, "map[org_id:o token:]"},
		{"axiom", `{"authorized_at":"t","client_id":"c","mcp_url":"u"}`, "map[authorized_at:t client_id:c mcp_url:u refresh_token:]"},
		{"vercel", `{"authorized_at":"t","team_id":"acme"}`, "map[authorized_at:t refresh_token: team_id:acme]"},
		{"loki", `{"url":"u"}`, "map[bearer_token: password: url:u]"},
	} {
		got := toConnector(db.Connector{Name: "x", Kind: c.kind, Config: json.RawMessage(c.config), Secrets: []byte{1}})
		if fmt.Sprint(got.Config) != c.want {
			t.Errorf("%s %s: config = %v, want %s", c.kind, c.config, got.Config, c.want)
		}
	}
}
