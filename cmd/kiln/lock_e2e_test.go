package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// libManifest is a minimal library package; its single target is named after the package.
func libManifest(name string, extra string) string {
	short := name[strings.Index(name, "/")+1:]
	return `{"name":"` + name + `","repository":{"type":"git","url":"https://example.com/` + name + `"},
	  "targets":{"` + short + `":{"type":"library","include":["include"]}}` + extra + `}`
}

// lockEnv is a registry plus a local repository holding binaries.
type lockEnv struct {
	t    *testing.T
	reg  *manifestRegistry
	repo string
	work string
}

func newLockEnv(t *testing.T) *lockEnv {
	t.Helper()
	t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "cache"))
	return &lockEnv{t: t, reg: newManifestRegistry(t, map[string]map[string]string{}), repo: filepath.Join(t.TempDir(), "repo"), work: t.TempDir()}
}

// release makes name@version available in the registry with a prebuilt binary.
func (e *lockEnv) release(name, version string) {
	e.t.Helper()
	m := libManifest(name, "")
	e.reg.add(name, version, m)
	dir := filepath.Join(e.work, "pkg", strings.ReplaceAll(name, "/", "_"))
	write(e.t, filepath.Join(dir, "kiln.json"), m)
	publish(e.t, e.reg.srv.URL, dir, name, version, e.repo, name[strings.Index(name, "/")+1:])
}

func (e *lockEnv) project(requires string) string {
	e.t.Helper()
	dir := filepath.Join(e.work, "project")
	write(e.t, filepath.Join(dir, "kiln.json"), `{"name":"me/project","require":{`+requires+`}}`)
	return dir
}

func (e *lockEnv) cmd(verb, project string, extra ...string) []string {
	args := []string{verb, "--registry", e.reg.srv.URL, "--dir", project, "--local", e.repo}
	return append(append(args, fixedSettings...), extra...)
}

func lockedVersions(t *testing.T, project string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(project, "kiln.lock"))
	if err != nil {
		t.Fatal(err)
	}
	var lk struct {
		Packages []struct{ Name, Version string }
	}
	if err := json.Unmarshal(raw, &lk); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, p := range lk.Packages {
		out[p.Name] = p.Version
	}
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The promise: everybody working on the project gets the same dependencies.
func TestInstallFollowsTheLockInsteadOfResolvingAgain(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/dep", "1.0.0")
	project := e.project(`"v/dep":"^1.0"`)

	out := mustRun(t, e.cmd("install", project)...)
	if !strings.Contains(out, "created") {
		t.Errorf("first install should create the lock:\n%s", out)
	}
	lockPath := filepath.Join(project, "kiln.lock")
	first := readFile(t, lockPath)
	if got := lockedVersions(t, project)["v/dep"]; got != "1.0.0" {
		t.Fatalf("locked %s", got)
	}

	// a new release appears; it satisfies ^1.0 and a binary exists for it
	e.release("v/dep", "1.1.0")
	out = mustRun(t, e.cmd("install", project)...)
	if !strings.Contains(out, "followed exactly") || strings.Contains(out, "1.1.0") {
		t.Errorf("install must follow the lock:\n%s", out)
	}
	if readFile(t, lockPath) != first {
		t.Error("install must not touch an existing lockfile")
	}

	// another developer: a fresh machine (empty cache), same kiln.json and kiln.lock
	other := filepath.Join(t.TempDir(), "other-checkout")
	write(t, filepath.Join(other, "kiln.json"), readFile(t, filepath.Join(project, "kiln.json")))
	write(t, filepath.Join(other, "kiln.lock"), first)
	t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "other-cache"))
	mustRun(t, e.cmd("install", other)...)
	if readFile(t, filepath.Join(other, "kiln.lock")) != first {
		t.Error("the second developer's lock must be identical")
	}
	if !strings.Contains(readFile(t, filepath.Join(other, "vendor", "kiln", "kiln.cmake")), "v::dep") {
		t.Error("integration not generated")
	}
	cm := readFile(t, filepath.Join(other, "vendor", "kiln", "kiln.cmake"))
	if strings.Contains(cm, "1.1.0") || !strings.Contains(cm, "# v/dep 1.0.0") {
		t.Errorf("the second developer must get 1.0.0:\n%s", cm)
	}

	// control: without a lock the newest version is chosen
	fresh := filepath.Join(t.TempDir(), "no-lock")
	write(t, filepath.Join(fresh, "kiln.json"), readFile(t, filepath.Join(project, "kiln.json")))
	mustRun(t, e.cmd("install", fresh)...)
	if got := lockedVersions(t, fresh)["v/dep"]; got != "1.1.0" {
		t.Errorf("without a lock the newest version is chosen, got %s", got)
	}

	// update moves to the newest and rewrites the lock
	out = mustRun(t, e.cmd("update", project)...)
	if !strings.Contains(out, "rewritten") || lockedVersions(t, project)["v/dep"] != "1.1.0" || readFile(t, lockPath) == first {
		t.Errorf("update must rewrite the lock:\n%s", out)
	}
}

