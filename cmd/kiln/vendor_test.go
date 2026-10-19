package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func vendorFixture(t *testing.T) (project string, args func(...string) []string, cache string) {
	t.Helper()
	needTools(t, "git", "cmake", "g++")
	cache = filepath.Join(t.TempDir(), "cache")
	t.Setenv("KILN_CACHE_DIR", cache)
	t.Setenv("KILN_ALLOW_FILE_SOURCES", "1")
	t.Setenv("KILN_NO_SOURCES", "") // these tests are about the sources

	tinyURL, tinyCommit := gitProject(t, "v1.0.0", tinyFiles)
	appURL, appCommit := gitProject(t, "v1.0.0", appFiles)
	tinyM, appM := sourceManifests(tinyURL, appURL)
	reg := newManifestRegistry(t, map[string]map[string]string{"v/tiny": {"1.0.0": tinyM}, "v/app": {"1.0.0": appM}})
	reg.commits["v/tiny@1.0.0"], reg.commits["v/app@1.0.0"] = tinyCommit, appCommit

	project = t.TempDir()
	write(t, filepath.Join(project, "kiln.json"), `{"require":{"v/app":"^1.0"}}`)
	args = func(extra ...string) []string {
		return append(extra, "-registry", reg.srv.URL, "-dir", project, "-s", "build_type=release")
	}
	return project, args, cache
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestInstallCopiesTheSourcesIntoVendor(t *testing.T) {
	project, args, cache := vendorFixture(t)
	out := mustRun(t, args("install")...)
	if !strings.Contains(out, "library sources:   "+filepath.Join(project, "vendor")+"\n") {
		t.Errorf("the output does not say where the sources are:\n%s", out)
	}

	vendor := filepath.Join(project, "vendor")
	for _, f := range []string{"kiln/kiln.cmake", "v/tiny/include/tiny.h", "v/tiny/.kiln-source.json", "v/app/src/app.cc"} {
		if !exists(filepath.Join(vendor, filepath.FromSlash(f))) {
			t.Errorf("missing %s in vendor/", f)
		}
	}
	if exists(filepath.Join(vendor, "v", "tiny", ".git")) {
		t.Error("the copy must not contain .git")
	}
	// the cache is laid out by name and version, not by hash alone
	for _, f := range []string{"sources/v/tiny/1.0.0/include/tiny.h", "sources/v/app/1.0.0/src/app.cc"} {
		if !exists(filepath.Join(cache, filepath.FromSlash(f))) {
			t.Errorf("missing %s in the cache", f)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(cache, "packages", "v", "tiny", "1.0.0", "*", "include", "tiny.h")); len(m) != 1 {
		t.Errorf("the built package is not at packages/v/tiny/1.0.0/<id>: %v", m)
	}
	// building did not write into the cached checkout
	if exists(filepath.Join(cache, "sources", "v", "tiny", "1.0.0", "build")) {
		t.Error("the cached source was used as a build directory")
	}
}

func TestInstallKeepsEditedVendorSourcesAndPrunesRemovedPackages(t *testing.T) {
	project, args, _ := vendorFixture(t)
	mustRun(t, args("install")...)
	tinyH := filepath.Join(project, "vendor", "v", "tiny", "include", "tiny.h")
	write(t, tinyH, "// my debugging edit\n")
	// a directory that kiln did not make is never touched
	write(t, filepath.Join(project, "vendor", "mine", "keep.txt"), "x")

	mustRun(t, args("install")...)
	if b, _ := os.ReadFile(tinyH); string(b) != "// my debugging edit\n" {
		t.Errorf("an install must not overwrite a vendor directory that is already right: %q", b)
	}

	mustRun(t, args("remove", "v/app")...)
	if exists(filepath.Join(project, "vendor", "v")) {
		t.Error("the sources of packages that are no longer installed must be removed")
	}
	if !exists(filepath.Join(project, "vendor", "mine", "keep.txt")) {
		t.Error("a directory kiln did not create was removed")
	}
}

func TestNoSourcesFlag(t *testing.T) {
	project, args, cache := vendorFixture(t)
	out := mustRun(t, args("install", "--no-sources")...)
	if strings.Contains(out, "library sources") {
		t.Errorf("%s", out)
	}
	if !exists(filepath.Join(project, "vendor", "kiln", "kiln.cmake")) {
		t.Error("the CMake file must still be written")
	}
	if exists(filepath.Join(project, "vendor", "v")) {
		t.Error("--no-sources must not copy sources")
	}
	// the source build itself needed the checkout, so it is cached for later
	if !exists(filepath.Join(cache, "sources", "v", "tiny", "1.0.0")) {
		t.Error("the cached checkout is expected after a source build")
	}
}

func TestVendorNeverReplacesADirectoryItDidNotCreate(t *testing.T) {
	project, args, _ := vendorFixture(t)
	// the project already has its own code where v/tiny would go
	write(t, filepath.Join(project, "vendor", "v", "tiny", "own.txt"), "mine")
	code, _, errs := runCLI(args("install")...)
	if code != exitOK {
		t.Fatalf("a directory in the way must not fail the install: exit %d\n%s", code, errs)
	}
	if !strings.Contains(errs, "was not created by kiln") {
		t.Errorf("expected a warning:\n%s", errs)
	}
	if !exists(filepath.Join(project, "vendor", "v", "tiny", "own.txt")) {
		t.Error("the project's own directory was removed")
	}
	if !exists(filepath.Join(project, "vendor", "v", "app", "src", "app.cc")) {
		t.Error("the other packages must still get their sources")
	}
}
