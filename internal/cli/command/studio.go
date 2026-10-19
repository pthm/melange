package command

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	_ "github.com/lib/pq"
	"github.com/spf13/cobra"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/internal/studio"
)

var (
	studioDB       string
	studioDBSchema string
	studioAddr     string
	studioSchema   string
)

var studioCmd = &cobra.Command{
	Use:   "studio",
	Short: "Open a local, read-only web console for a database (experimental)",
	Long: `Studio serves a web console on localhost for inspecting a melange-managed
database: the deployed model, Explain traces, and Expand trees.

It needs only a connection: the model is read from the database, so no
schema file or repo checkout is required. Every connection is opened
read-only, and the server binds a loopback address only. The one exception
is an Explain with what-if tuples: it opens a read-write transaction to
create a temporary view, switches it to read-only, and always rolls it
back, so nothing is committed.`,
	Example: `  # Point the console at staging
  melange studio --env staging

  # Fixed port
  melange studio --addr 127.0.0.1:7070`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dsn, err := resolveDSN(studioDB)
		if err != nil {
			return err
		}
		return runStudio(dsn, resolveString(studioDBSchema, cfg.Database.Schema, "public"), resolveString(studioSchema, cfg.Schema), studioAddr)
	},
}

func init() {
	f := studioCmd.Flags()
	f.StringVar(&studioDB, "db", "", "database URL")
	f.StringVar(&studioDBSchema, "db-schema", "", "database schema (default: config database.schema, else public)")
	f.StringVar(&studioSchema, "schema", "", "local schema file for the Health page's drift checks (default: config schema; optional)")
	f.StringVar(&studioAddr, "addr", "127.0.0.1:0", "loopback address to listen on (port 0 = pick a free port)")
}

func runStudio(dsn, databaseSchema, schemaPath, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return cli.GeneralError("listen address", err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return cli.GeneralError("listen address", fmt.Errorf("%q is not a loopback address; studio is local-only", host))
	}

	roDSN, err := studio.ReadOnlyDSN(dsn)
	if err != nil {
		return cli.ConfigError("database URL", err)
	}
	db, err := sql.Open("postgres", roDSN)
	if err != nil {
		return cli.DBConnectError("connecting to database", err)
	}
	defer func() { _ = db.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := studio.VerifyReadOnly(ctx, db); err != nil {
		return cli.DBConnectError("connecting to database", err)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return cli.GeneralError("listening", err)
	}
	srv := &http.Server{
		Handler:           studio.New(db, databaseSchema, activeEnv, schemaPath).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Fprintf(os.Stderr, "melange studio: http://%s (ctrl-c to stop)\n", ln.Addr())

	// The root command's background update check (off with --no-update-check
	// or in CI) also feeds the top bar's "update available" badge. The result
	// goes back on the channel for the usual notice on exit.
	if updateResult != nil {
		go func() {
			info := <-updateResult
			if info != nil && info.UpdateAvailable {
				studio.SetLatestRelease(info.LatestVersion)
			}
			updateResult <- info
		}()
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return cli.GeneralError("serving", err)
		}
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
