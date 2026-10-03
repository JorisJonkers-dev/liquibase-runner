// Package vault reads the migration identity's database credential the way a `delivery: self`
// Process does: the pod presents its own ServiceAccount token to Vault.
package vault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Credential is one database login.
type Credential struct {
	Username string
	Password string
}

// Client reads from one Vault.
type Client struct {
	Addr string
	HTTP *http.Client
}

// Credential logs in to the Kubernetes auth method as role with the ServiceAccount token jwt,
// then reads the credential at path.
func (c Client) Credential(ctx context.Context, role, jwt, path string) (Credential, error) {
	var login struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	body, err := json.Marshal(map[string]string{"role": role, "jwt": jwt})
	if err != nil {
		return Credential{}, fmt.Errorf("vault login: %w", err)
	}
	if err := c.do(ctx, http.MethodPost, "auth/kubernetes/login", "", body, &login); err != nil {
		return Credential{}, fmt.Errorf("vault login as %s: %w", role, err)
	}
	if login.Auth.ClientToken == "" {
		return Credential{}, fmt.Errorf("vault login as %s: no token in the response", role)
	}

	var secret struct {
		Data Credential `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, path, login.Auth.ClientToken, nil, &secret); err != nil {
		return Credential{}, fmt.Errorf("vault read %s: %w", path, err)
	}
	if secret.Data.Username == "" || secret.Data.Password == "" {
		return Credential{}, fmt.Errorf("vault read %s: no username and password in the response", path)
	}
	return secret.Data, nil
}

// UnmarshalJSON reads the two fields a database credential carries.
func (c *Credential) UnmarshalJSON(raw []byte) error {
	var fields struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	c.Username, c.Password = fields.Username, fields.Password
	return nil
}

func (c Client) do(ctx context.Context, method, path, token string, body []byte, into any) error {
	url := strings.TrimRight(c.Addr, "/") + "/v1/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return errors.New(res.Status)
	}
	return json.Unmarshal(raw, into)
}
