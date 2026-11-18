package main

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

// run() reads the real environment, so its guard runs on t.Setenv values. The
// refusal lands in LoadSandboxd, before any driver or listener opens. An
// empty UPMO_ENV is production, like an unset one.
func TestRunRefusesDevelopmentServiceKeyInProduction(t *testing.T) {
	t.Setenv("UPMO_ENV", "")
	t.Setenv("UPMO_SERVICE_KEY", "upmo_svc_"+base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{'g'}, 32)))
	err := run()
	if err == nil || !strings.Contains(err.Error(), "UPMO_SERVICE_KEY uses a development or weak key") {
		t.Fatalf("run err = %v", err)
	}
	if strings.Contains(err.Error(), "upmo_svc_") {
		t.Fatalf("run err holds the key: %v", err)
	}
}
