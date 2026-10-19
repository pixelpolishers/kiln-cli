package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func needTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitProject creates a git repository holding files, tagged tag, and returns
// its file:// URL and the tagged commit.
func gitProject(t *testing.T, tag string, files map[string]string) (url, commit string) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		write(t, filepath.Join(dir, name), content)
	}
	gitRun(t, dir, "init", "-q", "-b", "main")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "release")
	gitRun(t, dir, "tag", tag)
	d := filepath.ToSlash(dir)
	if !strings.HasPrefix(d, "/") {
		d = "/" + d // Windows: file:///C:/dir
	}
	return "file://" + d, gitRun(t, dir, "rev-parse", "HEAD")
}

var tinyFiles = map[string]string{
	"CMakeLists.txt": `cmake_minimum_required(VERSION 3.15)
project(tiny CXX)
set(TINY_VALUE 1 CACHE STRING "value baked into the library")
add_library(tiny src/tiny.cc)
target_include_directories(tiny PUBLIC $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include> $<INSTALL_INTERFACE:include>)
target_compile_definitions(tiny PRIVATE TINY_VALUE=${TINY_VALUE})
install(TARGETS tiny EXPORT tiny-targets ARCHIVE DESTINATION lib)
install(EXPORT tiny-targets NAMESPACE tiny:: DESTINATION lib/cmake/tiny)
file(WRITE ${CMAKE_CURRENT_BINARY_DIR}/tiny-config.cmake "include(\"\${CMAKE_CURRENT_LIST_DIR}/tiny-targets.cmake\")\n")
install(FILES ${CMAKE_CURRENT_BINARY_DIR}/tiny-config.cmake DESTINATION lib/cmake/tiny)
install(FILES include/tiny.h DESTINATION include)
`,
	"include/tiny.h": "#pragma once\nint tiny_value();\n",
	"src/tiny.cc":    "#include <tiny.h>\nint tiny_value() { return TINY_VALUE; }\n",
}

// app depends on tiny and finds it through find_package, which only works if
// the build was given the install prefix of the already built dependency.
var appFiles = map[string]string{
	"CMakeLists.txt": `cmake_minimum_required(VERSION 3.15)
project(app CXX)
find_package(tiny CONFIG REQUIRED)
add_library(app src/app.cc)
target_include_directories(app PUBLIC $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include> $<INSTALL_INTERFACE:include>)
target_link_libraries(app PUBLIC tiny::tiny)
install(TARGETS app ARCHIVE DESTINATION lib)
install(FILES include/app.h DESTINATION include)
`,
	"include/app.h": "#pragma once\nint app_value();\n",
	"src/app.cc":    "#include <app.h>\n#include <tiny.h>\nint app_value() { return tiny_value() + 100; }\n",
}

func sourceManifests(tinyURL, appURL string) (tiny, app string) {
	tiny = `{"name":"v/tiny","repository":{"type":"git","url":"` + tinyURL + `"},
	  "targets":{"tiny":{"type":"library","include":["include"]}},
	  "build":{"system":"cmake","options":{"TINY_VALUE":7}},
	  "providers":[{"type":"github","tag":"v{version}"}]}`
	app = `{"name":"v/app","repository":{"type":"git","url":"` + appURL + `"},
	  "require":{"v/tiny":"^1.0"},
	  "targets":{"app":{"type":"library","include":["include"],"requires":["v/tiny"]}},
	  "build":{"system":"cmake"},
	  "providers":[{"type":"github","tag":"v{version}"}]}`
	return
}

func TestInstallBuildsFromSource(t *testing.T) {
	needTools(t, "git", "cmake", "g++")
	t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("KILN_ALLOW_FILE_SOURCES", "1")

	tinyURL, tinyCommit := gitProject(t, "v1.0.0", tinyFiles)
	appURL, appCommit := gitProject(t, "v1.0.0", appFiles)
	tinyM, appM := sourceManifests(tinyURL, appURL)
	reg := newManifestRegistry(t, map[string]map[string]string{"v/tiny": {"1.0.0": tinyM}, "v/app": {"1.0.0": appM}})
	reg.commits["v/tiny@1.0.0"], reg.commits["v/app@1.0.0"] = tinyCommit, appCommit // pinned, and the tags agree

	project := t.TempDir()
	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"v/app":"^1.0"}}`)
	// real settings: a source build only runs when the settings are this machine's toolchain
	out := mustRun(t, "install", "-registry", reg.srv.URL, "-dir", project, "-s", "build_type=release")
	if strings.Count(out, "built from source") < 2 {
		t.Fatalf("both packages must be built from source:\n%s", out)
	}

	var lock struct {
		Packages []struct {
			Name, Commit string
		}
	}
	raw, _ := os.ReadFile(filepath.Join(project, "kiln.lock"))
	if err := json.Unmarshal(raw, &lock); err != nil || len(lock.Packages) != 2 {
		t.Fatalf("lock: %v %s", err, raw)
	}
	for _, p := range lock.Packages {
		want := map[string]string{"v/tiny": tinyCommit, "v/app": appCommit}[p.Name]
		if p.Commit != want {
			t.Errorf("%s: locked commit %q, want %q", p.Name, p.Commit, want)
		}
	}
	for _, machineSpecific := range []string{"source-build", `"origin"`, `"settings"`, `"id"`} {
		if strings.Contains(string(raw), machineSpecific) {
			t.Errorf("the lock mentions %s, which depends on how this machine built the packages:\n%s", machineSpecific, raw)
		}
	}

	// the second run reuses the build
	again := mustRun(t, "install", "-registry", reg.srv.URL, "-dir", project, "-s", "build_type=release")
	if strings.Count(again, "cached (built from source)") != 2 {
		t.Errorf("second install should reuse the builds:\n%s", again)
	}

	// and the result really links and runs: tiny's option (7) is baked in, app adds 100
	write(t, filepath.Join(project, "main.cpp"), "#include <app.h>\n#include <cstdio>\nint main() { std::printf(\"%d\\n\", app_value()); }\n")
	write(t, filepath.Join(project, "CMakeLists.txt"), `cmake_minimum_required(VERSION 3.15)
