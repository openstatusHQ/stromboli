package main

import (
	"context"
	"fmt"
	"maps"
	"os/exec"
	"runtime"
	"slices"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	stromboliv1 "github.com/openstatushq/stromboli/gen/stromboli/v1"
	"github.com/openstatushq/stromboli/internal/connector"
	"github.com/openstatushq/stromboli/internal/connector/oauth"
)

// oauthLogin logs in to the connector's MCP server in a browser and adds the
// grant to cfg.
func (a *app) oauthLogin(ctx context.Context, kind, server string, cfg map[string]string) error {
	if !a.canPrompt() {
		return fmt.Errorf("%s connectors log in with OAuth in a browser: run this command in a terminal", kind)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	g, err := oauth.Login(ctx, oauth.LoginConfig{
		ServerURL:         server,
		ClientMetadataURL: cfg["client_metadata_url"],
		Out:               a.stderr,
		In:                a.stdin,
		OpenBrowser:       openBrowser,
	})
	if err != nil {
		return fmt.Errorf("log in to %s: %w", server, err)
	}
	maps.Copy(cfg, g.Config())
	a.info("Logged in to %s.", server)
	return nil
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func (a *app) connectorLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login <name>",
		Short: "Log a connector in again with OAuth",
		Long: `Log in again to the MCP server of a connector that uses OAuth (vercel), for
example after its login expired or was revoked. Its other settings are kept.`,
		Example: `  stromboli connector login vercel`,
		Args:    exactArgs(1, "<name>"),
		RunE: func(cmd *cobra.Command, pos []string) error {
			name := pos[0]
			c, err := a.api()
			if err != nil {
				return err
			}
			resp, err := c.connectors.ListConnectors(cmd.Context(), connect.NewRequest(&stromboliv1.ListConnectorsRequest{}))
			if err != nil {
				return err
			}
			i := slices.IndexFunc(resp.Msg.Connectors, func(cn *stromboliv1.Connector) bool { return cn.Name == name })
			if i < 0 {
				return fmt.Errorf("connector %q not found", name)
			}
			cn := resp.Msg.Connectors[i]
			// Keep the public settings; the login replaces the OAuth keys.
			cfg := map[string]string{}
			for k, v := range cn.Config {
				if connector.IsOAuthKey(cn.Kind, k) {
					continue
				}
				if connector.IsSecret(cn.Kind, k) {
					return fmt.Errorf("connector %q has secret %s, which login cannot keep: use `stromboli connector add %s %s ... --replace`", name, k, name, cn.Kind)
				}
				cfg[k] = v
			}
			server, ok := connector.OAuthServer(cn.Kind, cfg)
			if !ok {
				return fmt.Errorf("connector %q (%s) does not log in with OAuth", name, cn.Kind)
			}
			if err := a.oauthLogin(cmd.Context(), cn.Kind, server, cfg); err != nil {
				return err
			}
			if _, err := c.connectors.CreateConnector(cmd.Context(), connect.NewRequest(&stromboliv1.CreateConnectorRequest{
				Name: name, Kind: cn.Kind, Config: cfg, Replace: true,
			})); err != nil {
				return err
			}
			a.info("Connector %q saved.", name)
			return a.testConnector(cmd, c, name)
		},
	}
}
