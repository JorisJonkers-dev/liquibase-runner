//go:build integration

// Package integration runs the built image against a real Postgres, with a Vault that knows one
// role: the image is what the platform runs, so the image is what is tested.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var images sync.Once

// build builds the runner image, and one migration image per fixture changelog FROM it.
func build(t *testing.T) {
	t.Helper()
	images.Do(func() {
		mustDocker(t, "build", "-q", "-t", image, "../..")
		for _, version := range []string{"v1", "v2", "v3"} {
			mustDocker(t, "build", "-q", "-t", image+"-"+version, "-f", "testdata/Dockerfile", filepath.Join("testdata", version))
		}
	})
}

const (
	image    = "liquibase-runner:it"
	database = "notes_db"
	password = "integration" //nolint:gosec // a throwaway database's.

	first  = "aaaaaaaaaaa1"
	second = "aaaaaaaaaaa2"
	third  = "aaaaaaaaaaa3"
	fourth = "aaaaaaaaaaa4"
	fifth  = "aaaaaaaaaaa5"
)

// env is one run of the tests: a network, a Postgres on it, a Vault the containers reach
// through the host, and a ServiceAccount token to mount.
type env struct {
	t        *testing.T
	network  string
	postgres string
	vault    string
	token    string
}

func docker(t *testing.T, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return string(out), err
}

func mustDocker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := docker(t, args...)
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func setup(t *testing.T) *env {
	t.Helper()
	suffix := fmt.Sprint(os.Getpid())
	e := &env{t: t, network: "liquibase-runner-it-" + suffix, postgres: "liquibase-runner-it-pg-" + suffix}

	build(t)

	mustDocker(t, "network", "create", e.network)
	t.Cleanup(func() { _, _ = docker(t, "network", "rm", e.network) })
	mustDocker(t, "run", "-d", "--name", e.postgres, "--network", e.network,
		"-e", "POSTGRES_PASSWORD="+password, "-e", "POSTGRES_DB="+database, "postgres:17-alpine")
	t.Cleanup(func() { _, _ = docker(t, "rm", "-f", e.postgres) })
	e.waitForPostgres()

	e.vault = startVault(t)
	e.token = filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(e.token, []byte("sa-token\n"), 0o644); err != nil { //nolint:gosec // the container's user must read it.
		t.Fatal(err)
	}
	return e
}

func (e *env) waitForPostgres() {
	e.t.Helper()
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		// The image starts Postgres twice; only a query over TCP proves the second start.
		if _, err := docker(e.t, "exec", e.postgres, "psql", "-h", "127.0.0.1", "-U", "postgres", "-d", database, "-c", "select 1"); err == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	e.t.Fatal("postgres did not start")
}

// startVault is a Vault that signs in one role with one token, and gives it the owner login.
func startVault(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/kubernetes/login", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["role"] != "notes-migration" || body["jwt"] != "sa-token" {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"auth":{"client_token":"vault-token"}}`))
	})
	mux.HandleFunc("GET /v1/database/creds/notes-owner", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "vault-token" {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		_, _ = fmt.Fprintf(w, `{"data":{"username":"postgres","password":%q}}`, password)
	})
	// Every interface: containers reach it through the host's gateway.
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(mux)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return fmt.Sprintf("http://host.docker.internal:%d", listener.Addr().(*net.TCPAddr).Port)
}

// run runs one command of a migration image the way the render does: a read-only root
// filesystem, a writable /tmp, the ServiceAccount token, and the fixed variables.
func (e *env) run(version string, extra []string, command ...string) (string, error) {
	args := []string{
		"run", "--rm", "--network", e.network, "--add-host", "host.docker.internal:host-gateway",
		"--read-only", "--tmpfs", "/tmp",
		"-v", e.token + ":/var/run/secrets/kubernetes.io/serviceaccount/token:ro",
		"-e", "DATABASE_HOST=" + e.postgres, "-e", "DATABASE_PORT=5432",
	}
	args = append(args, extra...)
	args = append(args, image+"-"+version)
	return docker(e.t, append(args, command...)...)
}

func (e *env) cluster(name, role string) []string {
	return []string{
		"-e", "DATABASE_NAME=" + name, "-e", "VAULT_ADDR=" + e.vault,
		"-e", "VAULT_ROLE=" + role, "-e", "VAULT_CREDENTIALS_PATH=database/creds/notes-owner",
	}
}

func (e *env) ci(name string) []string {
	return []string{"-e", "DATABASE_NAME=" + name, "-e", "DATABASE_USERNAME=postgres", "-e", "DATABASE_PASSWORD=" + password}
}

func (e *env) must(version string, extra []string, command ...string) string {
	e.t.Helper()
	out, err := e.run(version, extra, command...)
	if err != nil {
		e.t.Fatalf("%s %v: %v\n%s", version, command, err, out)
	}
	return out
}

func (e *env) query(name, sql string) string {
	e.t.Helper()
	return strings.TrimSpace(mustDocker(e.t, "exec", e.postgres, "psql", "-U", "postgres", "-d", name, "-At", "-c", sql))
}

func (e *env) expect(name, sql, want string) {
	e.t.Helper()
	if got := e.query(name, sql); got != want {
		e.t.Fatalf("%s\n got: %s\nwant: %s", sql, strings.ReplaceAll(got, "\n", " "), strings.ReplaceAll(want, "\n", " "))
	}
}

