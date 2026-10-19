package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const fmtManifest = `{"name":"fmtlib/fmt","description":"A modern formatting library","license":"MIT",
 "require":{"zlib/zlib":{"version":"^1.3","targets":["z"]},"a/b":"1.0.0"},
 "options":{"header_only":{"default":false,"description":"Header only"}},
 "targets":{"fmt":{"type":"library","requires":["zlib/zlib"]},"fmt-header-only":{"type":"header-only"}},
 "build":{"system":"cmake"},"providers":[{"type":"github","tag":"v{version}"}]}`

func fakeRegistry(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	jsonOut := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}
	mux.HandleFunc("GET /api/v1/packages", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "zzz" {
			jsonOut(w, 200, `[]`)
			return
		}
		jsonOut(w, 200, `[{"name":"fmtlib/fmt","description":"A modern formatting library","license":"MIT","latest":"11.0.2","version_count":2}]`)
	})
	mux.HandleFunc("GET /api/v1/packages/fmtlib/fmt", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, 200, `{"name":"fmtlib/fmt","description":"A modern formatting library","license":"MIT","latest":"11.0.2",
		 "versions":[{"version":"11.0.2","commit":"0123456789abcdef0123456789abcdef01234567","published_at":"x","flagged":false},
		             {"version":"11.0.1","commit":"1111111111111111111111111111111111111111","published_at":"x","flagged":true}]}`)
	})
	mux.HandleFunc("GET /api/v1/packages/fmtlib/fmt/versions/{v}", func(w http.ResponseWriter, r *http.Request) {
		v := r.PathValue("v")
		if v != "11.0.2" && v != "11.0.1" {
			jsonOut(w, 404, `{"error":"not_found","message":"no such package or version"}`)
			return
		}
		jsonOut(w, 200, `{"name":"fmtlib/fmt","version":"`+v+`","commit":"0123456789abcdef0123456789abcdef01234567","published_at":"x","flagged":false,"manifest":`+fmtManifest+`}`)
	})
	mux.HandleFunc("GET /api/v1/packages/", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, 404, `{"error":"not_found","message":"no such package or version"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func runCLI(args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = run(args, IO{Out: &out, Err: &errb})
	return code, out.String(), errb.String()
}

func TestSearch(t *testing.T) {
	srv := fakeRegistry(t)
	code, out, _ := runCLI("search", "fmt", "-registry", srv.URL)
	if code != 0 || !strings.Contains(out, "fmtlib/fmt") || !strings.Contains(out, "11.0.2") || !strings.Contains(out, "NAME") {
		t.Errorf("%d %q", code, out)
	}
	// flags may follow the positional argument and may be -json
	code, out, _ = runCLI("search", "-registry", srv.URL, "fmt", "-json")
	var res []map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &res) != nil || len(res) != 1 {
		t.Errorf("json: %d %q", code, out)
	}
	code, out, errs := runCLI("search", "zzz", "-registry", srv.URL)
	if code != 0 || out != "" || !strings.Contains(errs, "no packages found") {
		t.Errorf("empty: %d %q %q", code, out, errs)
	}
}

func TestInfo(t *testing.T) {
	srv := fakeRegistry(t)
	code, out, errs := runCLI("info", "fmtlib/fmt", "-registry", srv.URL)
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	for _, want := range []string{"fmtlib/fmt 11.0.2", "License:   MIT", "11.0.2, 11.0.1", "Build:     cmake", "github (tag v{version})",
		"fmt-header-only", "header-only", "zlib/zlib", "^1.3", "targets: z", "a/b", "header_only", "Binary coverage: not available"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// a constraint picks the newest unflagged match (11.0.1 is flagged and must be skipped)
	if code, out, _ = runCLI("info", "fmtlib/fmt:~11.0", "-registry", srv.URL); code != 0 || !strings.Contains(out, "fmtlib/fmt 11.0.2") {
		t.Errorf("constraint: %d %s", code, out)
	}
	// explicit version, normalized
	if code, out, _ = runCLI("info", "fmtlib/fmt:11.0.1", "-registry", srv.URL); code != 0 || !strings.Contains(out, "fmtlib/fmt 11.0.1") {
		t.Errorf("explicit version: %d %s", code, out)
	}
}

func TestInfoErrors(t *testing.T) {
	srv := fakeRegistry(t)
	if code, _, errs := runCLI("info", "no/such", "-registry", srv.URL); code != exitError || !strings.Contains(errs, "not found") {
		t.Errorf("missing package: %d %q", code, errs)
	}
	if code, _, errs := runCLI("info", "fmtlib/fmt:9.9.9", "-registry", srv.URL); code != exitError || !strings.Contains(errs, "available: 11.0.2, 11.0.1") {
		t.Errorf("missing version: %d %q", code, errs)
	}
	if code, _, errs := runCLI("info", "fmtlib/fmt:^12", "-registry", srv.URL); code != exitError || !strings.Contains(errs, "no version of fmtlib/fmt matches ^12") {
		t.Errorf("unmatched constraint: %d %q", code, errs)
	}
	if code, _, _ := runCLI("info", "fmtlib/fmt:not-a-version", "-registry", srv.URL); code != exitUsage {
		t.Errorf("garbage constraint: %d", code)
	}
	if code, _, _ := runCLI("info", "-registry", srv.URL); code != exitUsage {
		t.Errorf("no args: %d", code)
	}
}

func TestNetworkFailureExitCode(t *testing.T) {
	srv := fakeRegistry(t)
	url := srv.URL
	srv.Close()
	code, _, errs := runCLI("search", "-registry", url)
	if code != exitNetwork || !strings.Contains(errs, "cannot reach registry") {
		t.Errorf("%d %q", code, errs)
	}
}

func TestSettingsOverrides(t *testing.T) {
	// Overrides must make the command succeed even on a machine without a compiler.
	code, out, errs := runCLI("settings", "-json",
		"-s", "compiler.name=gcc", "-s", "compiler.version=13", "-s", "stdlib=libstdc++", "-s", "build_type=debug", "-s", "linkage=shared")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	if m["build_type"] != "debug" || m["linkage"] != "shared" || m["cpp_standard"] != "17" {
		t.Errorf("%v", m)
	}
}

func TestSettingsRejectsInvalid(t *testing.T) {
	base := []string{"settings", "-s", "compiler.name=gcc", "-s", "compiler.version=13", "-s", "stdlib=libstdc++"}
	if code, _, errs := runCLI(append(base, "-s", "build_type=fast")...); code != exitUsage || !strings.Contains(errs, "invalid") {
		t.Errorf("schema violation: %d %q", code, errs)
	}
	if code, _, _ := runCLI(append(base, "-s", "nonsense=1")...); code != exitUsage {
		t.Errorf("unknown key: %d", code)
	}
}

func TestUsage(t *testing.T) {
	if code, _, _ := runCLI(); code != exitUsage {
		t.Errorf("no command: %d", code)
	}
	if code, _, errs := runCLI("frobnicate"); code != exitUsage || !strings.Contains(errs, "unknown command") {
		t.Errorf("unknown: %d %q", code, errs)
	}
	if code, out, _ := runCLI("version"); code != 0 || !strings.HasPrefix(out, "kiln ") {
		t.Errorf("version: %d %q", code, out)
	}
	if code, _, _ := runCLI("search", "-h"); code != 0 {
		t.Errorf("-h should exit 0, got %d", code)
	}
}
