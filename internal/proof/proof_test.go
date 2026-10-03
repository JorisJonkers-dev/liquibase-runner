package proof_test

import (
	"strings"
	"testing"

	"github.com/JorisJonkers-dev/liquibase-runner/internal/proof"
)

const revision = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestMergeStartsAProofForAFirstRelease(t *testing.T) {
	got, err := proof.Merge(nil, proof.Application{ID: "auth"})
	if err != nil {
		t.Fatal(err)
	}
	want := `apiVersion: proof.jorisjonkers.dev/v1
kind: MigrationProof
schemaVersion: 1.0.0
applications:
  - id: auth
    nonTransactional: false
`
	if string(got) != want {
		t.Fatalf("proof:\n%s\nwant:\n%s", got, want)
	}
}

func TestMergeReplacesTheApplicationsEntryAndKeepsTheOthersInOrder(t *testing.T) {
	existing := `apiVersion: proof.jorisjonkers.dev/v1
kind: MigrationProof
schemaVersion: 1.0.0
applications:
  - id: notes
    nonTransactional: false
  - id: auth
    testedAgainst: "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
    nonTransactional: false
`
	got, err := proof.Merge([]byte(existing), proof.Application{ID: "auth", TestedAgainst: revision, NonTransactional: true})
	if err != nil {
		t.Fatal(err)
	}
	want := `apiVersion: proof.jorisjonkers.dev/v1
kind: MigrationProof
schemaVersion: 1.0.0
applications:
  - id: auth
    testedAgainst: "` + revision + `"
    nonTransactional: true
  - id: notes
    nonTransactional: false
`
	if string(got) != want {
		t.Fatalf("proof:\n%s\nwant:\n%s", got, want)
	}

	again, err := proof.Merge(got, proof.Application{ID: "auth", TestedAgainst: revision, NonTransactional: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(got) {
		t.Fatalf("the same entry merged twice changed the proof:\n%s", again)
	}
}

func TestMergeRefusesWhatIsNotAProof(t *testing.T) {
	cases := map[string]struct {
		existing string
		entry    proof.Application
		want     string
	}{
		"an entry naming no application":  {"", proof.Application{}, "names no application"},
		"a revision that is not a digest": {"", proof.Application{ID: "auth", TestedAgainst: "v1.2.3"}, "not a sha256 revision"},
		"a document of another kind": {
			"apiVersion: v1\nkind: ConfigMap\nschemaVersion: 1.0.0\napplications: []\n",
			proof.Application{ID: "auth"},
			"not a Migration Proof",
		},
		"a document with a field the proof does not have": {
			"apiVersion: proof.jorisjonkers.dev/v1\nkind: MigrationProof\nextra: true\n",
			proof.Application{ID: "auth"},
			"read the existing proof",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := proof.Merge([]byte(c.existing), c.entry)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v, want one naming %q", err, c.want)
			}
		})
	}
}
