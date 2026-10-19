package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// pkgSpec describes one version served by the fake registry.
type pkgSpec struct {
	version string
	flagged bool
	require string // JSON object for "require", or ""
}

func registryWith(t *testing.T, pkgs map[string][]pkgSpec) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	jsonOut := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}
	notFound := `{"error":"not_found","message":"no such package or version"}`
	mux.HandleFunc("GET /api/v1/packages/{vendor}/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("vendor") + "/" + r.PathValue("name")
		specs, ok := pkgs[name]
		if !ok {
			jsonOut(w, 404, notFound)
			return
		}
		var vs []string
		for _, s := range specs {
			vs = append(vs, fmt.Sprintf(`{"version":%q,"commit":"%040d","published_at":"x","flagged":%v}`, s.version, 0, s.flagged))
		}
		jsonOut(w, 200, fmt.Sprintf(`{"name":%q,"versions":[%s]}`, name, strings.Join(vs, ",")))
	})
	mux.HandleFunc("GET /api/v1/packages/{vendor}/{name}/versions/{v}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("vendor") + "/" + r.PathValue("name")
		for _, s := range pkgs[name] {
			if s.version == r.PathValue("v") {
				req := ""
				if s.require != "" {
					req = `,"require":` + s.require
				}
				jsonOut(w, 200, fmt.Sprintf(`{"name":%q,"version":%q,"commit":"%040d","published_at":"x","flagged":false,"manifest":{"name":%q%s}}`,
					name, s.version, 0, name, req))
				return
			}
		}
		jsonOut(w, 404, notFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestResolveTransitive(t *testing.T) {
	srv := registryWith(t, map[string][]pkgSpec{
		"gabime/spdlog": {{version: "1.14.1", require: `{"fmtlib/fmt":{"version":"^11.0","targets":["fmt"]}}`}},
		"fmtlib/fmt":    {{version: "11.0.1"}, {version: "11.0.2"}, {version: "11.1.0-rc.1"}, {version: "12.0.0"}},
	})
	code, out, errs := runCLI("resolve", "gabime/spdlog", "-registry", srv.URL)
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	fmtAt, spdAt := strings.Index(out, "fmtlib/fmt"), strings.Index(out, "gabime/spdlog")
	if fmtAt < 0 || spdAt < 0 || fmtAt > spdAt {
		t.Errorf("dependencies must be listed first:\n%s", out)
	}
	if !strings.Contains(out, "11.0.2") || strings.Contains(out, "12.0.0") || strings.Contains(out, "rc.1") {
		t.Errorf("wrong fmt version (^11.0, no prerelease):\n%s", out)
	}

	code, out, _ = runCLI("resolve", "gabime/spdlog:^1.14", "fmtlib/fmt:>=11.0.1 <11.0.2", "-json", "-registry", srv.URL)
	var res []struct{ Name, Version string }
	if code != 0 || json.Unmarshal([]byte(out), &res) != nil || len(res) != 2 || res[0].Name != "fmtlib/fmt" || res[0].Version != "11.0.1" {
		t.Errorf("root constraint on a transitive dependency: %d %s", code, out)
	}
}

func TestResolveConflictExitCode(t *testing.T) {
	srv := registryWith(t, map[string][]pkgSpec{
		"a/a": {{version: "1.0.0", require: `{"c/c":"^20.0"}`}},
		"b/b": {{version: "1.0.0", require: `{"c/c":"^25.0"}`}},
		"c/c": {{version: "20.0.0"}},
	})
	code, _, errs := runCLI("resolve", "a/a", "b/b", "-registry", srv.URL)
	if code != exitResolution {
		t.Errorf("exit = %d", code)
	}
	for _, want := range []string{"a/a@1.0.0 requires c/c ^20.0", "b/b@1.0.0 requires c/c ^25.0", "available versions: 20.0.0"} {
		if !strings.Contains(errs, want) {
			t.Errorf("missing %q in:\n%s", want, errs)
		}
	}
}

func TestResolveSkipsFlaggedAndPlatformDeps(t *testing.T) {
	srv := registryWith(t, map[string][]pkgSpec{
		"app/app":  {{version: "1.0.0", require: `{"lib/lib":"*","win/only":{"version":"*","platforms":["windows"]},"opt/opt":{"version":"*","optional":true}}`}},
		"lib/lib":  {{version: "1.0.0"}, {version: "2.0.0", flagged: true}},
		"win/only": {{version: "1.0.0"}},
		"opt/opt":  {{version: "1.0.0"}},
	})
	_, out, _ := runCLI("resolve", "app/app", "-s", "os=linux", "-registry", srv.URL)
	if !strings.Contains(out, "lib/lib  1.0.0") || strings.Contains(out, "2.0.0") || strings.Contains(out, "win/only") || strings.Contains(out, "opt/opt") {
		t.Errorf("linux:\n%s", out)
	}
	_, out, _ = runCLI("resolve", "app/app", "-s", "os=windows", "-registry", srv.URL)
	if !strings.Contains(out, "win/only") {
		t.Errorf("windows must include the platform dependency:\n%s", out)
	}
}

func TestResolveErrors(t *testing.T) {
	srv := registryWith(t, map[string][]pkgSpec{"a/a": {{version: "1.0.0", require: `{"ghost/ghost":"*"}`}}})
	if code, _, errs := runCLI("resolve", "a/a", "-registry", srv.URL); code != exitResolution || !strings.Contains(errs, "ghost/ghost does not exist (required by a/a@1.0.0)") {
		t.Errorf("unknown dependency: %d %q", code, errs)
	}
	if code, _, errs := runCLI("resolve", "nope/nope", "-registry", srv.URL); code != exitResolution || !strings.Contains(errs, "nope/nope does not exist (required by root)") {
		t.Errorf("unknown root: %d %q", code, errs)
	}
	if code, _, _ := runCLI("resolve", "-registry", srv.URL); code != exitUsage {
		t.Errorf("no args: %d", code)
	}
	if code, _, _ := runCLI("resolve", "a/a:???", "-registry", srv.URL); code != exitUsage {
		t.Errorf("bad constraint: %d", code)
	}
	srv.Close()
	if code, _, _ := runCLI("resolve", "a/a", "-registry", srv.URL); code != exitNetwork {
		t.Errorf("network: %d", code)
	}
}
