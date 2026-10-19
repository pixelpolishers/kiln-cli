package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCacheLsAndClear(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/one", "1.0.0")
	e.release("v/two", "1.0.0")
	project := e.project(`"v/one":"^1.0","v/two":"^1.0"`)
	cacheDir := os.Getenv("KILN_CACHE_DIR")
	mustRun(t, e.cmd("install", project)...)

	out := mustRun(t, "cache", "ls")
	for _, want := range []string{"cache: " + cacheDir, "packages", "v/one 1.0.0", "v/two 1.0.0", "total"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	// a dry run deletes nothing
	out = mustRun(t, "cache", "clear", "--dry-run")
	if !strings.Contains(out, "Would delete 2 packages") {
		t.Errorf("%s", out)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "packages", "v", "one")); err != nil {
		t.Fatalf("a dry run must not delete: %v", err)
	}

	// a stale temporary directory from an interrupted build is removed, a recent one is not
	stale := filepath.Join(cacheDir, ".build-stale")
	recent := filepath.Join(cacheDir, ".build-recent")
	write(t, filepath.Join(stale, "x"), "x")
	write(t, filepath.Join(recent, "x"), "x")
	old := timeAgo(2)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	out = mustRun(t, "cache", "clear")
	if !strings.Contains(out, "Deleted 2 packages") || !strings.Contains(out, "1 abandoned temporary directory") || !strings.Contains(out, "kiln install") {
		t.Errorf("%s", out)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("the abandoned temporary directory stays")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Error("a recent temporary directory may belong to a running kiln and must stay")
	}
	if left := mustRun(t, "cache", "ls"); !strings.Contains(left, "empty") {
		t.Errorf("%s", left)
	}
	// the project's own files are untouched
	for _, f := range []string{"kiln.json", "kiln.lock", filepath.Join("vendor", "kiln", "kiln.cmake")} {
		if _, err := os.Stat(filepath.Join(project, f)); err != nil {
			t.Errorf("clear must not touch the project: %v", err)
		}
	}
	// and one install makes everything usable again, with the locked versions
	before := lockedVersions(t, project)
	mustRun(t, e.cmd("install", project)...)
	if got := lockedVersions(t, project); len(got) != 2 || got["v/one"] != before["v/one"] {
		t.Errorf("%v %v", before, got)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "packages", "v", "one")); err != nil {
		t.Errorf("install fills the cache again: %v", err)
	}

	// only one part
	mustRun(t, "cache", "clear", "--logs")
	if _, err := os.Stat(filepath.Join(cacheDir, "packages", "v", "one")); err != nil {
		t.Errorf("--logs must leave the packages: %v", err)
	}
}

func TestCacheRefusesAnUnsafeDirectoryAndUnknownCommands(t *testing.T) {
	home, _ := os.UserHomeDir()
	t.Setenv("KILN_CACHE_DIR", home)
	if code, _, errs := runCLI("cache", "clear"); code == exitOK || !strings.Contains(errs, "refusing") {
		t.Errorf("%d %s", code, errs)
	}
	t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "new"))
	if out := mustRun(t, "cache", "clear"); !strings.Contains(out, "Nothing to delete") {
		t.Errorf("%s", out)
	}
	if code, _, _ := runCLI("cache", "frobnicate"); code != exitUsage {
		t.Errorf("exit %d", code)
	}
	if code, _, _ := runCLI("cache"); code != exitUsage {
		t.Errorf("exit %d", code)
	}
}

func timeAgo(hours int) time.Time { return time.Now().Add(-time.Duration(hours) * time.Hour) }
