// Package runner is the two commands the platform's migration runner takes, `up <tag>` and
// `down <tag>`, and the checks an application's CI runs against the same changelog.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/JorisJonkers-dev/liquibase-runner/internal/changelog"
)

// Database is where the project database is reached, and as whom.
type Database struct {
	Host     string
	Port     string
	Name     string
	Username string
	Password string
}

// URL is the JDBC address Liquibase connects to.
func (d Database) URL() string {
	return fmt.Sprintf("jdbc:postgresql://%s:%s/%s", d.Host, d.Port, d.Name)
}

// Store is what the runner asks the database directly.
type Store interface {
	ReleaseStaleLock(ctx context.Context, after time.Duration) (bool, error)
	TagsAfter(ctx context.Context, tag string) ([]string, error)
	Ran(ctx context.Context) (func(changelog.ChangeSet) bool, error)
	Close(ctx context.Context) error
}

// Liquibase runs one Liquibase command against db, resolving changelogs along searchPath.
type Liquibase func(ctx context.Context, db Database, searchPath []string, args ...string) error

// Runner holds what one run needs.
type Runner struct {
	// ChangelogDir is where the migration image holds the application's changelog.
	ChangelogDir string
	// ChangelogFile is the application's root changelog, relative to ChangelogDir. It is the
	// name every changeset is recorded under, so an image never changes it.
	ChangelogFile string
	// StaleAfter is how old a lock is before it is taken to belong to a run that was killed.
	StaleAfter time.Duration

	Database  Database
	Open      func(ctx context.Context, db Database) (Store, error)
	Liquibase Liquibase
	Log       io.Writer
}

// ErrNonTransactionalNotAlone is the third migration-safety obligation, broken.
var ErrNonTransactionalNotAlone = errors.New("a non-transactional changeset must be the only changeset of its release")

// Up applies the changelog, then a changeset of the runner's own that tags the database, so
// every revision has its own row and its own tag even when it changes no schema.
func (r Runner) Up(ctx context.Context, tag string) error {
	if err := changelog.ValidTag(tag); err != nil {
		return err
	}
	return r.with(ctx, func(Store) error {
		return r.liquibase(ctx, []string{tag}, "update")
	})
}

// Down rolls the database back to tag. The tag changesets written after it are named in the
// changelog handed to Liquibase, so their rows leave with the changes they followed: a revision
// that is applied again then tags the state after its changes, not the state before them.
func (r Runner) Down(ctx context.Context, tag string) error {
	if err := changelog.ValidTag(tag); err != nil {
		return err
	}
	return r.with(ctx, func(s Store) error {
		after, err := s.TagsAfter(ctx, tag)
		if err != nil {
			return err
		}
		return r.liquibase(ctx, after, "rollback", "--tag="+tag)
	})
}

// Reversibility applies every changeset the database does not hold, rolls each back and applies
// it again: a changeset with no working rollback fails here, in CI, instead of in a down.
func (r Runner) Reversibility(ctx context.Context) error {
	return r.with(ctx, func(Store) error {
		return r.liquibase(ctx, nil, "update-testing-rollback")
	})
}

// NonTransactional reports whether the release, the changesets the database does not hold yet,
// is one non-transactional changeset. A non-transactional changeset that shares its release is
// refused: its partial failure could not be rolled back, and the others' could have been.
func (r Runner) NonTransactional(ctx context.Context) (bool, error) {
	sets, err := changelog.Read(os.DirFS(r.ChangelogDir), r.ChangelogFile)
	if err != nil {
		return false, err
	}
	var release, nonTransactional int
	err = r.with(ctx, func(s Store) error {
		ran, err := s.Ran(ctx)
		if err != nil {
			return err
		}
		for _, c := range sets {
			// A changeset Liquibase runs again is part of every release, held or not.
			if ran(c) && !c.Reruns {
				continue
			}
			release++
			if c.NonTransactional {
				nonTransactional++
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if nonTransactional > 0 && release > 1 {
		return false, fmt.Errorf("%w: this release holds %d changesets, %d of them non-transactional",
			ErrNonTransactionalNotAlone, release, nonTransactional)
	}
	return nonTransactional == 1, nil
}

// with opens the database, clears a stale lock, and runs do.
func (r Runner) with(ctx context.Context, do func(Store) error) (err error) {
	s, err := r.Open(ctx, r.Database)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close(ctx)) }()

	released, err := s.ReleaseStaleLock(ctx, r.StaleAfter)
	if err != nil {
		return err
	}
	if released {
		_, _ = fmt.Fprintf(r.Log, "released a changelog lock granted more than %s ago\n", r.StaleAfter)
	}
	return do(s)
}

// liquibase writes the runner's changelog beside nothing of the image's, and runs one command
// over it and the application's.
func (r Runner) liquibase(ctx context.Context, tags []string, args ...string) (err error) {
	if _, statErr := fs.Stat(os.DirFS(r.ChangelogDir), r.ChangelogFile); statErr != nil {
		return fmt.Errorf("the image holds no changelog at %s: %w",
			filepath.Join(r.ChangelogDir, r.ChangelogFile), statErr)
	}
	wrapper, err := changelog.Wrapper(r.ChangelogFile, tags)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "liquibase-runner-")
	if err != nil {
		return fmt.Errorf("write the runner's changelog: %w", err)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	if err := os.WriteFile(filepath.Join(dir, changelog.WrapperFile), wrapper, 0o600); err != nil {
		return fmt.Errorf("write the runner's changelog: %w", err)
	}

	args = append([]string{"--changelog-file=" + changelog.WrapperFile}, args...)
	return r.Liquibase(ctx, r.Database, []string{r.ChangelogDir, dir}, args...)
}
