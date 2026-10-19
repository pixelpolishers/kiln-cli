package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// reqPackage is a package whose manifest declares build requirements.
func reqPackage(name, repoURL, requires string) string {
	return `{"name":"` + name + `","repository":{"type":"git","url":"` + repoURL + `"},
	  "targets":{"` + strings.Split(name, "/")[1] + `":{"type":"library","include":["include"]}},
	  "build":{"system":"cmake","requires":` + requires + `},
	  "providers":[{"type":"github","tag":"v{version}"}]}`
}

func skipOnWindows(t *testing.T, why string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix-only test: " + why)
	}
}

func cacheEntries(t *testing.T) []string {
	t.Helper()
	entries, _ := filepath.Glob(filepath.Join(os.Getenv("KILN_CACHE_DIR"), "packages", "*", "*", "*", "*"))
	return entries
}

func TestMissingRequirementsAreReportedTogetherBeforeAnyBuild(t *testing.T) {
	skipOnWindows(t, "a manifest probe runs the unix command false")
	needTools(t, "git", "cmake")
	t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("KILN_ALLOW_FILE_SOURCES", "1")
	url, commit := gitProject(t, "v1.0.0", tinyFiles)

	reg := newManifestRegistry(t, map[string]map[string]string{
		"v/a": {"1.0.0": reqPackage("v/a", url, `[{"name":"flux capacitor","tool":"definitely-not-a-tool-aaa","hint":"buy one at the shop"}]`)},
		"v/b": {"1.0.0": reqPackage("v/b", url, `[{"name":"Perl modules","check":["false"],"hint":{"fedora":"sudo dnf install perl-core","debian":"sudo apt install perl","default":"install perl"}}]`)},
		"v/c": {"1.0.0": reqPackage("v/c", url, `[]`)}, // fine, but must not be built either: nothing starts while anything is missing
	})
	for _, n := range []string{"v/a", "v/b", "v/c"} {
		reg.commits[n+"@1.0.0"] = commit
	}
	project := t.TempDir()
	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"v/a":"*","v/b":"*","v/c":"*"}}`)

	code, _, errs := runCLI("install", "-registry", reg.srv.URL, "-dir", project)
	if code != exitMissingReq {
		t.Fatalf("exit %d, want %d\n%s", code, exitMissingReq, errs)
	}
	for _, want := range []string{
		"v/a 1.0.0", "missing: flux capacitor", `"definitely-not-a-tool-aaa" was not found on PATH`, "buy one at the shop",
		"v/b 1.0.0", "missing: Perl modules", "`false` failed",
		"Nothing was installed", "items that are only needed to build go away with --binary-only",
	} {
		if !strings.Contains(errs, want) {
			t.Errorf("missing %q in:\n%s", want, errs)
		}
	}
	if strings.Contains(errs, "v/c 1.0.0") {
		t.Errorf("the package with no problems must not be listed:\n%s", errs)
	}
	if got := cacheEntries(t); len(got) != 0 {
		t.Errorf("nothing may be built while requirements are missing, found %v", got)
	}
	if _, err := os.Stat(filepath.Join(project, "vendor")); err == nil {
		t.Error("no integration files may be written")
	}
}

func TestOptionalRequirementOnlyWarns(t *testing.T) {
	needTools(t, "git", "cmake", "g++")
	t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("KILN_ALLOW_FILE_SOURCES", "1")
	url, commit := gitProject(t, "v1.0.0", tinyFiles)
	m := reqPackage("v/tiny", url, `[{"name":"audio backend","tool":"definitely-not-a-tool-bbb","optional":true,"hint":"install a sound server"}]`)
	reg := newManifestRegistry(t, map[string]map[string]string{"v/tiny": {"1.0.0": m}})
	reg.commits["v/tiny@1.0.0"] = commit
	project := t.TempDir()
	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"v/tiny":"*"}}`)

	code, out, errs := runCLI("install", "-registry", reg.srv.URL, "-dir", project)
	if code != 0 {
		t.Fatalf("an optional requirement must not block: %d\n%s\n%s", code, out, errs)
	}
	if !strings.Contains(errs, "optional audio backend is missing") || !strings.Contains(errs, "install a sound server") {
		t.Errorf("the warning must say what is missing and how to fix it:\n%s", errs)
	}
	if len(cacheEntries(t)) != 1 {
		t.Error("the package must have been built")
	}
}

