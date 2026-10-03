package changelog_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/JorisJonkers-dev/liquibase-runner/internal/changelog"
)

func TestWrapperIncludesTheChangelogThenTagsEachRevision(t *testing.T) {
	got, err := changelog.Wrapper("db/changelog.yaml", []string{"0123456789ab", "ba9876543210"})
	if err != nil {
		t.Fatal(err)
	}
	want := `databaseChangeLog:
    - include:
        file: db/changelog.yaml
    - changeSet:
        author: liquibase-runner
        changes:
            - tagDatabase:
                tag: 0123456789ab
        id: 0123456789ab
    - changeSet:
        author: liquibase-runner
        changes:
            - tagDatabase:
                tag: ba9876543210
        id: ba9876543210
`
	if string(got) != want {
		t.Fatalf("wrapper:\n%s\nwant:\n%s", got, want)
	}
}

func TestWrapperRefusesATagThatIsNotARevision(t *testing.T) {
	for _, tag := range []string{"", "v1", "0123456789AB", "0123456789abc", "0123456789a\n"} {
		if _, err := changelog.Wrapper("changelog.yaml", []string{tag}); err == nil {
			t.Errorf("tag %q was accepted", tag)
		}
	}
}

func TestReadFollowsIncludesInLiquibaseOrder(t *testing.T) {
	root := fstest.MapFS{
		"changelog.yaml": {Data: []byte(`
databaseChangeLog:
  - changeSet: {id: 1, author: a}
  - include: {file: releases/first.yaml}
  - includeAll: {path: more, relativeToChangelogFile: true}
  - changeSet: {id: last, author: a, runInTransaction: false, logicalFilePath: renamed.yaml}
`)},
		"releases/first.yaml": {Data: []byte(`
databaseChangeLog:
  - logicalFilePath: legacy/first.yaml
  - changeSet: {id: "2", author: b, runInTransaction: true}
  - include: {file: sibling.yml, relativeToChangelogFile: true}
`)},
		"releases/sibling.yml": {Data: []byte("databaseChangeLog:\n  - changeSet: {id: 3, author: b}\n")},
		"more/b.yaml":          {Data: []byte("databaseChangeLog:\n  - changeSet: {id: 5, author: c}\n")},
		"more/a.yaml":          {Data: []byte("databaseChangeLog:\n  - changeSet: {id: 4, author: c}\n")},
	}
	got, err := changelog.Read(root, "changelog.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := []changelog.ChangeSet{
		{ID: "1", Author: "a", File: "changelog.yaml"},
		{ID: "2", Author: "b", File: "legacy/first.yaml"},
		{ID: "3", Author: "b", File: "releases/sibling.yml"},
		{ID: "4", Author: "c", File: "more/a.yaml"},
		{ID: "5", Author: "c", File: "more/b.yaml"},
		{ID: "last", Author: "a", File: "renamed.yaml", NonTransactional: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changesets:\n%+v\nwant:\n%+v", got, want)
	}
}

func TestReadRefusesWhatItCannotCheck(t *testing.T) {
	cases := map[string]struct {
		files fstest.MapFS
		want  string
	}{
		"a changelog in another format": {
			files: fstest.MapFS{
				"changelog.yaml": {Data: []byte("databaseChangeLog:\n  - include: {file: legacy.sql}\n")},
				"legacy.sql":     {Data: []byte("--liquibase formatted sql\n")},
			},
			want: "legacy.sql: not a YAML changelog",
		},
		"a changelog that includes itself": {
			files: fstest.MapFS{
				"changelog.yaml": {Data: []byte("databaseChangeLog:\n  - include: {file: changelog.yaml}\n")},
			},
			want: "changelog.yaml includes itself",
		},
		"a missing include": {
			files: fstest.MapFS{
				"changelog.yaml": {Data: []byte("databaseChangeLog:\n  - include: {file: gone.yaml}\n")},
			},
			want: "read changelog",
		},
		"a missing directory": {
			files: fstest.MapFS{
				"changelog.yaml": {Data: []byte("databaseChangeLog:\n  - includeAll: {path: gone}\n")},
			},
			want: "read changelog directory",
		},
		"a file that is not YAML inside": {
			files: fstest.MapFS{"changelog.yaml": {Data: []byte("databaseChangeLog: [")}},
			want:  "changelog.yaml:",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := changelog.Read(c.files, "changelog.yaml")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v, want one naming %q", err, c.want)
			}
		})
	}
	if _, err := changelog.Read(fstest.MapFS{}, "changelog.xml"); !errors.Is(err, changelog.ErrNotYAML) {
		t.Fatalf("error %v, want ErrNotYAML", err)
	}
}

