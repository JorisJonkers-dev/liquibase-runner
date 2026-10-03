package runner_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JorisJonkers-dev/liquibase-runner/internal/changelog"
	"github.com/JorisJonkers-dev/liquibase-runner/internal/runner"
	"github.com/JorisJonkers-dev/liquibase-runner/internal/store"
)

const (
	first  = "aaaaaaaaaaa1"
	second = "aaaaaaaaaaa2"
	third  = "aaaaaaaaaaa3"
)

// database is a Store that remembers what it was asked.
type database struct {
	staleLock bool
	tagsAfter map[string][]string
	ran       map[string]bool
	fail      map[string]error
	closed    bool
}

func (d *database) ReleaseStaleLock(context.Context, time.Duration) (bool, error) {
	return d.staleLock, d.fail["lock"]
}

func (d *database) TagsAfter(_ context.Context, tag string) ([]string, error) {
	after, ok := d.tagsAfter[tag]
	if !ok {
		return nil, store.ErrNoTag
	}
	return after, nil
}

func (d *database) Ran(context.Context) (func(changelog.ChangeSet) bool, error) {
	return func(c changelog.ChangeSet) bool { return d.ran[c.ID] }, d.fail["ran"]
}

func (d *database) Close(context.Context) error {
	d.closed = true
	return d.fail["close"]
}

// call is one Liquibase command the runner issued, with the changelog it wrote for it.
type call struct {
	db         runner.Database
	searchPath []string
	args       []string
	wrapper    string
}

type fixture struct {
	runner.Runner
	db    *database
	calls []call
	log   bytes.Buffer
}

