package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/sandboxd/adapters/stub"
)

func serve(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	Handler{Provider: stub.New(nil)}.Mount(mux)
	return mux
}

func post(mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

const createJSON = `{"task_id":"018f3b2c-7a10-7c4a-8e11-111111111111","image":"upmo/sandbox:base","tier":"container","ttl_seconds":600,"allow":["github.com"]}`

func TestCreateThenDestroy(t *testing.T) {
	mux := serve(t)
	rec := post(mux, PathCreate, createJSON)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var box sandboxBody
	if err := json.Unmarshal(rec.Body.Bytes(), &box); err != nil {
		t.Fatal(err)
	}
	if box.ID == "" || box.Tier != "container" || box.Shape.ExecSeconds != 900 || box.Shape.PIDs != 256 {
		t.Fatalf("box = %+v", box)
	}
	if rec := post(mux, PathPause, `{"id":"`+box.ID+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("pause = %d", rec.Code)
	}
	if rec := post(mux, PathDestroy, `{"id":"`+box.ID+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("destroy = %d", rec.Code)
	}
	if rec := post(mux, PathDestroy, `{"id":"`+box.ID+`"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("second destroy = %d", rec.Code)
	}
}

func TestCreateRefuses(t *testing.T) {
	mux := serve(t)
	cases := map[string]struct {
		body string
		code int
	}{
		"not json":     {`{`, http.StatusBadRequest},
		"no ttl":       {strings.Replace(createJSON, `"ttl_seconds":600`, `"ttl_seconds":0`, 1), http.StatusBadRequest},
		"huge ttl":     {strings.Replace(createJSON, `"ttl_seconds":600`, `"ttl_seconds":9223372036854775807`, 1), http.StatusBadRequest},
		"address":      {strings.Replace(createJSON, `"github.com"`, `"169.254.169.254"`, 1), http.StatusBadRequest},
		"gvisor stub":  {strings.Replace(createJSON, `"container"`, `"gvisor"`, 1), http.StatusConflict},
		"unknown tier": {strings.Replace(createJSON, `"container"`, `"none"`, 1), http.StatusBadRequest},
	}
	for name, c := range cases {
		if rec := post(mux, PathCreate, c.body); rec.Code != c.code {
			t.Errorf("%s = %d %s, want %d", name, rec.Code, rec.Body, c.code)
		}
	}
}

func TestUnbuiltRoutesAre501(t *testing.T) {
	mux := serve(t)
	for path, name := range map[string]string{
		PathExec: "Exec", PathPTY: "PTY", PathReadFile: "ReadFile", PathWriteFile: "WriteFile", PathExpose: "Expose",
	} {
		rec := post(mux, path, `{}`)
		if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), name+" is not built yet") {
			t.Errorf("%s = %d %q", path, rec.Code, rec.Body)
		}
	}
}
