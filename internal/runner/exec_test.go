package runner_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JorisJonkers-dev/liquibase-runner/internal/runner"
)

// script stands in for the image's Liquibase: it prints what it was given.
func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "liquibase")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil { //nolint:gosec // a test double that must be executable.
		t.Fatal(err)
	}
	return path
}

func TestExecHandsLiquibaseTheLoginInTheEnvironment(t *testing.T) {
	var out, errs bytes.Buffer
	binary := script(t, `echo "args: $*"; echo "url: $LIQUIBASE_COMMAND_URL"; echo "user: $LIQUIBASE_COMMAND_USERNAME"; echo "password: $LIQUIBASE_COMMAND_PASSWORD"`)
	db := runner.Database{Host: "db", Port: "5432", Name: "notes_db", Username: "owner", Password: "s3cret"}

	err := runner.Exec(binary, &out, &errs)(context.Background(), db, []string{"/a", "/b"}, "--changelog-file=x.yaml", "rollback", "--tag=t")
	if err != nil {
		t.Fatal(err)
	}
	want := "args: --search-path=/a,/b --changelog-file=x.yaml rollback --tag=t\n" +
		"url: jdbc:postgresql://db:5432/notes_db\nuser: owner\npassword: s3cret\n"
	if out.String() != want {
		t.Fatalf("liquibase saw:\n%s\nwant:\n%s", out.String(), want)
	}
	if strings.Contains(strings.SplitN(out.String(), "\n", 2)[0], "s3cret") {
		t.Fatal("the password is in the argument list")
	}
}

func TestExecNamesTheCommandThatFailed(t *testing.T) {
	var out, errs bytes.Buffer
	binary := script(t, `echo "no such tag" >&2; exit 3`)

	err := runner.Exec(binary, &out, &errs)(context.Background(), runner.Database{}, nil, "--changelog-file=x.yaml", "rollback")
	if err == nil || !strings.Contains(err.Error(), "liquibase rollback: exit status 3") {
		t.Fatalf("error %v", err)
	}
	if errs.String() != "no such tag\n" {
		t.Fatalf("liquibase's own message was lost: %q", errs.String())
	}

	err = runner.Exec(binary, &out, &errs)(context.Background(), runner.Database{}, nil, "--only-options")
	if err == nil || !strings.Contains(err.Error(), "liquibase : exit status 3") {
		t.Fatalf("error %v", err)
	}
}
