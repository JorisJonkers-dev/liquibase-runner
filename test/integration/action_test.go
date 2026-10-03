//go:build integration

package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const serving = "sha256:aaaaaaaaaaa1" + "0000000000000000000000000000000000000000000000000000"

// action runs the Migration Proof action's script the way the composite step does.
func action(t *testing.T, env ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "../../actions/migration-proof/run.sh")
	cmd.Env = append(os.Environ(), "APPLICATION=notes", "DATABASE_NAME=notes_db")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestTheMigrationProofAction(t *testing.T) {
	build(t)
	proof := filepath.Join(t.TempDir(), "deploy", "migration-proof.yml")
	ran := filepath.Join(t.TempDir(), "suite-ran")

	t.Run("a first release proves reversibility and records no serving revision", func(t *testing.T) {
		out, err := action(t, "IMAGE="+image+"-v2", "PROOF_FILE="+proof)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		expectFile(t, proof, `apiVersion: proof.jorisjonkers.dev/v1
kind: MigrationProof
schemaVersion: 1.0.0
applications:
  - id: notes
    nonTransactional: false
`)
	})

	t.Run("a later release runs the serving suite against the migrated database", func(t *testing.T) {
		// The suite stands in for the serving revision's: it reaches the database over the
		// address it was handed, and finds the release's change in it.
		suite := `exec 3<>"/dev/tcp/$DATABASE_HOST/$DATABASE_PORT" && test "$DATABASE_NAME" = notes_db && test -n "$DATABASE_PASSWORD" && touch "` + ran + `"`
		out, err := action(t, "IMAGE="+image+"-v3", "SERVING_IMAGE="+image+"-v2", "TESTED_AGAINST="+serving,
			"TEST_COMMAND="+suite, "PROOF_FILE="+proof)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if _, err := os.Stat(ran); err != nil {
			t.Fatalf("the serving suite did not run: %v\n%s", err, out)
		}
		expectFile(t, proof, `apiVersion: proof.jorisjonkers.dev/v1
kind: MigrationProof
schemaVersion: 1.0.0
applications:
  - id: notes
    testedAgainst: "`+serving+`"
    nonTransactional: true
`)
	})

	t.Run("a serving suite that fails writes no proof", func(t *testing.T) {
		before, err := os.ReadFile(proof)
		if err != nil {
			t.Fatal(err)
		}
		out, err := action(t, "IMAGE="+image+"-v3", "SERVING_IMAGE="+image+"-v2", "TESTED_AGAINST="+serving,
			"TEST_COMMAND=false", "PROOF_FILE="+proof)
		if err == nil {
			t.Fatalf("a failing suite passed:\n%s", out)
		}
		expectFile(t, proof, string(before))
	})

	t.Run("a release the checks refuse writes no proof", func(t *testing.T) {
		refused := filepath.Join(t.TempDir(), "migration-proof.yml")
		out, err := action(t, "IMAGE="+image+"-v3", "PROOF_FILE="+refused)
		if err == nil || !strings.Contains(out, "must be the only changeset of its release") {
			t.Fatalf("error %v\n%s", err, out)
		}
		if _, err := os.Stat(refused); !os.IsNotExist(err) {
			t.Fatalf("a proof was written for a refused release: %v", err)
		}
	})

	t.Run("an image that answers the check with anything but true or false writes no proof", func(t *testing.T) {
		mustDocker(t, "pull", "-q", "alpine:3.22")
		refused := filepath.Join(t.TempDir(), "migration-proof.yml")
		out, err := action(t, "IMAGE=alpine:3.22", "PROOF_FILE="+refused)
		if err == nil {
			t.Fatalf("an image that is no runner was given a proof:\n%s", out)
		}
		if _, err := os.Stat(refused); !os.IsNotExist(err) {
			t.Fatalf("a proof was written: %v", err)
		}
	})

	t.Run("a serving image needs its revision and its suite", func(t *testing.T) {
		for _, env := range [][]string{
			{"SERVING_IMAGE=" + image + "-v2", "TEST_COMMAND=true"},
			{"SERVING_IMAGE=" + image + "-v2", "TESTED_AGAINST=" + serving},
			{"TESTED_AGAINST=" + serving},
		} {
			out, err := action(t, append(env, "IMAGE="+image+"-v3", "PROOF_FILE="+proof)...)
			if err == nil || !strings.Contains(out, "migration-proof:") {
				t.Fatalf("%v was accepted:\n%s", env, out)
			}
		}
	})
}

func expectFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s:\n%s\nwant:\n%s", path, got, want)
	}
}
