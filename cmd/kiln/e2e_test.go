package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// manifestRegistry serves full manifests: name -> version -> manifest JSON.
type manifestRegistry struct {
	mu      sync.Mutex
	pkgs    map[string]map[string]string
	commits map[string]string // name@version -> commit (default: not pinned)
	srv     *httptest.Server
}

func newManifestRegistry(t *testing.T, pkgs map[string]map[string]string) *manifestRegistry {
	t.Helper()
	r := &manifestRegistry{pkgs: pkgs, commits: map[string]string{}}
	mux := http.NewServeMux()
	out := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}
	notFound := `{"error":"not_found","message":"no such package or version"}`
	mux.HandleFunc("GET /api/v1/packages/{vendor}/{name}", func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		name := req.PathValue("vendor") + "/" + req.PathValue("name")
		vs, ok := r.pkgs[name]
		if !ok {
			out(w, 404, notFound)
			return
		}
		var list []string
		for v := range vs {
			list = append(list, fmt.Sprintf(`{"version":%q,"commit":"%040d","published_at":"x","flagged":false}`, v, 0))
		}
		out(w, 200, fmt.Sprintf(`{"name":%q,"versions":[%s]}`, name, strings.Join(list, ",")))
	})
	mux.HandleFunc("GET /api/v1/packages/{vendor}/{name}/versions/{v}", func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		name := req.PathValue("vendor") + "/" + req.PathValue("name")
		m, ok := r.pkgs[name][req.PathValue("v")]
		if !ok {
			out(w, 404, notFound)
			return
		}
		commit := r.commits[name+"@"+req.PathValue("v")]
		if commit == "" {
			commit = strings.Repeat("0", 40)
		}
		out(w, 200, fmt.Sprintf(`{"name":%q,"version":%q,"commit":%q,"published_at":"x","flagged":false,"manifest":%s}`, name, req.PathValue("v"), commit, m))
	})
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	return r
}

func (r *manifestRegistry) add(name, version, manifest string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pkgs[name] == nil {
		r.pkgs[name] = map[string]string{}
	}
	r.pkgs[name][version] = manifest
}

// fixed settings, so the test does not depend on the compiler of the machine running it
var fixedSettings = []string{
	"-s", "os=linux", "-s", "arch=x86_64", "-s", "compiler.name=gcc", "-s", "compiler.version=13",
	"-s", "stdlib=libstdc++", "-s", "libc=glibc", "-s", "cxx11_abi=true",
}

