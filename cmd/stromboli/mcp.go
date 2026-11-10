package main

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/openstatushq/stromboli/internal/mcpserver"
)

func (a *app) mcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve stromboli's MCP tools over stdio, for MCP clients",
		Long: `Serve stromboli's MCP tools over stdio, for MCP clients that launch a local
process (Claude Code, Claude Desktop, Cursor...). It calls the stromboli server
at --url with STROMBOLI_API_TOKEN, like every other command.

The tools read the catalog, briefs, investigations and machine actions, and
start investigations. They cannot approve actions or change the catalog or
connectors.

A server with STROMBOLI_MCP_ENABLED=true also serves the same tools over HTTP
at /mcp.`,
		Example: `  # Claude Code
  claude mcp add stromboli -e STROMBOLI_URL=https://stromboli.example.com -e STROMBOLI_API_TOKEN=... -- stromboli mcp

  # Any client configured with a command
  {"command": "stromboli", "args": ["mcp"], "env": {"STROMBOLI_URL": "...", "STROMBOLI_API_TOKEN": "..."}}`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			s := mcpserver.New(mcpserver.API{
				Catalog:        c.catalog,
				Investigations: c.investigation,
				Actions:        c.actions,
			}, mcpserver.Options{Version: buildVersion()})
			// stdout carries the protocol; logs already go to stderr.
			return s.Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}
}
