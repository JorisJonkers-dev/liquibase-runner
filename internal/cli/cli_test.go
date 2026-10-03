package cli_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JorisJonkers-dev/liquibase-runner/internal/changelog"
	"github.com/JorisJonkers-dev/liquibase-runner/internal/cli"
	"github.com/JorisJonkers-dev/liquibase-runner/internal/runner"
)

const tag = "0123456789ab"

type noStore struct{ tags []string }

func (noStore) ReleaseStaleLock(context.Context, time.Duration) (bool, error) { return false, nil }
func (s noStore) TagsAfter(context.Context, string) ([]string, error)         { return s.tags, nil }
func (noStore) Close(context.Context) error                                   { return nil }
func (noStore) Ran(context.Context) (func(changelog.ChangeSet) bool, error) {
	return func(changelog.ChangeSet) bool { return false }, nil
}

// world is a command line with a changelog on disk, a Vault that knows one role, and a
// Liquibase that records what it was asked.
type world struct {
	cli.World
	env    map[string]string
	out    bytes.Buffer
	errs   bytes.Buffer
	ran    [][]string
	logins []runner.Database
}

func newWorld(t *testing.T) *world {
	t.Helper()
	dir := t.TempDir()
	body := "databaseChangeLog:\n  - changeSet: {id: 1, author: a, runInTransaction: false}\n"
	if err := os.WriteFile(filepath.Join(dir, "changelog.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/kubernetes/login", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"auth":{"client_token":"t"}}`))
	})
	mux.HandleFunc("GET /v1/database/creds/notes-owner", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"username":"v-owner","password":"from-vault"}}`))
	})
	vault := httptest.NewServer(mux)
	t.Cleanup(vault.Close)

	w := &world{env: map[string]string{
		"DATABASE_HOST":          "db",
		"DATABASE_PORT":          "5432",
		"DATABASE_NAME":          "notes_db",
		"VAULT_ADDR":             vault.URL,
		"VAULT_ROLE":             "notes-migration",
		"VAULT_CREDENTIALS_PATH": "database/creds/notes-owner",
		"CHANGELOG_DIR":          dir,
	}}
	w.World = cli.World{
		Getenv: func(name string) string { return w.env[name] },
		Stdin:  strings.NewReader(""),
		Stdout: &w.out,
		Stderr: &w.errs,
		ReadFile: func(string) ([]byte, error) {
			return []byte("sa-token\n"), nil
		},
		HTTP: vault.Client(),
		Open: func(_ context.Context, db runner.Database) (runner.Store, error) {
			w.logins = append(w.logins, db)
			return noStore{tags: []string{"ba9876543210"}}, nil
		},
		Liquibase: func(_ context.Context, _ runner.Database, _ []string, args ...string) error {
			w.ran = append(w.ran, args)
			return nil
		},
	}
	return w
}

func (w *world) run(args ...string) int {
	return cli.Run(context.Background(), args, w.World)
}

func TestUpAndDownSignInThroughVault(t *testing.T) {
	w := newWorld(t)
	if code := w.run("up", tag); code != 0 {
		t.Fatalf("up exited %d: %s", code, w.errs.String())
	}
	if code := w.run("down", tag); code != 0 {
		t.Fatalf("down exited %d: %s", code, w.errs.String())
	}
	if len(w.ran) != 2 || w.ran[0][1] != "update" || w.ran[1][1] != "rollback" || w.ran[1][2] != "--tag="+tag {
		t.Fatalf("liquibase ran %v", w.ran)
	}
	for _, login := range w.logins {
		if login.Username != "v-owner" || login.Password != "from-vault" {
			t.Fatalf("signed in as %+v, not as the credential Vault gave", login)
		}
	}
}

func TestCIVerbsSignInWithTheLoginTheWorkflowHands(t *testing.T) {
	w := newWorld(t)
	w.env["DATABASE_USERNAME"], w.env["DATABASE_PASSWORD"] = "ci", "ci-pw"
	delete(w.env, "VAULT_ADDR")

	for _, args := range [][]string{{"ci", "up", tag}, {"ci", "reversibility"}, {"ci", "non-transactional"}} {
		if code := w.run(args...); code != 0 {
			t.Fatalf("%v exited %d: %s", args, code, w.errs.String())
		}
	}
	if len(w.ran) != 2 || w.ran[0][1] != "update" || w.ran[1][1] != "update-testing-rollback" {
		t.Fatalf("liquibase ran %v", w.ran)
	}
	if w.out.String() != "true\n" {
		t.Fatalf("non-transactional printed %q", w.out.String())
	}
	for _, login := range w.logins {
		if login.Username != "ci" || login.Password != "ci-pw" {
			t.Fatalf("signed in as %+v", login)
		}
	}
}

