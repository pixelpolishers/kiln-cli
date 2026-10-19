package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// outdatedEnv has: v/app (requires v/lib ~1.0), v/lib, and v/solo. The project
// requires v/app ^1.0 and v/solo ^1.0, both at 1.0.0 in the lock.
func outdatedEnv(t *testing.T) (*lockEnv, string) {
	t.Helper()
	e := newLockEnv(t)
	e.release("v/lib", "1.0.0")
	e.release("v/solo", "1.0.0")
	app := libManifest("v/app", `,"require":{"v/lib":"~1.0.0"}`)
	e.reg.add("v/app", "1.0.0", app)
	write(t, filepath.Join(e.work, "pkg", "v_app", "kiln.json"), app)
	publish(t, e.reg.srv.URL, filepath.Join(e.work, "pkg", "v_app"), "v/app", "1.0.0", e.repo, "app")
	project := e.project(`"v/app":"^1.0","v/solo":"^1.0"`)
	mustRun(t, e.cmd("install", project)...)
	return e, project
}

func outdatedArgs(e *lockEnv, project string, extra ...string) []string {
	return append([]string{"outdated", "--registry", e.reg.srv.URL, "--dir", project}, extra...)
}

func TestOutdatedSaysEverythingIsUpToDate(t *testing.T) {
	e, project := outdatedEnv(t)
	out := mustRun(t, outdatedArgs(e, project)...)
	if !strings.Contains(out, "Everything is up to date.") {
		t.Errorf("%s", out)
	}
	if code, _, _ := runCLI(outdatedArgs(e, project, "--strict")...); code != exitOK {
		t.Errorf("--strict with nothing outdated: exit %d", code)
	}
}

func TestOutdatedShowsWantedAndLatest(t *testing.T) {
	e, project := outdatedEnv(t)
	e.release("v/solo", "1.2.0")                                 // allowed by ^1.0
	e.reg.add("v/solo", "2.0.0", libManifest("v/solo", ""))      // blocked by ^1.0
	e.reg.add("v/lib", "1.0.5", libManifest("v/lib", ""))        // allowed by ~1.0.0 of v/app
	e.reg.add("v/lib", "1.1.0", libManifest("v/lib", ""))        // blocked by v/app's ~1.0.0
	e.reg.add("v/solo", "3.0.0-rc.1", libManifest("v/solo", "")) // prereleases are never "latest"

	out := mustRun(t, outdatedArgs(e, project)...)
	var solo string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "v/solo") {
			solo = line
		}
	}
	f := strings.Fields(solo)
	if len(f) < 4 || f[1] != "1.0.0" || f[2] != "1.2.0" || f[3] != "2.0.0" {
		t.Errorf("v/solo: want locked 1.0.0, wanted 1.2.0, latest 2.0.0, got %q\n%s", solo, out)
	}
	if !strings.Contains(solo, "blocked by version constraints") {
		t.Errorf("a held back package must say so: %q", solo)
	}
	if strings.Contains(out, "3.0.0-rc.1") {
		t.Errorf("a prerelease must not be reported:\n%s", out)
	}
	// v/lib is not in kiln.json, only v/app needs it
	if strings.Contains(out, "v/lib") {
		t.Errorf("transitive packages are listed only with --all:\n%s", out)
	}
	if !strings.Contains(out, "1 more package(s) required by other packages are outdated") {
		t.Errorf("the hidden transitive package must be counted:\n%s", out)
	}

	all := mustRun(t, outdatedArgs(e, project, "--all")...)
	var lib string
	for _, line := range strings.Split(all, "\n") {
		if strings.HasPrefix(line, "v/lib") {
			lib = line
		}
	}
	g := strings.Fields(lib)
	if len(g) < 4 || g[1] != "1.0.0" || g[2] != "1.0.5" || g[3] != "1.1.0" {
		t.Errorf("v/lib: want 1.0.0 / 1.0.5 / 1.1.0, got %q\n%s", lib, all)
	}

	// nothing was changed
	if got := lockedVersions(t, project); got["v/solo"] != "1.0.0" || got["v/lib"] != "1.0.0" {
		t.Errorf("outdated must not touch the lock: %v", got)
	}
	// ...and `update` really installs the WANTED column (v/lib is left out: a new
	// v/lib changes the identity of v/app, which has no prebuilt binary here)
	mustRun(t, e.cmd("update", project, "v/solo")...)
	if got := lockedVersions(t, project)["v/solo"]; got != "1.2.0" {
		t.Errorf("update did not install what outdated promised: %s", got)
	}
}

func TestOutdatedStrictAndJSON(t *testing.T) {
	e, project := outdatedEnv(t)
	e.reg.add("v/solo", "1.1.0", libManifest("v/solo", ""))

	code, _, _ := runCLI(outdatedArgs(e, project, "--strict")...)
	if code != exitOutdated {
		t.Errorf("--strict with something outdated: exit %d, want %d", code, exitOutdated)
	}
	if code, _, _ := runCLI(outdatedArgs(e, project)...); code != exitOK {
		t.Errorf("without --strict the exit code is 0, got %d", code)
	}

	out := mustRun(t, outdatedArgs(e, project, "--json")...)
	var rows []struct {
		Name, Locked, Wanted, Latest string
		Direct                       bool
		HeldBack                     bool `json:"held-back"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("%v\n%s", err, out)
	}
	if r := rows[0]; r.Name != "v/solo" || r.Locked != "1.0.0" || r.Wanted != "1.1.0" || r.Latest != "1.1.0" {
		t.Errorf("%+v", r)
	}
}

func TestOutdatedNeedsALockAndWarnsWhenItIsStale(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/solo", "1.0.0")
	project := e.project(`"v/solo":"^1.0"`)
	code, _, errs := runCLI(outdatedArgs(e, project)...)
	if code == exitOK || !strings.Contains(errs, "kiln.lock") {
		t.Errorf("no lock: exit %d\n%s", code, errs)
	}

	mustRun(t, e.cmd("install", project)...)
	e.release("v/lib", "1.0.0")
	write(t, filepath.Join(project, "kiln.json"), `{"require":{"v/solo":"^1.0","v/lib":"^1.0"}}`)
	code, _, errs = runCLI(outdatedArgs(e, project)...)
	if code != exitOK || !strings.Contains(errs, "kiln.json changed since kiln.lock was written") {
		t.Errorf("stale lock: exit %d\n%s", code, errs)
	}
}