func TestStaleLock(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/dep", "1.0.0")
	e.release("v/other", "1.0.0")
	project := e.project(`"v/dep":"^1.0"`)
	mustRun(t, e.cmd("install", project)...)

	// changes that do not touch the dependencies keep the lock valid
	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/renamed","description":"now with a description","require":{"v/dep":"^1.0"}}`)
	mustRun(t, e.cmd("install", project)...)

	// a new dependency makes it stale
	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"v/dep":"^1.0","v/other":"^1.0"}}`)
	code, _, errs := runCLI(e.cmd("install", project)...)
	if code != exitResolution || !strings.Contains(errs, "out of date") || !strings.Contains(errs, "kiln update") {
		t.Fatalf("%d\n%s", code, errs)
	}
	// so does a changed constraint
	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"v/dep":"^1.0.0"}}`)
	if code, _, _ := runCLI(e.cmd("install", project)...); code != exitResolution {
		t.Errorf("changed constraint: %d", code)
	}
	// update fixes it
	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"v/dep":"^1.0","v/other":"^1.0"}}`)
	mustRun(t, e.cmd("update", project)...)
	mustRun(t, e.cmd("install", project)...)
	if lockedVersions(t, project)["v/other"] != "1.0.0" {
		t.Error("the new dependency must be locked")
	}
}

func TestUpdateOnlyChangesTheNamedPackages(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/a", "1.0.0")
	e.release("v/b", "1.0.0")
	project := e.project(`"v/a":"^1.0","v/b":"^1.0"`)
	mustRun(t, e.cmd("install", project)...)
	e.release("v/a", "1.1.0")
	e.release("v/b", "1.1.0")

	mustRun(t, e.cmd("update", project, "v/a")...)
	got := lockedVersions(t, project)
	if got["v/a"] != "1.1.0" || got["v/b"] != "1.0.0" {
		t.Errorf("only v/a may move: %v", got)
	}
	if code, _, errs := runCLI(e.cmd("update", project, "v/ghost")...); code == 0 || !strings.Contains(errs, "not in kiln.lock") {
		t.Errorf("unknown package: %d %s", code, errs)
	}
	fresh := e.project(`"v/a":"^1.0"`)
	os.Remove(filepath.Join(fresh, "kiln.lock"))
	if code, _, errs := runCLI(e.cmd("update", fresh, "v/a")...); code == 0 || !strings.Contains(errs, "no kiln.lock to update from") {
		t.Errorf("update <pkg> without a lock: %d %s", code, errs)
	}
}

func TestLockedFlag(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/dep", "1.0.0")
	project := e.project(`"v/dep":"^1.0"`)
	code, _, errs := runCLI(e.cmd("install", project, "--locked")...)
	if code != exitResolution || !strings.Contains(errs, "there is no kiln.lock") {
		t.Fatalf("%d %s", code, errs)
	}
	if _, err := os.Stat(filepath.Join(project, "kiln.lock")); err == nil {
		t.Error("--locked must never create a lock")
	}
	mustRun(t, e.cmd("install", project)...)
	mustRun(t, e.cmd("install", project, "--locked")...)
	if code, _, _ := runCLI(e.cmd("update", project, "--locked")...); code != exitUsage {
		t.Errorf("update --locked: %d", code)
	}
}

func TestLockedVersionNoLongerAvailable(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/dep", "1.0.0")
	e.release("v/dep", "1.1.0")
	project := e.project(`"v/dep":"1.0.0"`)
	mustRun(t, e.cmd("install", project)...)
	e.reg.mu.Lock()
	delete(e.reg.pkgs["v/dep"], "1.0.0")
	e.reg.mu.Unlock()
	code, _, errs := runCLI(e.cmd("install", project)...)
	if code != exitResolution || !strings.Contains(errs, "pins v/dep 1.0.0") || !strings.Contains(errs, "no longer offers") {
		t.Errorf("%d\n%s", code, errs)
	}
}

func TestMovedCommitIsRefused(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/dep", "1.0.0")
	e.reg.commits["v/dep@1.0.0"] = strings.Repeat("1", 40)
	project := e.project(`"v/dep":"^1.0"`)
	mustRun(t, e.cmd("install", project)...)
	if !strings.Contains(readFile(t, filepath.Join(project, "kiln.lock")), strings.Repeat("1", 40)) {
		t.Fatal("the lock must record the commit")
	}

	e.reg.commits["v/dep@1.0.0"] = strings.Repeat("2", 40) // the tag was moved
	code, _, errs := runCLI(e.cmd("install", project)...)
	if code != exitVerify || !strings.Contains(errs, "pins commit 111111111111") || !strings.Contains(errs, "now says 222222222222") {
		t.Fatalf("%d\n%s", code, errs)
	}
	// the user can accept it knowingly
	mustRun(t, e.cmd("update", project, "v/dep")...)
	if !strings.Contains(readFile(t, filepath.Join(project, "kiln.lock")), strings.Repeat("2", 40)) {
		t.Error("update must record the new commit")
	}
	mustRun(t, e.cmd("install", project)...)
}

func TestInvalidLockFile(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/dep", "1.0.0")
	project := e.project(`"v/dep":"^1.0"`)
	write(t, filepath.Join(project, "kiln.lock"), "{ not json")
	code, _, errs := runCLI(e.cmd("install", project)...)
	if code != exitError || !strings.Contains(errs, "kiln.lock is not valid") || !strings.Contains(errs, "kiln update") {
		t.Errorf("%d\n%s", code, errs)
	}
	mustRun(t, e.cmd("update", project)...) // update replaces a broken lock
}

func TestShortAndLongFlags(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/dep", "1.0.0")
	project := e.project(`"v/dep":"^1.0"`)

	// long, short and (Go's own) single-dash long forms all work
	for _, args := range [][]string{
		{"install", "--registry", e.reg.srv.URL, "--dir", project, "--local", e.repo, "--setting", "os=linux", "--setting", "arch=x86_64", "--setting", "compiler.name=gcc", "--setting", "compiler.version=13", "--setting", "stdlib=libstdc++", "--setting", "libc=glibc", "--setting", "cxx11_abi=true", "--binary-only", "--jobs", "2"},
		{"install", "--registry=" + e.reg.srv.URL, "-d", project, "--local=" + e.repo, "-s", "os=linux", "-s", "arch=x86_64", "-s", "compiler.name=gcc", "-s", "compiler.version=13", "-s", "stdlib=libstdc++", "-s", "libc=glibc", "-s", "cxx11_abi=true", "-j", "2"},
		{"install", "-registry", e.reg.srv.URL, "-dir", project, "-local", e.repo, "-binary-only", "-s", "os=linux", "-s", "arch=x86_64", "-s", "compiler.name=gcc", "-s", "compiler.version=13", "-s", "stdlib=libstdc++", "-s", "libc=glibc", "-s", "cxx11_abi=true"},
	} {
		mustRun(t, args...)
	}
	code, out, errs := runCLI("install", "--option", "malformed")
	if code != exitUsage || out != "" {
		t.Errorf("--option malformed: %d %s %s", code, out, errs)
	}
	if code, _, errs := runCLI("install", "-o", "malformed"); code != exitUsage || !strings.Contains(errs, "vendor/pkg:option=value") {
		t.Errorf("-o malformed: %d %s", code, errs)
	}
}

func TestHelpShowsShortAndLongFlags(t *testing.T) {
	code, _, errs := runCLI("install", "-h")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"-s, --setting key=value", "-o, --option vendor/pkg:option=value", "-d, --dir DIR", "-j, --jobs N", "-v, --verbose", "--binary-only", "--locked", "--registry URL"} {
		if !strings.Contains(errs, want) {
			t.Errorf("help is missing %q:\n%s", want, errs)
		}
	}
	// no single-dash long flags in the help
	for _, bad := range []string{"-binary-only", "-registry"} {
		for _, line := range strings.Split(errs, "\n") {
			if strings.Contains(line, bad) && !strings.Contains(line, "--"+strings.TrimPrefix(bad, "-")) {
				t.Errorf("help shows %q with one dash: %s", bad, line)
			}
		}
	}
	if code, _, errs := runCLI("update", "-h"); code != 0 || !strings.Contains(errs, "kiln update [vendor/pkg...]") {
		t.Errorf("update -h: %d %s", code, errs)
	}
}

// publishConfigs makes name@version available as prebuilt binaries for several
// build configurations (each a list of extra -s flags on top of fixedSettings).
func (e *lockEnv) publishConfigs(name, version, extra string, configs ...[]string) {
	e.t.Helper()
	m := libManifest(name, extra)
	e.reg.add(name, version, m)
	dir := filepath.Join(e.work, "pkg", strings.ReplaceAll(name, "/", "_"))
	write(e.t, filepath.Join(dir, "kiln.json"), m)
	lib := name[strings.Index(name, "/")+1:]
	for i, cfg := range configs {
		prefix := filepath.Join(e.t.TempDir(), "prefix")
		write(e.t, filepath.Join(prefix, "include", lib+".h"), "// "+lib+"\n")
		write(e.t, filepath.Join(prefix, "lib", "lib"+lib+".a"), "!<arch>\n")
		dist := filepath.Join(e.t.TempDir(), "dist"+string(rune('a'+i)))
		mustRun(e.t, append(append([]string{"package", "--registry", e.reg.srv.URL, "-d", dir, "--prefix", prefix, "--version", version, "--out", dist}, fixedSettings...), cfg...)...)
		mustRun(e.t, "publish", "-d", dir, "--dist", dist, "--version", version, "--local", e.repo)
	}
	mustRun(e.t, "finalize", "-d", dir, "--version", version, "--local", e.repo, "--commit", commit)
}

var (
	cfgLinux   = []string(nil)
	cfgWindows = []string{"-s", "os=windows", "-s", "libc=ucrt"}
)

func (e *lockEnv) installAs(cfg []string, project string, extra ...string) string {
	e.t.Helper()
	args := append([]string{"install", "--registry", e.reg.srv.URL, "--dir", project, "--local", e.repo}, fixedSettings...)
	return mustRun(e.t, append(append(args, cfg...), extra...)...)
}

// The principle: the lock locks versions, not the environment. Two developers on
// different platforms, each starting from nothing, get the same file.
func TestALinuxAndAWindowsMachineWriteTheSameLock(t *testing.T) {
	e := newLockEnv(t)
	e.publishConfigs("v/dep", "1.0.0", "", cfgLinux, cfgWindows)
	e.publishConfigs("v/winonly", "1.0.0", "", cfgLinux, cfgWindows)
	manifestJSON := `{"name":"me/project","require":{"v/dep":"^1.0","v/winonly":{"version":"^1.0","platforms":["windows"]}}}`

	linux := filepath.Join(t.TempDir(), "linux")
	windows := filepath.Join(t.TempDir(), "windows")
	for _, d := range []string{linux, windows} {
		write(t, filepath.Join(d, "kiln.json"), manifestJSON)
	}
	t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "linux-cache"))
	e.installAs(cfgLinux, linux)
	t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "windows-cache"))
	e.installAs(cfgWindows, windows)

	linuxLock, windowsLock := readFile(t, filepath.Join(linux, "kiln.lock")), readFile(t, filepath.Join(windows, "kiln.lock"))
	if linuxLock != windowsLock {
		t.Fatalf("the two platforms wrote different lockfiles:\n--- linux\n%s\n--- windows\n%s", linuxLock, windowsLock)
	}
	for _, machineSpecific := range []string{`"os"`, `"linux"`, `"windows"`, `"compiler"`, `"gcc"`, `"settings"`, `"id"`, `"origin"`, `"file"`} {
		if strings.Contains(linuxLock, machineSpecific) {
			t.Errorf("the lock mentions %s, which is about the machine:\n%s", machineSpecific, linuxLock)
		}
	}
	// the Windows-only package is locked on Linux too...
	if got := lockedVersions(t, linux); got["v/winonly"] != "1.0.0" || got["v/dep"] != "1.0.0" {
		t.Errorf("the lock must pin the packages of every platform: %v", got)
	}
	// ...but only installed where it applies
	linuxCmake := readFile(t, filepath.Join(linux, "vendor", "kiln", "kiln.cmake"))
	windowsCmake := readFile(t, filepath.Join(windows, "vendor", "kiln", "kiln.cmake"))
	if strings.Contains(linuxCmake, "v::winonly") || !strings.Contains(linuxCmake, "v::dep") {
		t.Errorf("linux must install dep only:\n%s", linuxCmake)
	}
	if !strings.Contains(windowsCmake, "v::winonly") || !strings.Contains(windowsCmake, "v::dep") {
		t.Errorf("windows must install both:\n%s", windowsCmake)
	}

	// a new release does not matter to either of them, and sharing the file works in both directions
	e.publishConfigs("v/dep", "1.1.0", "", cfgLinux, cfgWindows)
	write(t, filepath.Join(windows, "kiln.lock"), linuxLock)
	t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "windows-cache-2"))
	e.installAs(cfgWindows, windows)
	if !strings.Contains(readFile(t, filepath.Join(windows, "vendor", "kiln", "kiln.cmake")), "# v/dep 1.0.0") {
		t.Error("the Windows machine must install the version the Linux machine locked")
	}
	if readFile(t, filepath.Join(windows, "kiln.lock")) != linuxLock {
		t.Error("install must leave the shared lock alone")
	}

	// an update by either platform changes the file in the same way
	e.installAs(cfgLinux, linux) // (still the locked version, from the cache)
	mustRun(t, append(append([]string{"update", "--registry", e.reg.srv.URL, "--dir", linux, "--local", e.repo}, fixedSettings...), cfgLinux...)...)
	mustRun(t, append(append([]string{"update", "--registry", e.reg.srv.URL, "--dir", windows, "--local", e.repo}, fixedSettings...), cfgWindows...)...)
	if readFile(t, filepath.Join(linux, "kiln.lock")) != readFile(t, filepath.Join(windows, "kiln.lock")) {
		t.Error("`update` on two platforms must give the same lockfile")
	}
	if lockedVersions(t, linux)["v/dep"] != "1.1.0" {
		t.Error("update must move to the new release")
	}
}

// Other build settings on the same platform do not touch the lock either.
func TestTheLockDoesNotChangeWithTheBuildSettings(t *testing.T) {
	e := newLockEnv(t)
	cpp20 := []string{"-s", "cpp_standard=20"}
	debug := []string{"-s", "build_type=debug"}
	e.publishConfigs("v/dep", "1.0.0", "", cfgLinux, cpp20, debug)

	var locks []string
	for i, cfg := range [][]string{cfgLinux, cpp20, debug} {
		project := filepath.Join(t.TempDir(), "p")
		write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"v/dep":"^1.0"}}`)
		t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "cache"+string(rune('a'+i))))
		e.installAs(cfg, project)
		locks = append(locks, readFile(t, filepath.Join(project, "kiln.lock")))
	}
	if locks[0] != locks[1] || locks[0] != locks[2] {
		t.Errorf("the lock depends on the build settings:\n%s\n---\n%s\n---\n%s", locks[0], locks[1], locks[2])
	}
}

func TestOldFormatLockIsExplained(t *testing.T) {
	e := newLockEnv(t)
	e.release("v/dep", "1.0.0")
	project := e.project(`"v/dep":"^1.0"`)
	write(t, filepath.Join(project, "kiln.lock"), `{"lock-version":1,"content-hash":"x","settings":{"os":"linux"},"packages":[]}`)
	code, _, errs := runCLI(e.cmd("install", project)...)
	if code != exitError || !strings.Contains(errs, "older kiln") || !strings.Contains(errs, "kiln update") {
		t.Errorf("%d\n%s", code, errs)
	}
	mustRun(t, e.cmd("update", project)...)
	if !strings.Contains(readFile(t, filepath.Join(project, "kiln.lock")), `"lock-version": 2`) {
		t.Error("update must write the new format")
	}
	mustRun(t, e.cmd("install", project)...)
}
