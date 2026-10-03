// Package store reads and repairs Liquibase's own two tables, for what its command line cannot
// answer: which tag rows follow a tag, which changesets ran, and whether its lock is stale.
package store

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/JorisJonkers-dev/liquibase-runner/internal/changelog"
)

const undefinedTable = "42P01"

// ErrNoTag is returned when the tag to roll back to was never written.
var ErrNoTag = errors.New("tag is not in the database")

// Store is one connection to the project database.
type Store struct {
	conn *pgx.Conn
}

// Open connects to the database as the given login.
func Open(ctx context.Context, host, port, name, username, password string) (*Store, error) {
	dsn := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(username, password),
		Host:   net.JoinHostPort(host, port),
		Path:   "/" + name,
	}
	conn, err := pgx.Connect(ctx, dsn.String())
	if err != nil {
		return nil, fmt.Errorf("connect to %s on %s: %w", name, dsn.Host, err)
	}
	return &Store{conn: conn}, nil
}

// Close ends the connection.
func (s *Store) Close(ctx context.Context) error {
	return s.conn.Close(ctx)
}

// ReleaseStaleLock clears Liquibase's lock when it was granted longer ago than after. A migration
// Job runs once and is bounded by the platform's deadline, so a lock older than that belongs to a
// run that was killed, and would otherwise block every later migration until someone cleared it
// by hand. It reports whether it released one.
func (s *Store) ReleaseStaleLock(ctx context.Context, after time.Duration) (bool, error) {
	tag, err := s.conn.Exec(ctx, `
		UPDATE databasechangeloglock
		   SET locked = FALSE, lockgranted = NULL, lockedby = NULL
		 WHERE locked AND lockgranted < (now() AT TIME ZONE 'utc') - make_interval(secs => $1)`,
		after.Seconds())
	if isUndefinedTable(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("release a stale lock: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// TagsAfter returns the runner's tag changesets written after the row tagged tag, oldest first.
func (s *Store) TagsAfter(ctx context.Context, tag string) ([]string, error) {
	var at int
	err := s.conn.QueryRow(ctx,
		`SELECT orderexecuted FROM databasechangelog WHERE tag = $1 ORDER BY orderexecuted DESC LIMIT 1`,
		tag).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) || isUndefinedTable(err) {
		return nil, fmt.Errorf("%w: %s", ErrNoTag, tag)
	}
	if err != nil {
		return nil, fmt.Errorf("find tag %s: %w", tag, err)
	}

	rows, err := s.conn.Query(ctx, `
		SELECT id FROM databasechangelog
		 WHERE author = $1 AND filename = $2 AND orderexecuted > $3
		 ORDER BY orderexecuted`,
		changelog.Author, changelog.WrapperFile, at)
	if err != nil {
		return nil, fmt.Errorf("list the tags after %s: %w", tag, err)
	}
	tags, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("list the tags after %s: %w", tag, err)
	}
	return tags, nil
}

// Ran reports, for each changeset, whether the database already holds it.
func (s *Store) Ran(ctx context.Context) (func(changelog.ChangeSet) bool, error) {
	rows, err := s.conn.Query(ctx, `SELECT id, author, filename FROM databasechangelog`)
	if isUndefinedTable(err) {
		return func(changelog.ChangeSet) bool { return false }, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list the changesets that ran: %w", err)
	}
	type key struct{ id, author, file string }
	keys, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (key, error) {
		var k key
		return k, row.Scan(&k.id, &k.author, &k.file)
	})
	if isUndefinedTable(err) {
		return func(changelog.ChangeSet) bool { return false }, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list the changesets that ran: %w", err)
	}
	ran := make(map[key]bool, len(keys))
	for _, k := range keys {
		ran[k] = true
	}
	return func(c changelog.ChangeSet) bool { return ran[key{c.ID, c.Author, c.File}] }, nil
}

func isUndefinedTable(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == undefinedTable
}