func sflags(extra ...string) []string { return append(append([]string{}, fixedSettings...), extra...) }

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	code, out, errs := runCLI(args...)
	if code != 0 {
		t.Fatalf("kiln %s\nexit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, out, errs)
	}
	return out
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const (
	depManifest = `{"name":"v/dep","repository":{"type":"git","url":"https://example.com/v/dep"},
	  "targets":{"dep":{"type":"library","include":["include"]}}}`
	appManifest = `{"name":"v/app","repository":{"type":"git","url":"https://example.com/v/app"},
	  "require":{"v/dep":"^1.0"},
	  "targets":{"app":{"type":"library","include":["include"],"link-libraries":[{"value":"pthread","when":{"os":"linux"}},{"value":"ws2_32","when":{"os":"windows"}}]}}}`
	commit = "0123456789abcdef0123456789abcdef01234567"
)

// publish packs a prefix with one header and one static library and puts it in repo.
func publish(t *testing.T, regURL, pkgDir, name, version, repo string, libName string) {
	t.Helper()
	prefix := filepath.Join(t.TempDir(), "prefix")
	write(t, filepath.Join(prefix, "include", libName+".h"), "// "+libName+"\n")
	write(t, filepath.Join(prefix, "lib", "lib"+libName+".a"), "!<arch>\n")
	dist := t.TempDir()
	mustRun(t, append([]string{"package", "-registry", regURL, "-dir", pkgDir, "-prefix", prefix, "-version", version, "-out", dist}, fixedSettings...)...)
	mustRun(t, "publish", "-dir", pkgDir, "-dist", dist, "-version", version, "-local", repo)
	mustRun(t, "finalize", "-dir", pkgDir, "-version", version, "-local", repo, "-commit", commit)
}

func TestPackagePublishInstallRoundTrip(t *testing.T) {
	t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "cache"))
	reg := newManifestRegistry(t, map[string]map[string]string{"v/dep": {"1.0.0": depManifest}, "v/app": {"1.0.0": appManifest}})
	repo := filepath.Join(t.TempDir(), "repo")
	work := t.TempDir()
	write(t, filepath.Join(work, "dep", "kiln.json"), depManifest)
	write(t, filepath.Join(work, "app", "kiln.json"), appManifest)
	publish(t, reg.srv.URL, filepath.Join(work, "dep"), "v/dep", "1.0.0", repo, "dep")
	publish(t, reg.srv.URL, filepath.Join(work, "app"), "v/app", "1.0.0", repo, "app")

	project := filepath.Join(work, "project")
	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"v/app":"^1.0"}}`)
	out := mustRun(t, append([]string{"install", "-registry", reg.srv.URL, "-dir", project, "-local", repo}, fixedSettings...)...)
	if !strings.Contains(out, "2 package(s) ready") {
		t.Errorf("%s", out)
	}

	cm, err := os.ReadFile(filepath.Join(project, "vendor", "kiln", "kiln.cmake"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"add_library(v::dep UNKNOWN IMPORTED)", "add_library(v::app UNKNOWN IMPORTED)", `INTERFACE_LINK_LIBRARIES "v::dep;pthread"`} {
		if !strings.Contains(string(cm), want) {
			t.Errorf("missing %q in\n%s", want, cm)
		}
	}
	if strings.Contains(string(cm), "ws2_32") {
		t.Error("the windows-only system library must not appear in a linux package")
	}

	var lock struct {
		Packages []struct {
			Name, Version, Origin string
			Requires              []string
		}
	}
	raw, err := os.ReadFile(filepath.Join(project, "kiln.lock"))
	if err != nil || json.Unmarshal(raw, &lock) != nil || len(lock.Packages) != 2 || lock.Packages[0].Name != "v/dep" || lock.Packages[1].Requires[0] != "v/dep" {
		t.Fatalf("lock: %v %s", err, raw)
	}

	// a second run is served from the cache
	again := mustRun(t, append([]string{"install", "-registry", reg.srv.URL, "-dir", project, "-local", repo}, fixedSettings...)...)
	if strings.Count(again, "cached") != 2 {
		t.Errorf("second install should use the cache:\n%s", again)
	}
}

// The central design decision: a binary is valid only for the exact dependency
// builds it was made against, even if the new dependency satisfies every constraint.
func TestStrictDependencyMatching(t *testing.T) {
	t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "cache"))
	reg := newManifestRegistry(t, map[string]map[string]string{"v/dep": {"1.0.0": depManifest}, "v/app": {"1.0.0": appManifest}})
	repo := filepath.Join(t.TempDir(), "repo")
	work := t.TempDir()
	write(t, filepath.Join(work, "dep", "kiln.json"), depManifest)
	write(t, filepath.Join(work, "app", "kiln.json"), appManifest)
	publish(t, reg.srv.URL, filepath.Join(work, "dep"), "v/dep", "1.0.0", repo, "dep")
	publish(t, reg.srv.URL, filepath.Join(work, "app"), "v/app", "1.0.0", repo, "app")

	// dep 1.0.1 is released: it satisfies app's ^1.0, and gets its own binary
	reg.add("v/dep", "1.0.1", depManifest)
	publish(t, reg.srv.URL, filepath.Join(work, "dep"), "v/dep", "1.0.1", repo, "dep")

	project := filepath.Join(work, "project")
	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"v/app":"^1.0"}}`)
	code, _, errs := runCLI(append([]string{"install", "-binary-only", "-registry", reg.srv.URL, "-dir", project, "-local", repo}, fixedSettings...)...)
	if code != exitNoBinary || !strings.Contains(errs, "no binary available for v/app 1.0.0") {
		t.Fatalf("app was built against dep 1.0.0, so with dep 1.0.1 it has no matching binary: %d\n%s", code, errs)
	}

	// pinning dep to the version app was built against makes it match again
	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"v/app":"^1.0","v/dep":"1.0.0"}}`)
	mustRun(t, append([]string{"install", "-registry", reg.srv.URL, "-dir", project, "-local", repo}, fixedSettings...)...)
}

func TestInstallFailureModes(t *testing.T) {
	t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "cache"))
	reg := newManifestRegistry(t, map[string]map[string]string{"v/dep": {"1.0.0": depManifest}})
	repo := filepath.Join(t.TempDir(), "repo")
	work := t.TempDir()
	write(t, filepath.Join(work, "dep", "kiln.json"), depManifest)
	publish(t, reg.srv.URL, filepath.Join(work, "dep"), "v/dep", "1.0.0", repo, "dep")
	project := filepath.Join(work, "project")
	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"v/dep":"^1.0"}}`)
	install := func(extra ...string) (int, string) {
		code, _, errs := runCLI(append(append([]string{"install", "-binary-only", "-registry", reg.srv.URL, "-dir", project}, extra...), fixedSettings...)...)
		return code, errs
	}

	// other settings than the published ones
	if code, errs := install("-local", repo, "-s", "build_type=debug"); code != exitNoBinary || !strings.Contains(errs, "id:") {
		t.Errorf("debug: %d %s", code, errs)
	}
	// no repository at all
	if code, _ := install(); code != exitNoBinary {
		t.Errorf("no repo: %d", code)
	}
	// tampered archive, fresh cache
	archives, _ := filepath.Glob(filepath.Join(repo, "v", "dep", "1.0.0", "*.tar.gz"))
	f, _ := os.OpenFile(archives[0], os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("tamper")
	f.Close()
	t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "cache2"))
	if code, errs := install("-local", repo); code != exitVerify || !strings.Contains(errs, "file hash is") {
		t.Errorf("tampered: %d %s", code, errs)
	}
	// unknown package, bad option
	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"ghost/ghost":"*"}}`)
	if code, _ := install("-local", repo); code != exitResolution {
		t.Errorf("ghost: %d", code)
	}
	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"v/dep":"^1.0"}}`)
	if code, errs := install("-local", repo, "-o", "v/dep:nope=1"); code != exitResolution || !strings.Contains(errs, "no such option") {
		t.Errorf("bad option: %d %s", code, errs)
	}
	if code, errs := install("-o", "malformed"); code != exitUsage || !strings.Contains(errs, "vendor/pkg:option=value") {
		t.Errorf("malformed -o: %d %s", code, errs)
	}
	if code, _, _ := runCLI("install", "-dir", t.TempDir()); code != exitError {
		t.Errorf("no kiln.json: %d", code)
	}
}

