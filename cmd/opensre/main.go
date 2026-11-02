// Command opensre is the opensre server and its CLI.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/openstatushq/opensre/internal/config"
	"github.com/openstatushq/opensre/internal/server"
	"github.com/openstatushq/opensre/internal/store"
)

const usage = `opensre: an AI SRE agent

Server:
  opensre serve                         run the server (configured with env vars, see README)
  opensre migrate                       apply database migrations and exit

Client (uses OPENSRE_URL and OPENSRE_API_TOKEN):
  opensre catalog import <services.yaml> [--runbooks dir] [--prune] [--dry-run]
  opensre catalog export [--runbooks dir]
  opensre catalog validate [--check-connectors]
  opensre catalog discover <log-connector>
  opensre connector add <name> <kind> key=value...  [--replace]
  opensre connector list | test <name> | rm <name>
  opensre brief <service> [--since 1h]
  opensre investigate <service> [--title t] [--description d] [--no-watch]
  opensre investigations [--limit n]
  opensre show <investigation-id>
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	level := slog.LevelInfo
	if os.Getenv("OPENSRE_DEBUG") != "" {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "serve":
		err = serve(ctx)
	case "migrate":
		err = migrate(ctx)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		err = runClient(ctx, cmd, args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", strings.TrimPrefix(err.Error(), "unknown: "))
		os.Exit(1)
	}
}

func serve(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	return server.Run(ctx, cfg)
}

func migrate(ctx context.Context) error {
	url := os.Getenv("OPENSRE_DB_URL")
	if url == "" {
		return fmt.Errorf("OPENSRE_DB_URL is required")
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	fmt.Println("migrations applied")
	return nil
}
