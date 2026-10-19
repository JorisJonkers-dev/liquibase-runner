package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/JorisJonkers-dev/liquibase-runner/internal/changelog"
)

// Squawk lints one file of SQL without the rules in exclude, assuming Liquibase wraps it in a
// transaction when inTransaction, and answers its findings, or "" when there are none.
type Squawk func(ctx context.Context, file string, exclude []string, inTransaction bool) (string, error)

// ErrUnsafeSQL is the fourth migration-safety obligation, broken: a changeset of the release
// takes a long lock or destroys data, and does not say it means to.
var ErrUnsafeSQL = errors.New("a changeset of this release takes a long lock or destroys data without a lint-allow line")

// lenient is the rules the lint leaves out for every changeset: Liquibase runs a changeset once
// and records it, the runner sets both timeouts on its connection, and type preferences say
// nothing about locks or lost data.
func lenient() []string {
	return []string{
		"prefer-robust-stmts", "require-lock-timeout", "require-statement-timeout", "require-timeout-settings",
		"prefer-bigint-over-int", "prefer-bigint-over-smallint", "prefer-identity", "prefer-text-field",
		"prefer-timestamptz", "ban-char-field",
	}
}

// Lint renders each changeset of the release, the ones the database does not hold, to SQL with
// Liquibase's update-sql, and lints each with squawk, skipping the rules its comment allows.
func (r Runner) Lint(ctx context.Context) (err error) {
	sets, err := changelog.Read(os.DirFS(r.ChangelogDir), r.ChangelogFile)
	if err != nil {
		return err
	}
	byKey := map[string]changelog.ChangeSet{}
	for _, c := range sets {
		byKey[c.Key()] = c
	}
	dir, err := os.MkdirTemp("", "liquibase-runner-lint-")
	if err != nil {
		return fmt.Errorf("make the lint's directory: %w", err)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()

	rendered := filepath.Join(dir, "release.sql")
	if err := r.with(ctx, func(Store) error {
		return r.liquibase(ctx, nil, "--output-file="+rendered, "update-sql")
	}); err != nil {
		return err
	}
	raw, err := os.ReadFile(rendered) //nolint:gosec // the file the runner just had Liquibase write.
	if err != nil {
		return fmt.Errorf("read the release's SQL: %w", err)
	}

	var unsafe []string
	for i, part := range changelog.SplitSQL(string(raw)) {
		c, ok := byKey[part.Key]
		if !ok || part.SQL == "" {
			continue
		}
		findings, err := r.lintOne(ctx, dir, i, c, part.SQL)
		if err != nil {
			return fmt.Errorf("%s: %w", part.Key, err)
		}
		if findings != "" {
			unsafe = append(unsafe, part.Key+"\n"+findings)
		}
	}
	if len(unsafe) > 0 {
		_, _ = fmt.Fprintln(r.Log, strings.Join(unsafe, "\n"))
		return fmt.Errorf("%w: %d changesets", ErrUnsafeSQL, len(unsafe))
	}
	return nil
}

func (r Runner) lintOne(ctx context.Context, dir string, i int, c changelog.ChangeSet, sql string) (string, error) {
	allowed, err := changelog.LintAllow(c.Comment)
	if err != nil {
		return "", err
	}
	file := filepath.Join(dir, fmt.Sprintf("%04d.sql", i))
	if err := os.WriteFile(file, []byte(sql), 0o600); err != nil {
		return "", fmt.Errorf("write the changeset's SQL: %w", err)
	}
	return r.Squawk(ctx, file, append(lenient(), allowed...), !c.NonTransactional)
}
