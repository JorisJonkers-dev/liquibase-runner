package runner_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/JorisJonkers-dev/liquibase-runner/internal/runner"
)

const lintChangelog = `databaseChangeLog:
  - changeSet: {id: 1, author: a}
  - changeSet: {id: 2, author: a, comment: "lint-allow ban-drop-column: 3.4 stopped reading nick"}
  - changeSet: {id: 3, author: a, runInTransaction: false}
`

// renderedRelease is update-sql's output for changesets 2 and 3, and one of the runner's own.
const renderedRelease = `-- Lock Database
-- Changeset changelog.yaml::2::a
ALTER TABLE users DROP COLUMN nick;
INSERT INTO databasechangelog VALUES ('2');
-- Changeset changelog.yaml::3::a
CREATE INDEX CONCURRENTLY users_name ON users(name);
-- Changeset liquibase-runner.yaml::aaaaaaaaaaa1::liquibase-runner
-- Changeset changelog.yaml::1::a
`

// squawked is one squawk run: the SQL it read, the rules it left out, whether in a transaction.
type squawked struct {
	sql           string
	exclude       []string
	inTransaction bool
}

// linting is a runner whose Liquibase renders renderedRelease and whose squawk answers findings
// for SQL containing a key of findings.
func linting(t *testing.T, findings map[string]string) (*fixture, *[]squawked) {
	t.Helper()
	f := newFixture(t, lintChangelog)
	liquibase := f.Liquibase
	f.Liquibase = func(ctx context.Context, db runner.Database, searchPath []string, args ...string) error {
		for _, a := range args {
			if out, ok := strings.CutPrefix(a, "--output-file="); ok {
				if err := os.WriteFile(out, []byte(renderedRelease), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
		return liquibase(ctx, db, searchPath, args...)
	}
	var runs []squawked
	f.Squawk = func(_ context.Context, file string, exclude []string, inTransaction bool) (string, error) {
		raw, err := os.ReadFile(file) //nolint:gosec // the file the runner wrote.
		if err != nil {
			t.Fatal(err)
		}
		runs = append(runs, squawked{string(raw), exclude, inTransaction})
		for match, finding := range findings {
			if strings.Contains(string(raw), match) {
				return finding, nil
			}
		}
		return "", nil
	}
	return f, &runs
}

func TestLintRendersTheReleaseWithUpdateSQL(t *testing.T) {
	f, _ := linting(t, nil)
	if err := f.Lint(context.Background()); err != nil {
		t.Fatal(err)
	}
	args := f.calls[0].args
	if len(args) != 3 || args[0] != "--changelog-file=liquibase-runner.yaml" || !strings.HasPrefix(args[1], "--output-file=") || args[2] != "update-sql" {
		t.Fatalf("arguments %v", args)
	}
}

func TestLintChecksEachChangesetOfTheReleaseOnItsOwn(t *testing.T) {
	f, runs := linting(t, nil)
	_ = f.Lint(context.Background())
	if len(*runs) != 2 || (*runs)[0].sql != "ALTER TABLE users DROP COLUMN nick;" || (*runs)[1].sql != "CREATE INDEX CONCURRENTLY users_name ON users(name);" {
		t.Fatalf("squawk read %+v", *runs)
	}
}

func TestLintLeavesOutTheRulesAChangesetAllows(t *testing.T) {
	f, runs := linting(t, nil)
	_ = f.Lint(context.Background())
	if !slices.Contains((*runs)[0].exclude, "ban-drop-column") || slices.Contains((*runs)[1].exclude, "ban-drop-column") {
		t.Fatalf("excluded %v and %v", (*runs)[0].exclude, (*runs)[1].exclude)
	}
}

func TestLintLeavesOutTheRulesTheRunnerItselfMeets(t *testing.T) {
	f, runs := linting(t, nil)
	_ = f.Lint(context.Background())
	for _, rule := range []string{"prefer-robust-stmts", "require-lock-timeout", "require-statement-timeout"} {
		if !slices.Contains((*runs)[1].exclude, rule) {
			t.Errorf("%s is linted", rule)
		}
	}
}

func TestLintReadsANonTransactionalChangesetAsOutsideATransaction(t *testing.T) {
	f, runs := linting(t, nil)
	_ = f.Lint(context.Background())
	if !(*runs)[0].inTransaction || (*runs)[1].inTransaction {
		t.Fatalf("in a transaction: %v and %v", (*runs)[0].inTransaction, (*runs)[1].inTransaction)
	}
}

func TestAReleaseWithAFindingFailsTheLintNamingTheChangeset(t *testing.T) {
	f, _ := linting(t, map[string]string{"CREATE INDEX": "0:0: warning: require-concurrent-index-creation"})
	err := f.Lint(context.Background())
	if !errors.Is(err, runner.ErrUnsafeSQL) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(f.log.String(), "changelog.yaml::3::a\n0:0: warning: require-concurrent-index-creation") {
		t.Fatalf("log %q", f.log.String())
	}
}

func TestAChangesetWithAMalformedLintAllowLineFailsTheLint(t *testing.T) {
	f, _ := linting(t, nil)
	if err := os.WriteFile(f.ChangelogDir+"/changelog.yaml", []byte(strings.Replace(lintChangelog, ": 3.4 stopped reading nick", "", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.Lint(context.Background()); err == nil || !strings.Contains(err.Error(), "changelog.yaml::2::a") {
		t.Fatalf("err = %v", err)
	}
}

func TestTheLintFails(t *testing.T) {
	boom := errors.New("boom")
	for _, c := range []struct {
		name  string
		spoil func(f *fixture)
	}{
		{"when the changelog cannot be read", func(f *fixture) { f.ChangelogFile = "absent.yaml" }},
		{"when Liquibase does", func(f *fixture) { f.db.fail["liquibase"] = boom }},
		{"when squawk does", func(f *fixture) {
			f.Squawk = func(context.Context, string, []string, bool) (string, error) { return "", boom }
		}},
		{"when Liquibase writes no SQL", func(f *fixture) {
			f.Liquibase = func(context.Context, runner.Database, []string, ...string) error { return nil }
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, _ := linting(t, nil)
			c.spoil(f)
			if err := f.Lint(context.Background()); err == nil || errors.Is(err, runner.ErrUnsafeSQL) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