func TestCIProofMergesStdinToStdout(t *testing.T) {
	w := newWorld(t)
	w.Stdin = strings.NewReader("apiVersion: proof.jorisjonkers.dev/v1\nkind: MigrationProof\nschemaVersion: 1.0.0\napplications:\n  - id: notes\n    nonTransactional: false\n")
	revision := "sha256:" + strings.Repeat("ab", 32)

	if code := w.run("ci", "proof", "--application", "auth", "--tested-against", revision, "--non-transactional=true"); code != 0 {
		t.Fatalf("exited %d: %s", code, w.errs.String())
	}
	for _, want := range []string{"- id: auth", `testedAgainst: "` + revision + `"`, "nonTransactional: true", "- id: notes"} {
		if !strings.Contains(w.out.String(), want) {
			t.Errorf("proof lacks %q:\n%s", want, w.out.String())
		}
	}
}

func TestACallTheRunnerCannotReadIsUsage(t *testing.T) {
	calls := [][]string{
		{},
		{"migrate"},
		{"up"},
		{"up", tag, "extra"},
		{"down"},
		{"ci"},
		{"ci", "deploy"},
		{"ci", "up"},
		{"ci", "proof"},
		{"ci", "proof", "--application", "auth"},
		{"ci", "proof", "--non-transactional=false"},
		{"ci", "proof", "--application", "auth", "--non-transactional=false", "extra"},
		{"ci", "proof", "--unknown"},
	}
	for _, args := range calls {
		w := newWorld(t)
		w.env["DATABASE_USERNAME"], w.env["DATABASE_PASSWORD"] = "ci", "ci-pw"
		if code := w.run(args...); code != 2 {
			t.Errorf("%v exited %d, want 2", args, code)
		}
		if !strings.Contains(w.errs.String(), "usage:") || len(w.ran) != 0 {
			t.Errorf("%v: stderr %q, liquibase %v", args, w.errs.String(), w.ran)
		}
	}
}

func TestAFailedRunExitsOneAndSaysWhy(t *testing.T) {
	cases := map[string]struct {
		args  []string
		setup func(*world)
		want  string
	}{
		"a variable the render did not set": {
			[]string{"up", tag},
			func(w *world) { delete(w.env, "DATABASE_NAME"); delete(w.env, "VAULT_ROLE") },
			"not set: DATABASE_NAME, VAULT_ROLE",
		},
		"no login in CI": {
			[]string{"ci", "reversibility"},
			func(*world) {},
			"not set: DATABASE_USERNAME, DATABASE_PASSWORD",
		},
		"no ServiceAccount token": {
			[]string{"up", tag},
			func(w *world) { w.ReadFile = func(string) ([]byte, error) { return nil, os.ErrNotExist } },
			"read the ServiceAccount token",
		},
		"a Vault that refuses": {
			[]string{"down", tag},
			func(w *world) { w.env["VAULT_CREDENTIALS_PATH"] = "database/creds/other" },
			"vault read database/creds/other",
		},
		"a lock bound that is not a duration": {
			[]string{"up", tag},
			func(w *world) { w.env["LOCK_STALE_AFTER"] = "soon" },
			`LOCK_STALE_AFTER "soon" is not a positive duration`,
		},
		"a tag that is not a revision": {
			[]string{"up", "v1"},
			func(*world) {},
			`tag "v1" is not 12 lowercase hex digits`,
		},
		"liquibase failing": {
			[]string{"up", tag},
			func(w *world) {
				w.Liquibase = func(context.Context, runner.Database, []string, ...string) error {
					return errors.New("liquibase update: exit status 1")
				}
			},
			"liquibase update: exit status 1",
		},
		"a proof entry that is not one": {
			[]string{"ci", "proof", "--application", "auth", "--tested-against", "latest", "--non-transactional=false"},
			func(*world) {},
			"not a sha256 revision",
		},
		"a non-transactional changeset that is not alone": {
			[]string{"ci", "non-transactional"},
			func(w *world) {
				w.env["DATABASE_USERNAME"], w.env["DATABASE_PASSWORD"] = "ci", "ci-pw"
				body := "databaseChangeLog:\n  - changeSet: {id: 1, author: a, runInTransaction: false}\n  - changeSet: {id: 2, author: a}\n"
				if err := os.WriteFile(filepath.Join(w.env["CHANGELOG_DIR"], "changelog.yaml"), []byte(body), 0o600); err != nil {
					panic(err)
				}
			},
			"must be the only changeset of its release",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			c.setup(w)
			if code := w.run(c.args...); code != 1 {
				t.Fatalf("exited %d, want 1: %s", code, w.errs.String())
			}
			if !strings.Contains(w.errs.String(), c.want) {
				t.Fatalf("stderr %q, want it to name %q", w.errs.String(), c.want)
			}
		})
	}
}

func TestTheImagesOwnDefaultsAndALockBound(t *testing.T) {
	w := newWorld(t)
	delete(w.env, "CHANGELOG_DIR")
	w.env["LOCK_STALE_AFTER"] = "45m"
	if code := w.run("up", tag); code != 1 || !strings.Contains(w.errs.String(), "/liquibase/changelog/changelog.yaml") {
		t.Fatalf("exited %d: %s", code, w.errs.String())
	}
	if _, err := cli.ReadFile(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("an absent file was read")
	}
}
