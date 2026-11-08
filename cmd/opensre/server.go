package main

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/openstatushq/opensre/internal/config"
	"github.com/openstatushq/opensre/internal/server"
	"github.com/openstatushq/opensre/internal/store"
)

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the opensre server",
		Long: `Run the opensre server: the API, the alert webhooks and the investigation agent.

It is configured with environment variables (OPENSRE_DB_URL, OPENSRE_SECRET_KEY,
OPENSRE_API_TOKEN, OPENSRE_MODEL, ANTHROPIC_API_KEY, ...). Migrations run on startup.
See ` + docsURL + ` for the full list.`,
		Example: `  OPENSRE_DB_URL=postgres://localhost/opensre opensre serve`,
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			return server.Run(cmd.Context(), cfg)
		},
	}
}

func (a *app) migrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "migrate",
		Short:   "Apply database migrations and exit",
		Long:    "Apply database migrations to OPENSRE_DB_URL and exit. opensre serve also does this on startup.",
		Example: `  OPENSRE_DB_URL=postgres://localhost/opensre opensre migrate`,
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			url := os.Getenv("OPENSRE_DB_URL")
			if url == "" {
				return errors.New("OPENSRE_DB_URL is required: set it to the Postgres URL, e.g. postgres://user:pass@localhost/opensre")
			}
			st, err := store.Open(cmd.Context(), url)
			if err != nil {
				return err
			}
			defer st.Close()
			if err := st.Migrate(cmd.Context()); err != nil {
				return err
			}
			a.out("Migrations applied.")
			return nil
		},
	}
}