func newFixture(t *testing.T, changelogYAML string) *fixture {
	t.Helper()
	dir := t.TempDir()
	if changelogYAML != "" {
		if err := os.WriteFile(filepath.Join(dir, "changelog.yaml"), []byte(changelogYAML), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f := &fixture{db: &database{fail: map[string]error{}}}
	f.Runner = runner.Runner{
		ChangelogDir:  dir,
		ChangelogFile: "changelog.yaml",
		StaleAfter:    30 * time.Minute,
		Database:      runner.Database{Host: "db", Port: "5432", Name: "notes_db", Username: "owner", Password: "pw"},
		Open: func(context.Context, runner.Database) (runner.Store, error) {
			return f.db, f.db.fail["open"]
		},
		Liquibase: func(_ context.Context, db runner.Database, searchPath []string, args ...string) error {
			wrapper, err := os.ReadFile(filepath.Join(searchPath[1], changelog.WrapperFile))
			if err != nil {
				t.Fatalf("the runner's changelog was not on the search path: %v", err)
			}
			f.calls = append(f.calls, call{db, searchPath, args, string(wrapper)})
			return f.db.fail["liquibase"]
		},
		Log: &f.log,
	}
	return f
}

const oneChangeSet = "databaseChangeLog:\n  - changeSet: {id: 1, author: a}\n"

func TestUpAppliesTheChangelogThenTagsTheRevision(t *testing.T) {
	f := newFixture(t, oneChangeSet)
	if err := f.Up(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("liquibase ran %d times", len(f.calls))
	}
	got := f.calls[0]
	if want := []string{"--changelog-file=liquibase-runner.yaml", "update"}; !reflect.DeepEqual(got.args, want) {
		t.Errorf("arguments %v, want %v", got.args, want)
	}
	if got.searchPath[0] != f.ChangelogDir {
		t.Errorf("search path %v does not start at the image's changelog", got.searchPath)
	}
	if got.db.URL() != "jdbc:postgresql://db:5432/notes_db" || got.db.Username != "owner" {
		t.Errorf("database %+v", got.db)
	}
	for _, want := range []string{"file: changelog.yaml", "id: " + first, "tag: " + first} {
		if !strings.Contains(got.wrapper, want) {
			t.Errorf("the runner's changelog lacks %q:\n%s", want, got.wrapper)
		}
	}
	if _, err := os.Stat(got.searchPath[1]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the runner's changelog directory was left behind: %v", err)
	}
	if !f.db.closed {
		t.Error("the database connection was left open")
	}
}

func TestDownNamesEveryTagWrittenAfterItsTarget(t *testing.T) {
	f := newFixture(t, oneChangeSet)
	f.db.tagsAfter = map[string][]string{first: {second, third}}

	if err := f.Down(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	got := f.calls[0]
	if want := []string{"--changelog-file=liquibase-runner.yaml", "rollback", "--tag=" + first}; !reflect.DeepEqual(got.args, want) {
		t.Errorf("arguments %v, want %v", got.args, want)
	}
	if strings.Contains(got.wrapper, "id: "+first) {
		t.Errorf("the target's own tag changeset would be rolled back:\n%s", got.wrapper)
	}
	for _, tag := range []string{second, third} {
		if !strings.Contains(got.wrapper, "id: "+tag) {
			t.Errorf("the tag changeset %s would be left behind:\n%s", tag, got.wrapper)
		}
	}
}

func TestDownToATagNeverWrittenRunsNothing(t *testing.T) {
	f := newFixture(t, oneChangeSet)
	err := f.Down(context.Background(), first)
	if !errors.Is(err, store.ErrNoTag) {
		t.Fatalf("error %v, want ErrNoTag", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("liquibase ran: %v", f.calls)
	}
}

func TestATagThatIsNotARevisionReachesNothing(t *testing.T) {
	f := newFixture(t, oneChangeSet)
	opened := false
	f.Open = func(context.Context, runner.Database) (runner.Store, error) {
		opened = true
		return f.db, nil
	}
	for _, run := range []func(context.Context, string) error{f.Up, f.Down} {
		if err := run(context.Background(), "v1.2.3; drop"); err == nil {
			t.Error("a tag that is not a revision was accepted")
		}
	}
	if opened {
		t.Error("the database was opened for a tag that is not a revision")
	}
}

func TestAStaleLockIsReleasedAndSaidSo(t *testing.T) {
	f := newFixture(t, oneChangeSet)
	f.db.staleLock = true
	if err := f.Up(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if want := "released a changelog lock granted more than 30m0s ago\n"; f.log.String() != want {
		t.Fatalf("log %q, want %q", f.log.String(), want)
	}

	quiet := newFixture(t, oneChangeSet)
	if err := quiet.Up(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if quiet.log.Len() != 0 {
		t.Fatalf("log %q for a lock that was not stale", quiet.log.String())
	}
}

func TestReversibilityRunsUpdateTestingRollbackOverTheChangelogAlone(t *testing.T) {
	f := newFixture(t, oneChangeSet)
	if err := f.Reversibility(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := f.calls[0]
	if want := []string{"--changelog-file=liquibase-runner.yaml", "update-testing-rollback"}; !reflect.DeepEqual(got.args, want) {
		t.Errorf("arguments %v, want %v", got.args, want)
	}
	if strings.Contains(got.wrapper, "changeSet") {
		t.Errorf("the check would tag the database:\n%s", got.wrapper)
	}
}

func TestNonTransactional(t *testing.T) {
	const three = `databaseChangeLog:
  - changeSet: {id: 1, author: a}
  - changeSet: {id: 2, author: a, runInTransaction: false}
  - changeSet: {id: 3, author: a}
`
	cases := map[string]struct {
		ran     []string
		want    bool
		refused bool
	}{
		"a release of transactional changesets":      {ran: []string{"1", "2"}, want: false},
		"a release that changes no schema":           {ran: []string{"1", "2", "3"}, want: false},
		"a non-transactional changeset on its own":   {ran: []string{"1", "3"}, want: true},
		"a non-transactional changeset among others": {ran: []string{"1"}, refused: true},
		"a first release holding one":                {ran: nil, refused: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, three)
			f.db.ran = map[string]bool{}
			for _, id := range c.ran {
				f.db.ran[id] = true
			}
			got, err := f.NonTransactional(context.Background())
			if c.refused {
				if !errors.Is(err, runner.ErrNonTransactionalNotAlone) {
					t.Fatalf("error %v, want ErrNonTransactionalNotAlone", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("nonTransactional %v, want %v", got, c.want)
			}
			if len(f.calls) != 0 {
				t.Fatalf("the check ran liquibase: %v", f.calls)
			}
		})
	}
}

func TestAChangesetThatRunsAgainIsPartOfEveryRelease(t *testing.T) {
	const rerun = `databaseChangeLog:
  - changeSet: {id: 1, author: a, runInTransaction: false, runAlways: true}
  - changeSet: {id: 2, author: a}
  - changeSet: {id: 3, author: a}
`
	f := newFixture(t, rerun)
	f.db.ran = map[string]bool{"1": true, "2": true}
	if _, err := f.NonTransactional(context.Background()); !errors.Is(err, runner.ErrNonTransactionalNotAlone) {
		t.Fatalf("error %v: a non-transactional changeset that runs again shared a release and passed", err)
	}

	f.db.ran["3"] = true
	alone, err := f.NonTransactional(context.Background())
	if err != nil || !alone {
		t.Fatalf("nonTransactional %v, error %v, for a release that is only the changeset that runs again", alone, err)
	}
}

func TestEveryFailureIsReturned(t *testing.T) {
	boom := errors.New("boom")
	cases := map[string]struct {
		changelog string
		fail      string
		run       func(*fixture) error
	}{
		"the database cannot be opened": {oneChangeSet, "open", func(f *fixture) error { return f.Up(context.Background(), first) }},
		"the lock cannot be read":       {oneChangeSet, "lock", func(f *fixture) error { return f.Up(context.Background(), first) }},
		"liquibase fails":               {oneChangeSet, "liquibase", func(f *fixture) error { return f.Up(context.Background(), first) }},
		"the connection does not close": {oneChangeSet, "close", func(f *fixture) error { return f.Up(context.Background(), first) }},
		"what ran cannot be read": {oneChangeSet, "ran", func(f *fixture) error {
			_, err := f.NonTransactional(context.Background())
			return err
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, c.changelog)
			f.db.fail[c.fail] = boom
			if err := c.run(f); !errors.Is(err, boom) {
				t.Fatalf("error %v, want the failure", err)
			}
		})
	}

	empty := newFixture(t, "")
	if err := empty.Up(context.Background(), first); err == nil || !strings.Contains(err.Error(), "the image holds no changelog") {
		t.Fatalf("error %v for an image with no changelog", err)
	}
	if _, err := empty.NonTransactional(context.Background()); err == nil {
		t.Fatal("the check passed an image with no changelog")
	}
}
