// Command runner is the platform's Liquibase runner: the entrypoint of the image every
// application's migration image is built FROM.
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JorisJonkers-dev/liquibase-runner/internal/cli"
	"github.com/JorisJonkers-dev/liquibase-runner/internal/runner"
	"github.com/JorisJonkers-dev/liquibase-runner/internal/store"
)

const liquibase = "/liquibase/liquibase"

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return cli.Run(ctx, os.Args[1:], cli.World{
		Getenv:   os.Getenv,
		Stdin:    os.Stdin,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		ReadFile: cli.ReadFile,
		HTTP:     &http.Client{Timeout: 30 * time.Second},
		Open: func(ctx context.Context, db runner.Database) (runner.Store, error) {
			return store.Open(ctx, db.Host, db.Port, db.Name, db.Username, db.Password)
		},
		Liquibase: runner.Exec(liquibase, os.Stdout, os.Stderr),
	})
}
