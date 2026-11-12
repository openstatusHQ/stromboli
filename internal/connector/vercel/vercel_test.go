package vercel

import (
	"context"
	"strings"
	"testing"

	"github.com/openstatushq/stromboli/internal/connector/mcpconn/mcptest"
)

func TestCheckWrongTeam(t *testing.T) {
	srv := mcptest.New(t, map[string]mcptest.Handler{
		"list_projects": func(args map[string]any) (any, error) {
			if args["slug"] == "acme" {
				return map[string]any{"result": map[string]any{"projects": []any{}}}, nil
			}
			// Vercel's tool errors carry a JSON body.
			return nil, errString(`{"error":{"message":"Vercel API request failed (403: forbidden). Not authorized","status":403},"cliFallback":{"guidance":"..."}}`)
		},
	}, true)
	c := srv.Client(t, "list_projects")
	if err := Check(context.Background(), c, "acme"); err != nil {
		t.Fatal(err)
	}
	err := Check(context.Background(), c, "acmee")
	if err == nil || !strings.Contains(err.Error(), "Not authorized") || strings.Contains(err.Error(), "cliFallback") || !strings.Contains(err.Error(), `team_id "acmee"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestTeamArgs(t *testing.T) {
	if a := TeamArgs("team_x"); a["teamId"] != "team_x" || a["slug"] != nil {
		t.Errorf("id: %v", a)
	}
	if a := TeamArgs("acme"); a["slug"] != "acme" || a["teamId"] != nil {
		t.Errorf("slug: %v", a)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
