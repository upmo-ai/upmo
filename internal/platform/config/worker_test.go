package config

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/testutil/pgtls"
)

func workerKey(audience string, b byte) string {
	return "upmo_svc_" + audience + "_" + base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat(string(rune(b)), 32)))
}

var (
	workerBrainKey    = workerKey("worker-brain", 'b')
	workerGatewayKey  = workerKey("worker-gateway", 'g')
	workerSandboxdKey = workerKey("worker-sandboxd", 's')
)

// One process's base environment, without any worker variable.
func processEnv(t *testing.T, process string) map[string]string {
	t.Helper()
	switch process {
	case "brain":
		return map[string]string{
			"UPMO_DATABASE_APP_URL":    pgtls.URL(t, "upmo_app"),
			"UPMO_DATABASE_RUNNER_URL": pgtls.URL(t, "upmo_runner"),
			"UPMO_PUBLIC_URL":          "http://localhost:5173",
		}
	case "gateway":
		return map[string]string{
			"UPMO_DATABASE_GATEWAY_URL": pgtls.URL(t, "upmo_gateway"),
			"UPMO_PUBLIC_URL":           "http://localhost:5173",
			"UPMO_GATEWAY_KEY":          gwKey,
		}
	case "sandboxd":
		return map[string]string{"UPMO_SANDBOXD_KEY": sbxKey, "UPMO_EDITOR_KEY": edKey}
	}
	return map[string]string{
		"UPMO_DATABASE_URL": pgtls.URL(t, "upmo"),
		"UPMO_PUBLIC_URL":   "http://localhost:5173",
	}
}

func loadProcess(process string, env map[string]string) (worker string, keys [6]string, err error) {
	lookup := lookupFrom(env)
	switch process {
	case "sandboxd":
		cfg, err := loadSandboxd(lookup)
		return cfg.WorkerURL, [6]string{4: cfg.WorkerSandboxdKey, 5: cfg.WorkerSandboxdKeyPrevious}, err
	case "brain":
		cfg, err := loadBrain(lookup)
		return cfg.WorkerURL, workerFields(cfg), err
	case "gateway":
		cfg, err := loadGateway(lookup)
		return cfg.WorkerURL, workerFields(cfg), err
	}
	cfg, err := load(lookup)
	return cfg.WorkerURL, workerFields(cfg), err
}

func workerFields(cfg Config) [6]string {
	return [6]string{cfg.WorkerBrainKey, cfg.WorkerBrainKeyPrevious, cfg.WorkerGatewayKey, cfg.WorkerGatewayKeyPrevious, cfg.WorkerSandboxdKey, cfg.WorkerSandboxdKeyPrevious}
}

// Invariant 3: each caller holds only its own worker key, and exits when it
// is given another, even empty.
func TestWorkerKeysEachCallerHoldsOnlyItsOwn(t *testing.T) {
	own := map[string]struct {
		name, value string
		slot        int
	}{
		"brain":    {"UPMO_WORKER_BRAIN_KEY", workerBrainKey, 0},
		"gateway":  {"UPMO_WORKER_GATEWAY_KEY", workerGatewayKey, 2},
		"sandboxd": {"UPMO_WORKER_SANDBOXD_KEY", workerSandboxdKey, 4},
	}
	all := map[string]string{
		"UPMO_WORKER_BRAIN_KEY":    workerBrainKey,
		"UPMO_WORKER_GATEWAY_KEY":  workerGatewayKey,
		"UPMO_WORKER_SANDBOXD_KEY": workerSandboxdKey,
	}
	for process, mine := range own {
		env := processEnv(t, process)
		env["UPMO_WORKER_URL"] = "https://worker.example.com"
		env[mine.name] = mine.value
		url, keys, err := loadProcess(process, env)
		if err != nil || url != "https://worker.example.com" || keys[mine.slot] != mine.value {
			t.Fatalf("%s with its own key = %q %v %v", process, url, keys, err)
		}
		for name, value := range all {
			if name == mine.name {
				continue
			}
			for _, set := range []struct{ name, value string }{{name, value}, {name, ""}, {name + "_PREVIOUS", value}} {
				env := processEnv(t, process)
				env[set.name] = set.value
				want := "config: " + set.name + " must not be set for " + process
				if _, _, err := loadProcess(process, env); err == nil || err.Error() != want {
					t.Errorf("%s with %s=%q = %v, want %q", process, set.name, set.value, err, want)
				}
			}
		}
		// A key of another audience under its own name is refused too.
		env = processEnv(t, process)
		other := workerGatewayKey
		if process == "gateway" {
			other = workerBrainKey
		}
		env[mine.name] = other
		if _, _, err := loadProcess(process, env); err == nil || !strings.Contains(err.Error(), "config: "+mine.name+": svcsig: key is not a ") {
			t.Errorf("%s with a foreign audience key = %v", process, err)
		}
	}
	// The worker reads all three.
	env := processEnv(t, "worker")
	for name, value := range all {
		env[name] = value
	}
	if _, keys, err := loadProcess("worker", env); err != nil || keys[0] != workerBrainKey || keys[2] != workerGatewayKey || keys[4] != workerSandboxdKey {
		t.Fatalf("worker keys = %v %v", keys, err)
	}
}

