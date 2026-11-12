package secretchain

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// VaultHTTP reads a KV v2 secret data.value or data for the ref key.
func VaultHTTP(addr, token, path, ref string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(addr), "/")
	mount := strings.Trim(strings.TrimSpace(path), "/")
	if mount == "" {
		mount = "secret/data"
	}
	u, err := url.Parse(base + "/" + mount + "/" + url.PathEscape(ref))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", ErrMissing
	}
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return "", ErrMissing
	}
	req.Header.Set("X-Vault-Token", token)
	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return "", ErrMissing
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil || res.StatusCode != http.StatusOK {
		return "", ErrMissing
	}
	var payload struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", ErrMissing
	}
	if v, ok := payload.Data.Data["value"]; ok {
		return fmt.Sprint(v), nil
	}
	if v, ok := payload.Data.Data[ref]; ok {
		return fmt.Sprint(v), nil
	}
	return "", ErrMissing
}
