package gateway

import (
	"fmt"
	"os"
	"regexp"
)

var credentialRef = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// EnvSecrets reads UPMO_CREDENTIAL_<ref>. The value is never logged.
type EnvSecrets struct{}

func (EnvSecrets) Get(ref string) (string, error) {
	if !credentialRef.MatchString(ref) {
		return "", fmt.Errorf("gateway: credential_ref %q", ref)
	}
	v, ok := os.LookupEnv("UPMO_CREDENTIAL_" + ref)
	if !ok || v == "" {
		return "", fmt.Errorf("gateway: missing credential")
	}
	return v, nil
}