func TestPackageRejectsIncompletePrefix(t *testing.T) {
	reg := newManifestRegistry(t, map[string]map[string]string{})
	work := t.TempDir()
	write(t, filepath.Join(work, "dep", "kiln.json"), depManifest)
	prefix := filepath.Join(t.TempDir(), "prefix")
	write(t, filepath.Join(prefix, "include", "dep.h"), "//")
	// the static library is missing from the prefix
	code, _, errs := runCLI(append([]string{"package", "-registry", reg.srv.URL, "-dir", filepath.Join(work, "dep"), "-prefix", prefix, "-version", "1.0.0", "-out", t.TempDir()}, fixedSettings...)...)
	if code == 0 || !strings.Contains(errs, "no static library") {
		t.Errorf("%d %s", code, errs)
	}
	if code, _, _ := runCLI("package", "-dir", filepath.Join(work, "dep")); code != exitUsage {
		t.Errorf("missing flags: %d", code)
	}
}

func TestPublishIsIdempotentAndImmutable(t *testing.T) {
	reg := newManifestRegistry(t, map[string]map[string]string{"v/dep": {"1.0.0": depManifest}})
	repo := filepath.Join(t.TempDir(), "repo")
	work := t.TempDir()
	write(t, filepath.Join(work, "dep", "kiln.json"), depManifest)
	publish(t, reg.srv.URL, filepath.Join(work, "dep"), "v/dep", "1.0.0", repo, "dep")

	prefix := filepath.Join(t.TempDir(), "prefix")
	write(t, filepath.Join(prefix, "include", "dep.h"), "// the very same files")
	write(t, filepath.Join(prefix, "lib", "libdep.a"), "!<arch>\n")
	dist := t.TempDir()
	args := append([]string{"package", "-registry", reg.srv.URL, "-dir", filepath.Join(work, "dep"), "-prefix", prefix, "-version", "1.0.0", "-out", dist}, fixedSettings...)
	mustRun(t, args...)
	// same id, different content than what was published before: refused
	code, _, errs := runCLI("publish", "-dir", filepath.Join(work, "dep"), "-dist", dist, "-version", "1.0.0", "-local", repo)
	if code == 0 || !strings.Contains(errs, "immutable") {
		t.Errorf("republishing different bytes under the same id: %d %s", code, errs)
	}
}