project(demo CXX)
include(vendor/kiln/kiln.cmake)
add_executable(demo main.cpp)
target_link_libraries(demo PRIVATE v::app)
`)
	for _, step := range [][]string{{"-S", project, "-B", filepath.Join(project, "build")}, {"--build", filepath.Join(project, "build")}} {
		if o, err := exec.Command("cmake", step...).CombinedOutput(); err != nil {
			t.Fatalf("cmake %v: %v\n%s", step, err, o)
		}
	}
	o, err := exec.Command(filepath.Join(project, "build", "demo")).Output()
	if err != nil || strings.TrimSpace(string(o)) != "107" {
		t.Errorf("program output %q, %v; want 107 (7 from tiny's option + 100 from app)", o, err)
	}
}

func TestSourceBuildPolicies(t *testing.T) {
	needTools(t, "git", "cmake", "g++")
	t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("KILN_ALLOW_FILE_SOURCES", "1")
	tinyURL, commit := gitProject(t, "v1.0.0", tinyFiles)
	tinyM, _ := sourceManifests(tinyURL, tinyURL)
	reg := newManifestRegistry(t, map[string]map[string]string{"v/tiny": {"1.0.0": tinyM}})
	reg.commits["v/tiny@1.0.0"] = commit
	project := t.TempDir()
	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"v/tiny":"^1.0"}}`)
	args := func(extra ...string) []string {
		return append([]string{"install", "-registry", reg.srv.URL, "-dir", project}, extra...)
	}

	if code, _, errs := runCLI(args("-binary-only")...); code != exitNoBinary || !strings.Contains(errs, "turned off") {
		t.Errorf("-binary-only: %d %s", code, errs)
	}
	if code, _, _ := runCLI(args("-binary-only", "-source-only")...); code != exitUsage {
		t.Errorf("exclusive flags: %d", code)
	}
	// the safety check: settings that describe another toolchain must not be built here
	code, _, errs := runCLI(args("-s", "compiler.version=1")...)
	if code != exitNoBinary || !strings.Contains(errs, "cannot build here") || !strings.Contains(errs, `the settings say "gcc 1"`) && !strings.Contains(errs, `the settings say`) {
		t.Errorf("toolchain mismatch: %d %s", code, errs)
	}
	if out := mustRun(t, args()...); !strings.Contains(out, "built from source") {
		t.Errorf("%s", out)
	}
	// -source-only still reuses what was already built
	if out := mustRun(t, args("-source-only")...); !strings.Contains(out, "cached") {
		t.Errorf("%s", out)
	}
}

func TestSourceBuildRefusesMovedTag(t *testing.T) {
	needTools(t, "git", "cmake", "g++")
	t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("KILN_ALLOW_FILE_SOURCES", "1")
	tinyURL, _ := gitProject(t, "v1.0.0", tinyFiles)
	tinyM, _ := sourceManifests(tinyURL, tinyURL)
	reg := newManifestRegistry(t, map[string]map[string]string{"v/tiny": {"1.0.0": tinyM}})
	reg.commits["v/tiny@1.0.0"] = "1111111111111111111111111111111111111111" // the registry pinned another commit
	project := t.TempDir()
	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"v/tiny":"^1.0"}}`)
	code, _, errs := runCLI("install", "-registry", reg.srv.URL, "-dir", project)
	if code != exitNoBinary || !strings.Contains(errs, "was moved") {
		t.Errorf("%d %s", code, errs)
	}
	if entries, _ := filepath.Glob(filepath.Join(os.Getenv("KILN_CACHE_DIR"), "[0-9a-f]*")); len(entries) != 0 {
		t.Errorf("nothing from a moved tag may reach the cache: %v", entries)
	}
}

func TestSourceBuildFailureShowsTheLog(t *testing.T) {
	needTools(t, "git", "cmake", "g++")
	t.Setenv("KILN_CACHE_DIR", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("KILN_ALLOW_FILE_SOURCES", "1")
	broken := map[string]string{
		"CMakeLists.txt": "cmake_minimum_required(VERSION 3.15)\nproject(broken CXX)\nadd_library(broken src/b.cc)\ninstall(TARGETS broken ARCHIVE DESTINATION lib)\n",
		"src/b.cc":       "this is not C++ at all;\n",
	}
	url, commit := gitProject(t, "v1.0.0", broken)
	m := `{"name":"v/broken","repository":{"type":"git","url":"` + url + `"},"targets":{"broken":{"type":"library"}},"build":{"system":"cmake"}}`
	reg := newManifestRegistry(t, map[string]map[string]string{"v/broken": {"1.0.0": m}})
	reg.commits["v/broken@1.0.0"] = commit
	project := t.TempDir()
	write(t, filepath.Join(project, "kiln.json"), `{"name":"me/project","require":{"v/broken":"^1.0"}}`)
	code, _, errs := runCLI("install", "-registry", reg.srv.URL, "-dir", project)
	if code != exitNoBinary || !strings.Contains(errs, "failed") || !strings.Contains(errs, "b.cc") || !strings.Contains(errs, ".log") {
		t.Errorf("%d\n%s", code, errs)
	}
	if left, _ := filepath.Glob(filepath.Join(os.Getenv("KILN_CACHE_DIR"), ".build-*")); len(left) != 0 {
		t.Errorf("build directories left behind: %v", left)
	}
}
