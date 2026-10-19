package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// releaseWith is like release but the manifest has extra top-level fields (e.g. a require section).
func (e *lockEnv) releaseWith(name, version, extra string) {
	e.t.Helper()
	m := libManifest(name, extra)
	e.reg.add(name, version, m)
	dir := filepath.Join(e.work, "pkg", strings.ReplaceAll(name, "/", "_"))
	write(e.t, filepath.Join(dir, "kiln.json"), m)
	publish(e.t, e.reg.srv.URL, dir, name, version, e.repo, name[strings.Index(name, "/")+1:])
}

func (e *lockEnv) newProject(name string) string {
	e.t.Helper()
	dir := filepath.Join(e.work, "project")
	mustRun(e.t, "init", "--dir", dir, name)
	return dir
}

func hasKey(t *testing.T, path, key string) bool {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(readFile(t, path)), &m); err != nil {
		t.Fatal(err)
	}
	_, ok := m[key]
	return ok
}

func manifestSections(t *testing.T, project string) (require, dev map[string]any) {
	t.Helper()
	var m struct {
		Require    map[string]any `json:"require"`
		RequireDev map[string]any `json:"require-dev"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(project, "kiln.json"))), &m); err != nil {
		t.Fatal(err)
	}
	return m.Require, m.RequireDev
}

func TestRequireAddsTheLatestAndInstalls(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/dep", "1.0.0")
	e.release("v/dep", "1.1.0")
	project := e.newProject("me/app")

	out := mustRun(t, e.cmd("require", project, "v/dep")...)
	if !strings.Contains(out, "Adding v/dep ^1.1.0 to require") || !strings.Contains(out, "1 package(s) ready") {
		t.Errorf("%s", out)
	}
	if req, _ := manifestSections(t, project); req["v/dep"] != "^1.1.0" {
		t.Errorf("the newest stable version, as a caret constraint, must be written: %v", req)
	}
	if got := lockedVersions(t, project)["v/dep"]; got != "1.1.0" {
		t.Errorf("locked %s", got)
	}
	if !strings.Contains(readFile(t, filepath.Join(project, "vendor", "kiln", "kiln.cmake")), "v::dep") {
		t.Error("integration not generated")
	}
}

func TestRequireKeepsWhatIsAlreadyLocked(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/dep", "1.0.0")
	e.release("v/other", "1.0.0")
	project := e.newProject("me/app")
	mustRun(t, e.cmd("require", project, "v/dep:^1.0")...)
	if got := lockedVersions(t, project)["v/dep"]; got != "1.0.0" {
		t.Fatal(got)
	}

	e.release("v/dep", "1.2.0") // a newer dep appears; requiring something else must not touch dep
	mustRun(t, e.cmd("require", project, "v/other:1.0.0")...)
	got := lockedVersions(t, project)
	if got["v/dep"] != "1.0.0" || got["v/other"] != "1.0.0" {
		t.Errorf("adding a package must not move the others: %v", got)
	}
	if req, _ := manifestSections(t, project); req["v/other"] != "1.0.0" || req["v/dep"] != "^1.0" {
		t.Errorf("an explicit constraint is written as given: %v", req)
	}

	// requiring a package that is already there with a new constraint does move it
	mustRun(t, e.cmd("require", project, "v/dep:^1.2")...)
	if got := lockedVersions(t, project)["v/dep"]; got != "1.2.0" {
		t.Errorf("a changed constraint must be re-resolved: %s", got)
	}
}

func TestRequireFailuresLeaveTheManifestAlone(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/dep", "1.0.0")
	project := e.newProject("me/app")
	mustRun(t, e.cmd("require", project, "v/dep")...)
	before := readFile(t, filepath.Join(project, "kiln.json"))
	lockBefore := readFile(t, filepath.Join(project, "kiln.lock"))

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown package", []string{"v/ghost"}, "not found in the registry"},
		{"no matching version", []string{"v/dep:^9"}, "no version of v/dep matches ^9"},
		{"bad constraint", []string{"v/dep:???"}, "invalid constraint"},
	}
	for _, c := range cases {
		code, _, errs := runCLI(e.cmd("require", project, c.args...)...)
		if code == 0 || !strings.Contains(errs, c.want) {
			t.Errorf("%s: %d %s", c.name, code, errs)
		}
	}
	if code, _, _ := runCLI(e.cmd("require", project, "--check", "v/dep")...); code != exitUsage {
		t.Errorf("--check: %d", code)
	}
	if code, _, _ := runCLI(e.cmd("require", project)...); code != exitUsage {
		t.Errorf("no package: %d", code)
	}
	if readFile(t, filepath.Join(project, "kiln.json")) != before || readFile(t, filepath.Join(project, "kiln.lock")) != lockBefore {
		t.Error("a rejected require must not change kiln.json or kiln.lock")
	}
}

func TestRequireRevertsTheManifestWhenTheInstallFails(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/dep", "1.0.0")
	project := e.newProject("me/app")
	mustRun(t, e.cmd("require", project, "v/dep")...)
	before := readFile(t, filepath.Join(project, "kiln.json"))
	lockBefore := readFile(t, filepath.Join(project, "kiln.lock"))

	// in the registry, but no binary exists and building is forbidden
	e.reg.add("v/nobin", "1.0.0", libManifest("v/nobin", ""))
	code, _, errs := runCLI(e.cmd("require", project, "v/nobin", "--binary-only")...)
	if code != exitNoBinary || !strings.Contains(errs, "reverted to its original content") {
		t.Fatalf("%d\n%s", code, errs)
	}
	if readFile(t, filepath.Join(project, "kiln.json")) != before {
		t.Error("kiln.json must be restored")
	}
	if readFile(t, filepath.Join(project, "kiln.lock")) != lockBefore {
		t.Error("the lock must not change when the install fails")
	}
}

func TestRequireDevAndNoInstall(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/dep", "1.0.0")
	e.release("v/tool", "1.0.0")
	project := e.newProject("me/app")

	mustRun(t, e.cmd("require", project, "v/tool", "--dev")...)
	req, dev := manifestSections(t, project)
	if len(req) != 0 || dev["v/tool"] != "^1.0.0" {
		t.Errorf("--dev: require=%v require-dev=%v", req, dev)
	}
	if lockedVersions(t, project)["v/tool"] != "1.0.0" {
		t.Error("dev dependencies are installed for the root project")
	}

	lockBefore := readFile(t, filepath.Join(project, "kiln.lock"))
	out := mustRun(t, e.cmd("require", project, "v/dep", "--no-install")...)
	if !strings.Contains(out, "out of date") || readFile(t, filepath.Join(project, "kiln.lock")) != lockBefore {
		t.Errorf("--no-install edits only the manifest:\n%s", out)
	}
	if code, _, errs := runCLI(e.cmd("install", project)...); code != exitResolution || !strings.Contains(errs, "out of date") {
		t.Errorf("the lock is stale after --no-install: %d %s", code, errs)
	}
	mustRun(t, e.cmd("update", project)...)
	mustRun(t, e.cmd("install", project, "--locked")...)
}

func TestRequirePreservesTheRestOfTheFile(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/dep", "1.0.0")
	project := filepath.Join(e.work, "project")
	write(t, filepath.Join(project, "kiln.json"), `{
  "name": "me/app",
  "description": "my application",
  "license": "MIT",
  "keywords": ["one", "two", "three"],
  "targets": {"app": {"type": "executable"}}
}`)
	mustRun(t, e.cmd("require", project, "v/dep")...)
	var m map[string]any
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(project, "kiln.json"))), &m); err != nil {
		t.Fatal(err)
	}
	if m["description"] != "my application" || m["license"] != "MIT" || m["keywords"].([]any)[1] != "two" || m["targets"].(map[string]any)["app"] == nil {
		t.Errorf("fields were lost: %v", m)
	}
	body := readFile(t, filepath.Join(project, "kiln.json"))
	if strings.Index(body, `"description"`) > strings.Index(body, `"require"`) {
		t.Errorf("the key order must be kept:\n%s", body)
	}
}

func TestRemove(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/dep", "1.0.0")
	e.releaseWith("v/app", "1.0.0", `,"require":{"v/dep":"^1.0"}`) // app needs dep
	e.release("v/other", "1.0.0")
	project := e.newProject("me/project")
	mustRun(t, e.cmd("require", project, "v/app", "v/other")...)
	if got := lockedVersions(t, project); len(got) != 3 {
		t.Fatalf("%v", got)
	}

	e.release("v/other", "1.1.0") // a newer release must not sneak in during a remove
	out := mustRun(t, e.cmd("remove", project, "v/app")...)
	if !strings.Contains(out, "Removing v/app") {
		t.Errorf("%s", out)
	}
	got := lockedVersions(t, project)
	if _, ok := got["v/app"]; ok {
		t.Error("v/app must leave the lock")
	}
	if _, ok := got["v/dep"]; ok {
		t.Error("v/dep was only needed by v/app, so it must leave the lock too")
	}
	if got["v/other"] != "1.0.0" {
		t.Errorf("remove must not move the others: %v", got)
	}
	if req, _ := manifestSections(t, project); req["v/app"] != nil || req["v/other"] == nil {
		t.Errorf("%v", req)
	}
	cm := readFile(t, filepath.Join(project, "vendor", "kiln", "kiln.cmake"))
	if strings.Contains(cm, "v::app") || !strings.Contains(cm, "v::other") {
		t.Errorf("integration not regenerated:\n%s", cm)
	}

	// unknown package: error, nothing changes
	before := readFile(t, filepath.Join(project, "kiln.json"))
	if code, _, errs := runCLI(e.cmd("remove", project, "v/ghost")...); code == 0 || !strings.Contains(errs, "not in require or require-dev") {
		t.Errorf("%d %s", code, errs)
	}
	if readFile(t, filepath.Join(project, "kiln.json")) != before {
		t.Error("a failed remove must not change the manifest")
	}
	// removing the last one leaves a valid, empty project
	mustRun(t, e.cmd("remove", project, "v/other")...)
	if req, _ := manifestSections(t, project); len(req) != 0 {
		t.Errorf("%v", req)
	}
	if len(lockedVersions(t, project)) != 0 {
		t.Error("nothing should be locked any more")
	}
}

func TestInit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "My Cool_Project")
	out := mustRun(t, "init", "--dir", dir)
	if strings.Contains(out, "for ") {
		t.Errorf("by default there is no name: %s", out)
	}
	path := filepath.Join(dir, "kiln.json")
	if hasKey(t, path, "name") || !strings.Contains(readFile(t, path), `"require": {}`) {
		t.Errorf("a project that only uses libraries needs no name:\n%s", readFile(t, path))
	}
	original := readFile(t, path)
	if code, _, errs := runCLI("init", "--dir", dir, "me/other"); code == 0 || !strings.Contains(errs, "already exists") {
		t.Errorf("init must not overwrite: %d %s", code, errs)
	}
	if readFile(t, path) != original {
		t.Error("the existing file was modified")
	}

	named := filepath.Join(t.TempDir(), "named")
	if out := mustRun(t, "init", "--dir", named, "me/lib"); !strings.Contains(out, "for me/lib") || !strings.Contains(readFile(t, filepath.Join(named, "kiln.json")), `"name": "me/lib"`) {
		t.Errorf("a given name is used: %s", out)
	}
	if code, _, errs := runCLI("init", "--dir", filepath.Join(t.TempDir(), "x"), "NoVendor"); code != exitUsage || !strings.Contains(errs, "not a valid package name") {
		t.Errorf("%d %s", code, errs)
	}
	if code, _, _ := runCLI("init", "a/b", "c/d"); code != exitUsage {
		t.Errorf("two names: %d", code)
	}
}

// No kiln.json yet: require creates one, without a name.
func TestRequireCreatesTheManifestWhenItIsMissing(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/dep", "1.0.0")
	project := filepath.Join(e.work, "brand", "new", "project") // not even the directory exists
	out := mustRun(t, e.cmd("require", project, "v/dep")...)
	if !strings.Contains(out, "Creating ") || !strings.Contains(out, "1 package(s) ready") {
		t.Errorf("%s", out)
	}
	path := filepath.Join(project, "kiln.json")
	if hasKey(t, path, "name") {
		t.Errorf("the created manifest must have no name:\n%s", readFile(t, path))
	}
	if req, _ := manifestSections(t, project); req["v/dep"] != "^1.0.0" {
		t.Errorf("%v", req)
	}
	if lockedVersions(t, project)["v/dep"] != "1.0.0" {
		t.Error("the lock must be created too")
	}
	// the project works like any other from here on
	mustRun(t, e.cmd("install", project, "--locked")...)
	e.release("v/other", "1.0.0")
	mustRun(t, e.cmd("require", project, "v/other")...)
}

func TestFailedFirstRequireLeavesNoFileBehind(t *testing.T) {
	e := newLockEnv(t)
	e.reg.add("v/nobin", "1.0.0", libManifest("v/nobin", ""))
	e.reg.add("v/ghostly", "1.0.0", libManifest("v/ghostly", ""))
	project := filepath.Join(e.work, "fresh")

	// rejected before anything is written
	if code, _, _ := runCLI(e.cmd("require", project, "v/does-not-exist")...); code == 0 {
		t.Fatal("must fail")
	}
	if _, err := os.Stat(filepath.Join(project, "kiln.json")); err == nil {
		t.Error("an unknown package must not leave a kiln.json behind")
	}
	// the install fails after the file was written: it is removed again
	code, _, errs := runCLI(e.cmd("require", project, "v/nobin", "--binary-only")...)
	if code != exitNoBinary || !strings.Contains(errs, "it created was removed again") {
		t.Fatalf("%d\n%s", code, errs)
	}
	if _, err := os.Stat(filepath.Join(project, "kiln.json")); err == nil {
		t.Error("the kiln.json created by the failed require must be removed")
	}
	if _, err := os.Stat(filepath.Join(project, "kiln.lock")); err == nil {
		t.Error("no lock may be left behind")
	}
}

func TestRemoveNeedsAManifest(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/dep", "1.0.0")
	empty := t.TempDir()
	code, _, errs := runCLI(e.cmd("remove", empty, "v/dep")...)
	if code == 0 || !strings.Contains(errs, "nothing to remove from") {
		t.Errorf("%d %s", code, errs)
	}
	if _, err := os.Stat(filepath.Join(empty, "kiln.json")); err == nil {
		t.Error("remove must not create a manifest")
	}
}

// A name is needed to package or publish, and nowhere else.
func TestANamelessProjectCannotBePackagedOrPublished(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/dep", "1.0.0")
	project := filepath.Join(e.work, "app")
	mustRun(t, e.cmd("require", project, "v/dep")...) // nameless, and installs fine

	prefix := filepath.Join(t.TempDir(), "prefix")
	write(t, filepath.Join(prefix, "include", "x.h"), "//")
	for _, args := range [][]string{
		append([]string{"package", "-d", project, "--prefix", prefix, "--version", "1.0.0", "--registry", e.reg.srv.URL}, fixedSettings...),
		{"publish", "-d", project, "--version", "1.0.0", "--local", e.repo},
		{"finalize", "-d", project, "--version", "1.0.0", "--local", e.repo, "--commit", commit},
	} {
		code, _, errs := runCLI(args...)
		if code == 0 || !strings.Contains(errs, `has no "name"`) || !strings.Contains(errs, "vendor/name") {
			t.Errorf("kiln %s: %d\n%s", args[0], code, errs)
		}
	}
}