// Invariant 11: UPMO_WORKER_URL is https in production; development allows
// http on loopback and one DNS label.
func TestWorkerURLMustBeHTTPSInProduction(t *testing.T) {
	for _, tc := range []struct {
		env, url, want string
	}{
		{"production", "http://worker:8090", "config: UPMO_WORKER_URL must be https"},
		{"production", "http://worker.example.com", "config: UPMO_WORKER_URL must be https"},
		{"production", "http://127.0.0.1:8090", "config: UPMO_WORKER_URL must be https"},
		{"production", "https://worker.example.com", ""},
		{"development", "http://worker:8090", ""},
		{"development", "http://127.0.0.1:8090", ""},
		{"development", "http://worker.example.com", "config: UPMO_WORKER_URL must be https"},
		{"production", "https://worker.example.com/keys", "config: UPMO_WORKER_URL must be an absolute https URL with no path"},
	} {
		for _, process := range []string{"brain", "gateway", "sandboxd"} {
			env := processEnv(t, process)
			env["UPMO_ENV"] = tc.env
			env["UPMO_WORKER_URL"] = tc.url
			env[map[string]string{"brain": "UPMO_WORKER_BRAIN_KEY", "gateway": "UPMO_WORKER_GATEWAY_KEY", "sandboxd": "UPMO_WORKER_SANDBOXD_KEY"}[process]] =
				map[string]string{"brain": workerBrainKey, "gateway": workerGatewayKey, "sandboxd": workerSandboxdKey}[process]
			_, _, err := loadProcess(process, env)
			if (tc.want == "" && err != nil) || (tc.want != "" && (err == nil || err.Error() != tc.want)) {
				t.Errorf("%s %s %s = %v, want %q", process, tc.env, tc.url, err, tc.want)
			}
		}
	}
	env := processEnv(t, "gateway")
	env["UPMO_WORKER_URL"] = "https://worker.example.com"
	if _, _, err := loadProcess("gateway", env); err == nil || err.Error() != "config: UPMO_WORKER_GATEWAY_KEY is required when UPMO_WORKER_URL is set" {
		t.Fatalf("gateway url without its key = %v", err)
	}
}

// Invariant 2, the refusal itself: a master key current or previous, even
// empty, stops gateway and sandboxd.
func TestRefuseMasterKey(t *testing.T) {
	for _, tc := range []struct {
		env     map[string]string
		environ []string
	}{
		{map[string]string{"UPMO_MASTER_KEY": "x"}, nil},
		{map[string]string{"UPMO_MASTER_KEY": ""}, nil},
		{nil, []string{"UPMO_MASTER_KEY_PREVIOUS_k0=x"}},
		{nil, []string{"UPMO_MASTER_KEY_PREVIOUS_k0="}},
	} {
		if err := RefuseMasterKey(lookupFrom(tc.env), tc.environ); err == nil || err.Error() != "config: UPMO_MASTER_KEY is refused" {
			t.Errorf("RefuseMasterKey(%v, %v) = %v", tc.env, tc.environ, err)
		}
	}
	if err := RefuseMasterKey(lookupFrom(map[string]string{"UPMO_MASTER_KEY_ID": "k1"}), []string{"UPMO_GATEWAY_KEY=x"}); err != nil {
		t.Fatalf("no master key = %v", err)
	}
}
