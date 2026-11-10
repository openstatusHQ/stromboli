package main

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/openstatushq/stromboli/internal/config"
	"github.com/openstatushq/stromboli/internal/server"
	"github.com/openstatushq/stromboli/internal/store"
)

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the stromboli server",
		Long: `Run the stromboli server: the API, the alert webhooks and the investigation agent.

It is configured with environment variables (STROMBOLI_DB_URL, STROMBOLI_SECRET_KEY,
STROMBOLI_API_TOKEN, STROMBOLI_MODEL, ANTHROPIC_API_KEY, ...). Migrations run on startup.
See ` + docsURL + ` for the full list.`,
		Example: `  STROMBOLI_DB_URL=postgres://localhost/stromboli stromboli serve`,
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
		Long:    "Apply database migrations to STROMBOLI_DB_URL and exit. stromboli serve also does this on startup.",
		Example: `  STROMBOLI_DB_URL=postgres://localhost/stromboli stromboli migrate`,
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			url := os.Getenv("STROMBOLI_DB_URL")
			if url == "" {
				return errors.New("STROMBOLI_DB_URL is required: set it to the Postgres URL, e.g. postgres://user:pass@localhost/stromboli")
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