const (
	history = `select id || coalesce('=' || tag, '') from databasechangelog order by orderexecuted`
	columns = `select string_agg(column_name, ',' order by ordinal_position) from information_schema.columns where table_name = 'notes'`
)

func TestTheImage(t *testing.T) {
	e := setup(t)
	notes := e.cluster(database, "notes-migration")

	t.Run("up applies the changelog and tags the revision", func(t *testing.T) {
		e.t = t
		e.must("v1", notes, "up", first)
		e.expect(database, history, "create-notes\n"+first+"="+first)
		e.expect(database, columns, "id")
	})

	t.Run("a revision that changes no schema still gets its own row and tag", func(t *testing.T) {
		e.t = t
		e.must("v1", notes, "up", second)
		e.expect(database, history, "create-notes\n"+first+"="+first+"\n"+second+"="+second)
	})

	t.Run("up applies only what the database does not hold", func(t *testing.T) {
		e.t = t
		e.must("v2", notes, "up", third)
		e.expect(database, history, "create-notes\n"+first+"="+first+"\n"+second+"="+second+"\nadd-body\n"+third+"="+third)
		e.expect(database, columns, "id,body")
	})

	t.Run("down rolls back to the tag and takes the later tag with it", func(t *testing.T) {
		e.t = t
		e.must("v2", notes, "down", second)
		e.expect(database, history, "create-notes\n"+first+"="+first+"\n"+second+"="+second)
		e.expect(database, columns, "id")
	})

	t.Run("the revision applied again tags the state after its changes", func(t *testing.T) {
		e.t = t
		e.must("v2", notes, "up", third)
		e.expect(database, history, "create-notes\n"+first+"="+first+"\n"+second+"="+second+"\nadd-body\n"+third+"="+third)
	})

	t.Run("down to a tag never written changes nothing", func(t *testing.T) {
		e.t = t
		out, err := e.run("v2", notes, "down", "ffffffffffff")
		if err == nil || !strings.Contains(out, "tag is not in the database: ffffffffffff") {
			t.Fatalf("error %v\n%s", err, out)
		}
		e.expect(database, columns, "id,body")
	})

	t.Run("a role Vault refuses reaches no database", func(t *testing.T) {
		e.t = t
		out, err := e.run("v2", e.cluster(database, "someone-else"), "up", fourth)
		if err == nil || !strings.Contains(out, "vault login as someone-else: 403") {
			t.Fatalf("error %v\n%s", err, out)
		}
		e.expect(database, `select count(*) from databasechangelog where id = '`+fourth+`'`, "0")
	})

	t.Run("a lock a killed run left behind is released", func(t *testing.T) {
		e.t = t
		e.query(database, `update databasechangeloglock set locked = true, lockedby = 'a pod that is gone', lockgranted = (now() at time zone 'utc') - interval '2 hours'`)
		out := e.must("v2", notes, "up", fourth)
		if !strings.Contains(out, "released a changelog lock granted more than 30m0s ago") {
			t.Fatalf("the release was not reported:\n%s", out)
		}
		e.expect(database, `select tag from databasechangelog order by orderexecuted desc limit 1`, fourth)
	})

	t.Run("a lock a running migration holds is left alone", func(t *testing.T) {
		e.t = t
		e.query(database, `update databasechangeloglock set locked = true, lockedby = 'a running pod', lockgranted = now() at time zone 'utc'`)
		t.Cleanup(func() {
			e.query(database, `update databasechangeloglock set locked = false, lockedby = null, lockgranted = null`)
		})
		out, err := e.run("v2", append(notes, "-e", "LIQUIBASE_CHANGELOG_LOCK_WAIT_TIME_IN_MINUTES=0"), "up", fifth)
		if err == nil {
			t.Fatalf("up ran under another run's lock:\n%s", out)
		}
		e.expect(database, `select locked::text || ' ' || lockedby from databasechangeloglock`, "true a running pod")
	})

	t.Run("ci: every changeset rolls back and applies again", func(t *testing.T) {
		e.t = t
		e.query(database, `create database ci_db`)
		e.must("v2", e.ci("ci_db"), "ci", "reversibility")
		e.expect("ci_db", columns, "id,body")
	})

	t.Run("ci: a release of transactional changesets", func(t *testing.T) {
		e.t = t
		e.query(database, `create database ci_first`)
		if got := strings.TrimSpace(e.must("v2", e.ci("ci_first"), "ci", "non-transactional")); got != "false" {
			t.Fatalf("nonTransactional %q", got)
		}
	})

	t.Run("ci: a non-transactional changeset on its own", func(t *testing.T) {
		e.t = t
		e.must("v2", e.ci("ci_db"), "ci", "up", first)
		if got := strings.TrimSpace(e.must("v3", e.ci("ci_db"), "ci", "non-transactional")); got != "true" {
			t.Fatalf("nonTransactional %q", got)
		}
		e.must("v3", e.ci("ci_db"), "ci", "reversibility")
	})

	t.Run("ci: a non-transactional changeset that shares its release is refused", func(t *testing.T) {
		e.t = t
		out, err := e.run("v3", e.ci("ci_first"), "ci", "non-transactional")
		if err == nil || !strings.Contains(out, "must be the only changeset of its release") {
			t.Fatalf("error %v\n%s", err, out)
		}
	})
}
