// Package cli is the runner's command line: the two commands the platform runs in the cluster,
// and the `ci` commands an application repository runs against a throwaway database.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/JorisJonkers-dev/liquibase-runner/internal/proof"
	"github.com/JorisJonkers-dev/liquibase-runner/internal/runner"
	"github.com/JorisJonkers-dev/liquibase-runner/internal/vault"
)

const usage = `usage:
  runner up <tag>                  apply the changelog, then tag the database
  runner down <tag>                roll the database back to a tag
  runner ci up <tag>               up, signed in from DATABASE_USERNAME and DATABASE_PASSWORD
  runner ci reversibility          apply, roll back and apply again every changeset not yet run
  runner ci non-transactional      print whether the release is one non-transactional changeset
  runner ci proof --application <id> [--tested-against sha256:...] --non-transactional=<bool>
                                   merge an entry into the Migration Proof on stdin, to stdout

A tag is an Application revision's first 12 hex digits.
`

// Exit codes: a failed run, and a call the runner could not read.
const (
	exitFailed = 1
	exitUsage  = 2
)

// The image's own defaults. An application's migration image overrides CHANGELOG_FILE when its
// changesets are already recorded under another name.
const (
	defaultChangelogDir  = "/liquibase/changelog"
	defaultChangelogFile = "changelog.yaml"
	defaultStaleAfter    = 30 * time.Minute
	tokenFile            = "/var/run/secrets/kubernetes.io/serviceaccount/token" //nolint:gosec // a path, not a credential.
)

// World is everything outside the process the command line touches.
type World struct {
	Getenv    func(string) string
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	ReadFile  func(string) ([]byte, error)
	HTTP      *http.Client
	Open      func(ctx context.Context, db runner.Database) (runner.Store, error)
	Liquibase runner.Liquibase
}

var errUsage = errors.New("usage")

// Run runs one command and returns the process's exit code.
func Run(ctx context.Context, args []string, w World) int {
	err := dispatch(ctx, args, w)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprintf(w.Stderr, "%v\n\n%s", err, usage)
		return exitUsage
	default:
		_, _ = fmt.Fprintf(w.Stderr, "runner: %v\n", err)
		return exitFailed
	}
}

func dispatch(ctx context.Context, args []string, w World) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: no command", errUsage)
	}
	switch args[0] {
	case "up", "down":
		tag, err := oneTag(args)
		if err != nil {
			return err
		}
		r, err := inCluster(ctx, w)
		if err != nil {
			return err
		}
		if args[0] == "up" {
			return r.Up(ctx, tag)
		}
		return r.Down(ctx, tag)
	case "ci":
		return ci(ctx, args[1:], w)
	default:
		return fmt.Errorf("%w: unknown command %q", errUsage, args[0])
	}
}

func ci(ctx context.Context, args []string, w World) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: ci needs a command", errUsage)
	}
	if args[0] == "proof" {
		return writeProof(args[1:], w)
	}
	r, err := inCI(w)
	if err != nil {
		return err
	}
	switch args[0] {
	case "up":
		tag, err := oneTag(args)
		if err != nil {
			return err
		}
		return r.Up(ctx, tag)
	case "reversibility":
		return r.Reversibility(ctx)
	case "non-transactional":
		alone, err := r.NonTransactional(ctx)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w.Stdout, alone)
		return err
	default:
		return fmt.Errorf("%w: unknown ci command %q", errUsage, args[0])
	}
}

func oneTag(args []string) (string, error) {
	if len(args) != 2 {
		return "", fmt.Errorf("%w: %s takes one tag", errUsage, args[0])
	}
	return args[1], nil
}

// inCluster signs in the way the render arranges it: the migration identity reads its owner
// credential from Vault itself.
func inCluster(ctx context.Context, w World) (runner.Runner, error) {
	r, err := base(w, "VAULT_ADDR", "VAULT_ROLE", "VAULT_CREDENTIALS_PATH")
	if err != nil {
		return runner.Runner{}, err
	}
	jwt, err := w.ReadFile(tokenFile)
	if err != nil {
		return runner.Runner{}, fmt.Errorf("read the ServiceAccount token: %w", err)
	}
	client := vault.Client{Addr: w.Getenv("VAULT_ADDR"), HTTP: w.HTTP}
	login, err := client.Credential(ctx, w.Getenv("VAULT_ROLE"), strings.TrimSpace(string(jwt)), w.Getenv("VAULT_CREDENTIALS_PATH"))
	if err != nil {
		return runner.Runner{}, err
	}
	r.Database.Username, r.Database.Password = login.Username, login.Password
	return r, nil
}

// inCI signs in with a login the workflow hands over: CI has a throwaway database and no Vault.
func inCI(w World) (runner.Runner, error) {
	r, err := base(w, "DATABASE_USERNAME", "DATABASE_PASSWORD")
	if err != nil {
		return runner.Runner{}, err
	}
	r.Database.Username, r.Database.Password = w.Getenv("DATABASE_USERNAME"), w.Getenv("DATABASE_PASSWORD")
	return r, nil
}

func base(w World, alsoRequired ...string) (runner.Runner, error) {
	required := append([]string{"DATABASE_HOST", "DATABASE_PORT", "DATABASE_NAME"}, alsoRequired...)
	var missing []string
	for _, name := range required {
		if w.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return runner.Runner{}, fmt.Errorf("not set: %s", strings.Join(missing, ", "))
	}

	staleAfter := defaultStaleAfter
	if raw := w.Getenv("LOCK_STALE_AFTER"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return runner.Runner{}, fmt.Errorf("LOCK_STALE_AFTER %q is not a positive duration", raw)
		}
		staleAfter = parsed
	}

	return runner.Runner{
		ChangelogDir:  orDefault(w.Getenv("CHANGELOG_DIR"), defaultChangelogDir),
		ChangelogFile: orDefault(w.Getenv("CHANGELOG_FILE"), defaultChangelogFile),
		StaleAfter:    staleAfter,
		Database: runner.Database{
			Host: w.Getenv("DATABASE_HOST"),
			Port: w.Getenv("DATABASE_PORT"),
			Name: w.Getenv("DATABASE_NAME"),
		},
		Open:      w.Open,
		Liquibase: w.Liquibase,
		Log:       w.Stderr,
	}, nil
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func writeProof(args []string, w World) error {
	flags := flag.NewFlagSet("ci proof", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	application := flags.String("application", "", "")
	testedAgainst := flags.String("tested-against", "", "")
	nonTransactional := flags.Bool("non-transactional", false, "")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if flags.NArg() > 0 || *application == "" || !given(flags, "non-transactional") {
		return fmt.Errorf("%w: ci proof takes --application and --non-transactional", errUsage)
	}

	existing, err := io.ReadAll(w.Stdin)
	if err != nil {
		return fmt.Errorf("read the existing proof: %w", err)
	}
	merged, err := proof.Merge(existing, proof.Application{
		ID:               *application,
		TestedAgainst:    *testedAgainst,
		NonTransactional: *nonTransactional,
	})
	if err != nil {
		return err
	}
	_, err = w.Stdout.Write(merged)
	return err
}

// given reports whether a flag was passed: the proof's nonTransactional is never a default.
func given(flags *flag.FlagSet, name string) bool {
	found := false
	flags.Visit(func(f *flag.Flag) { found = found || f.Name == name })
	return found
}

// ReadFile is os.ReadFile, for World.
func ReadFile(name string) ([]byte, error) {
	return os.ReadFile(name) //nolint:gosec // the one fixed path this package reads.
}
