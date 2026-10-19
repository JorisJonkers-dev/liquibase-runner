package changelog_test

import (
	"errors"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/JorisJonkers-dev/liquibase-runner/internal/changelog"
)

func TestALintAllowLineExcusesEachRuleItNames(t *testing.T) {
	rules, err := changelog.LintAllow("Contract step.\nlint-allow ban-drop-column, renaming-column: 3.4 stopped reading legacy_name\n  lint-allow ban-drop-table: gone since 3.2")
	if err != nil || !reflect.DeepEqual(rules, []string{"ban-drop-column", "renaming-column", "ban-drop-table"}) {
		t.Fatalf("LintAllow = %v, %v", rules, err)
	}
}

func TestACommentWithoutALintAllowLineExcusesNothing(t *testing.T) {
	rules, err := changelog.LintAllow("Adds the users table.")
	if err != nil || rules != nil {
		t.Fatalf("LintAllow = %v, %v", rules, err)
	}
}

func TestALintAllowLineNeedsItsRulesAndAReason(t *testing.T) {
	for _, c := range []struct{ name, comment string }{
		{"no reason", "lint-allow ban-drop-column"},
		{"an empty reason", "lint-allow ban-drop-column:   "},
		{"an empty rule", "lint-allow ban-drop-column,: dropped in 3.4"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := changelog.LintAllow(c.comment); !errors.Is(err, changelog.ErrLintAllow) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// rendered is update-sql's output for a release of two changesets, as Liquibase writes it.
const rendered = `-- *********************************************************************
-- Update Database Script
-- *********************************************************************
-- Change Log: liquibase-runner.yaml

-- Lock Database
UPDATE public.databasechangeloglock SET LOCKED = TRUE, LOCKGRANTED = NOW() WHERE ID = 1 AND LOCKED = FALSE;

-- Changeset changelog.yaml::0019-a::identity
-- Adds a column
ALTER TABLE public.users ADD nick TEXT;

INSERT INTO public.databasechangelog (ID, AUTHOR, FILENAME) VALUES ('0019-a', 'identity', 'changelog.yaml');

-- Changeset changelog.yaml::0019-b::identity
CREATE INDEX users_nick ON public.users(nick);

INSERT INTO public.DATABASECHANGELOG (ID) VALUES ('0019-b');

-- Release Database Lock
UPDATE public.databasechangeloglock SET LOCKED = FALSE, LOCKEDBY = NULL WHERE ID = 1;
`

func TestUpdateSQLIsSplitIntoEachChangesetsOwnStatements(t *testing.T) {
	got := changelog.SplitSQL(rendered)
	want := []changelog.SQL{
		{Key: "changelog.yaml::0019-a::identity", SQL: "ALTER TABLE public.users ADD nick TEXT;"},
		{Key: "changelog.yaml::0019-b::identity", SQL: "CREATE INDEX users_nick ON public.users(nick);"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitSQL = %#v", got)
	}
}

func TestAChangesetCarriesItsCommentAndItsKey(t *testing.T) {
	root := fstest.MapFS{"changelog.yaml": {Data: []byte("databaseChangeLog:\n  - changeSet: {id: 7, author: a, comment: \"lint-allow ban-drop-column: gone\"}\n")}}
	sets, err := changelog.Read(root, "changelog.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if sets[0].Comment != "lint-allow ban-drop-column: gone" || sets[0].Key() != "changelog.yaml::7::a" {
		t.Fatalf("changeset %+v, key %q", sets[0], sets[0].Key())
	}
}