func TestRequirementsAreNotCheckedWhenABinaryIsUsed(t *testing.T) {
	t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "cache"))
	// the requirement can never be met, but nobody builds: a prebuilt binary is used
	m := `{"name":"v/dep","repository":{"type":"git","url":"https://example.com/v/dep"},
	  "targets":{"dep":{"type":"library","include":["include"]}},
	  "build":{"system":"cmake","requires":[{"name":"impossible","tool":"definitely-not-a-tool-ccc"}]}}`
	reg := newManifestRegistry(t, map[string]map[string]string{"v/dep": {"1.0.0": m}})
	repo := filepath.Join(t.TempDir(), "repo")
	work := t.TempDir()
	write(t, filepath.Join(work, "dep", "kiln.json"), m)
	publish(t, reg.srv.URL, filepath.Join(work, "dep"), "v/dep", "1.0.0", repo, "dep")

	project := filepath.Join(work, "project")
	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"v/dep":"*"}}`)
	args := append([]string{"install", "-registry", reg.srv.URL, "-dir", project, "-local", repo}, fixedSettings...)
	mustRun(t, args...)

	// -check reports that nothing needs building
	cache2 := filepath.Join(t.TempDir(), "cache2")
	t.Setenv("KILN_CACHE_DIR", cache2)
	out := mustRun(t, append(args, "-check")...)
	if !strings.Contains(out, "No source builds are needed") {
		t.Errorf("%s", out)
	}
	// but forcing a source build does hit the requirement
	code, _, errs := runCLI(append(args, "-source-only")...)
	if code != exitMissingReq || !strings.Contains(errs, "impossible") {
		t.Errorf("-source-only must check the requirements: %d\n%s", code, errs)
	}
}

func TestCheckFlagBuildsAndWritesNothing(t *testing.T) {
	skipOnWindows(t, "probes run the unix commands true and false")
	needTools(t, "git", "cmake")
	t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "cache"))
	url, commit := gitProject(t, "v1.0.0", tinyFiles)
	ok := reqPackage("v/ok", url, `[{"name":"true","check":["true"]}]`)
	bad := reqPackage("v/bad", url, `[{"name":"false","check":["false"],"hint":"fix it"}]`)
	reg := newManifestRegistry(t, map[string]map[string]string{"v/ok": {"1.0.0": ok}, "v/bad": {"1.0.0": bad}})
	reg.commits["v/ok@1.0.0"], reg.commits["v/bad@1.0.0"] = commit, commit

	project := t.TempDir()
	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"v/ok":"*"}}`)
	out := mustRun(t, "install", "-check", "-registry", reg.srv.URL, "-dir", project)
	if !strings.Contains(out, "All system requirements are met, including those of the 1 package(s) that need a source build: v/ok") {
		t.Errorf("%s", out)
	}
	for _, f := range []string{"vendor", "kiln.lock"} {
		if _, err := os.Stat(filepath.Join(project, f)); err == nil {
			t.Errorf("-check must not write %s", f)
		}
	}
	if len(cacheEntries(t)) != 0 {
		t.Error("-check must not build")
	}

	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"v/ok":"*","v/bad":"*"}}`)
	code, _, errs := runCLI("install", "-check", "-registry", reg.srv.URL, "-dir", project)
	if code != exitMissingReq || !strings.Contains(errs, "missing: false") || !strings.Contains(errs, "fix it") {
		t.Errorf("%d\n%s", code, errs)
	}
}

func TestWhenAndProbesInManifests(t *testing.T) {
	skipOnWindows(t, "probes run the unix commands true and false")
	needTools(t, "git", "cmake")
	t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "cache"))
	url, commit := gitProject(t, "v1.0.0", tinyFiles)
	m := reqPackage("v/p", url, `[
	  {"name":"windows only tool","tool":"definitely-not-a-tool-ddd","when":{"os":"windows"}},
	  {"name":"probe that passes","check":["true"]},
	  {"name":"probe that fails","check":["false"],"hint":"fix it"}]`)
	reg := newManifestRegistry(t, map[string]map[string]string{"v/p": {"1.0.0": m}})
	reg.commits["v/p@1.0.0"] = commit
	project := t.TempDir()
	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"v/p":"*"}}`)

	code, _, errs := runCLI("install", "-registry", reg.srv.URL, "-dir", project, "-s", "os=linux")
	if code != exitMissingReq || !strings.Contains(errs, "probe that fails") {
		t.Fatalf("%d\n%s", code, errs)
	}
	for _, unwanted := range []string{"windows only tool", "probe that passes"} {
		if strings.Contains(errs, unwanted) {
			t.Errorf("%q must not be reported:\n%s", unwanted, errs)
		}
	}
}