func TestReadReadsEveryKeyOfAnEntryAndMarksWhatRunsAgain(t *testing.T) {
	root := fstest.MapFS{
		"changelog.yaml": {Data: []byte(`
databaseChangeLog:
  - logicalFilePath: legacy.yaml
    changeSet: {id: 1, author: a, runInTransaction: false, runAlways: true}
    include: {file: more.yaml}
  - changeSet: {id: 3, author: a, runOnChange: true}
`)},
		"more.yaml": {Data: []byte("databaseChangeLog:\n  - changeSet: {id: 2, author: a}\n")},
	}
	got, err := changelog.Read(root, "changelog.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := []changelog.ChangeSet{
		{ID: "1", Author: "a", File: "legacy.yaml", NonTransactional: true, Reruns: true},
		{ID: "2", Author: "a", File: "more.yaml"},
		{ID: "3", Author: "a", File: "legacy.yaml", Reruns: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changesets:\n%+v\nwant:\n%+v", got, want)
	}
}

func TestReadRefusesAChangelogLiquibaseCouldReadDifferently(t *testing.T) {
	cases := map[string]string{
		"a changeset key in another case":     "databaseChangeLog:\n  - ChangeSet: {id: 1, author: a}\n",
		"runInTransaction in another case":    "databaseChangeLog:\n  - changeSet: {id: 1, author: a, runintransaction: false}\n",
		"runAlways in another case":           "databaseChangeLog:\n  - changeSet: {id: 1, author: a, RunAlways: true}\n",
		"an include key in another case":      "databaseChangeLog:\n  - include: {File: more.yaml}\n",
		"an includeAll key in another case":   "databaseChangeLog:\n  - includeAll: {Path: more}\n",
		"an entry that is not a mapping":      "databaseChangeLog:\n  - changeSet\n",
		"a changeset that is not a mapping":   "databaseChangeLog:\n  - changeSet: one\n",
		"an include that is not a mapping":    "databaseChangeLog:\n  - include: more.yaml\n",
		"an includeAll that is not a mapping": "databaseChangeLog:\n  - includeAll: more\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := changelog.Read(fstest.MapFS{"changelog.yaml": {Data: []byte(body)}}, "changelog.yaml")
			if !errors.Is(err, changelog.ErrAmbiguous) {
				t.Fatalf("error %v, want ErrAmbiguous", err)
			}
		})
	}

	for name, body := range map[string]string{
		"runInTransaction that is not a boolean":   "databaseChangeLog:\n  - changeSet: {id: 1, author: a, runInTransaction: \"${tx}\"}\n",
		"an include flag that is not a boolean":    "databaseChangeLog:\n  - include: {file: more.yaml, relativeToChangelogFile: maybe}\n",
		"an includeAll flag that is not a boolean": "databaseChangeLog:\n  - includeAll: {path: more, relativeToChangelogFile: maybe}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := changelog.Read(fstest.MapFS{"changelog.yaml": {Data: []byte(body)}}, "changelog.yaml"); err == nil {
				t.Fatal("the changelog was read")
			}
		})
	}
}
