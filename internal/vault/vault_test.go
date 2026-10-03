package vault_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JorisJonkers-dev/liquibase-runner/internal/vault"
)

// fake is a Vault that knows one role, one ServiceAccount token and one credential path.
func fake(t *testing.T, login, secret string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/kubernetes/login", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["role"] != "notes-migration" || body["jwt"] != "sa-token" {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(login))
	})
	mux.HandleFunc("GET /v1/database/creds/notes-owner", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "vault-token" {
			http.Error(w, "permission denied", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(secret))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

const (
	goodLogin  = `{"auth":{"client_token":"vault-token"}}`
	goodSecret = `{"data":{"username":"v-owner","password":"s3cret"}}` //nolint:gosec // the fake Vault's.
)

func TestCredentialLogsInThenReadsTheOwnerCredential(t *testing.T) {
	server := fake(t, goodLogin, goodSecret)
	client := vault.Client{Addr: server.URL + "/", HTTP: server.Client()}

	got, err := client.Credential(context.Background(), "notes-migration", "sa-token", "database/creds/notes-owner")
	if err != nil {
		t.Fatal(err)
	}
	if got != (vault.Credential{Username: "v-owner", Password: "s3cret"}) {
		t.Fatalf("credential %+v", got)
	}
}

func TestCredentialFailsClosed(t *testing.T) {
	cases := map[string]struct {
		login, secret, role, path, want string
	}{
		"a role Vault refuses":         {goodLogin, goodSecret, "other", "database/creds/notes-owner", "vault login as other: 403"},
		"a login with no token":        {`{"auth":{}}`, goodSecret, "notes-migration", "database/creds/notes-owner", "no token in the response"},
		"a path the role cannot read":  {goodLogin, goodSecret, "notes-migration", "database/creds/other", "vault read database/creds/other: 404"},
		"a secret with no password":    {goodLogin, `{"data":{"username":"v-owner"}}`, "notes-migration", "database/creds/notes-owner", "no username and password"},
		"a secret that is not JSON":    {goodLogin, `<html>`, "notes-migration", "database/creds/notes-owner", "vault read database/creds/notes-owner"},
		"a credential of another kind": {goodLogin, `{"data":"text"}`, "notes-migration", "database/creds/notes-owner", "vault read database/creds/notes-owner"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			server := fake(t, c.login, c.secret)
			client := vault.Client{Addr: server.URL, HTTP: server.Client()}
			_, err := client.Credential(context.Background(), c.role, "sa-token", c.path)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v, want one naming %q", err, c.want)
			}
		})
	}
}

func TestCredentialReportsAVaultItCannotReach(t *testing.T) {
	server := fake(t, goodLogin, goodSecret)
	client := vault.Client{Addr: server.URL, HTTP: server.Client()}
	server.Close()

	if _, err := client.Credential(context.Background(), "notes-migration", "sa-token", "database/creds/notes-owner"); err == nil {
		t.Fatal("a closed Vault gave a credential")
	}
	bad := vault.Client{Addr: "http://[::1", HTTP: http.DefaultClient}
	if _, err := bad.Credential(context.Background(), "notes-migration", "sa-token", "x"); err == nil {
		t.Fatal("an address that is not one gave a credential")
	}
}
