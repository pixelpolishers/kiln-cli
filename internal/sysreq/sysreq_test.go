package sysreq

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pixelpolishers/kiln-core/manifest"
	"github.com/pixelpolishers/kiln-core/when"
)

// machine fakes a computer with some tools, and probes that succeed or fail by their text.
func machine(tools []string, okProbes []string, platform Platform) Env {
	has := map[string]bool{}
	for _, t := range tools {
		has[t] = true
	}
	ok := map[string]bool{}
	for _, p := range okProbes {
		ok[p] = true
	}
	return Env{
		LookPath: func(n string) (string, error) {
			if has[n] {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		},
		Probe: func(argv []string) error {
			if ok[strings.Join(argv, " ")] {
				return nil
			}
			return errors.New("exit status 1")
		},
		Platform: platform,
	}
}

var fedora = Platform{OS: "linux", Distro: []string{"fedora"}}

func TestToolCheckProbeAndPkgConfig(t *testing.T) {
	reqs := []manifest.Requirement{
		{Name: "perl", Tool: "perl"},
		{Name: "make", Tool: "make"},
		{Name: "perl modules", Check: []string{"perl", "-MFindBin", "-e1"}},
		{Name: "perl module Missing", Check: []string{"perl", "-MNo::Such", "-e1"}},
		{Name: "x11", PkgConfig: "x11"},
		{Name: "wayland", PkgConfig: "wayland-client"},
	}
	env := machine([]string{"perl", "pkg-config"}, []string{"perl -MFindBin -e1", "pkg-config --exists x11"}, fedora)
	got := Check(reqs, nil, env)
	status := map[string]bool{}
	for _, r := range got {
		status[r.Req.Name] = r.OK
	}
	want := map[string]bool{"perl": true, "make": false, "perl modules": true, "perl module Missing": false, "x11": true, "wayland": false}
	if !reflect.DeepEqual(status, want) {
		t.Errorf("got %v\nwant %v", status, want)
	}
	for _, r := range got {
		if !r.OK && r.Reason == "" {
			t.Errorf("%s failed without a reason", r.Req.Name)
		}
	}
}

func TestPkgConfigMissingIsExplained(t *testing.T) {
	r := Check([]manifest.Requirement{{Name: "x11", PkgConfig: "x11"}}, nil, machine(nil, nil, fedora))
	if len(r) != 1 || r[0].OK || !strings.Contains(r[0].Reason, "pkg-config is not installed") {
		t.Errorf("%+v", r)
	}
}

func TestWhenFiltersRequirements(t *testing.T) {
	reqs := []manifest.Requirement{
		{Name: "make", Tool: "make", When: map[string]any{"os": []any{"linux", "macos"}}},
		{Name: "nmake", Tool: "nmake", When: map[string]any{"os": "windows"}},
	}
	ctx := when.Context(map[string]any{"os": "linux"}, nil)
	got := Check(reqs, ctx, machine(nil, nil, fedora))
	if len(got) != 1 || got[0].Req.Name != "make" {
		t.Errorf("only the requirement for this os applies: %+v", got)
	}
}

func TestHints(t *testing.T) {
	h := manifest.Hint{Default: "install it", By: map[string]string{
		"fedora": "sudo dnf install x", "debian": "sudo apt install x", "macos": "brew install x",
	}}
	cases := []struct {
		p    Platform
		want string
	}{
		{Platform{OS: "linux", Distro: []string{"fedora"}}, "sudo dnf install x"},
		{Platform{OS: "linux", Distro: []string{"ubuntu", "debian"}}, "sudo apt install x"}, // ID_LIKE fallback
		{Platform{OS: "linux", Distro: []string{"rhel", "fedora"}}, "sudo dnf install x"},
		{Platform{OS: "macos"}, "brew install x"}, // by operating system
		{Platform{OS: "linux", Distro: []string{"arch"}}, "install it"},
		{Platform{OS: "freebsd"}, "install it"},
	}
	for _, c := range cases {
		if got := hintFor(h, c.p); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.p, got, c.want)
		}
	}
	if hintFor(manifest.Hint{}, fedora) != "" {
		t.Error("no hint must give an empty string")
	}
}

func TestDistro(t *testing.T) {
	dir := t.TempDir()
	write := func(content string) string {
		p := filepath.Join(dir, "os-release")
		os.WriteFile(p, []byte(content), 0o644)
		return p
	}
	if got := Distro(write("NAME=\"Ubuntu\"\nID=ubuntu\nID_LIKE=\"debian\"\nVERSION_ID=\"24.04\"\n")); !reflect.DeepEqual(got, []string{"ubuntu", "debian"}) {
		t.Errorf("%v", got)
	}
	if got := Distro(write("ID=fedora\n")); !reflect.DeepEqual(got, []string{"fedora"}) {
		t.Errorf("%v", got)
	}
	if got := Distro(filepath.Join(dir, "missing")); got != nil {
		t.Errorf("%v", got)
	}
}

func TestImplicit(t *testing.T) {
	names := func(m manifest.Package) []string {
		var out []string
		for _, r := range Implicit(m) {
			out = append(out, r.Tool)
		}
		return out
	}
	if got := names(manifest.Package{Build: &manifest.Build{System: "cmake"}}); !reflect.DeepEqual(got, []string{"git", "cmake"}) {
		t.Errorf("cmake: %v", got)
	}
	if got := names(manifest.Package{Build: &manifest.Build{System: "custom"}}); !reflect.DeepEqual(got, []string{"git"}) {
		t.Errorf("custom: %v", got)
	}
	// the implicit git requirement carries a hint for the common platforms
	env := machine(nil, nil, Platform{OS: "linux", Distro: []string{"ubuntu", "debian"}})
	r := Check(Implicit(manifest.Package{}), nil, env)
	if len(r) != 1 || r[0].Hint != "sudo apt install git" {
		t.Errorf("%+v", r)
	}
}

func TestFormatAndSplit(t *testing.T) {
	fs := []Failure{
		{Package: "openssl/openssl", Version: "3.3.2", Result: Result{Req: manifest.Requirement{Name: "Perl core modules"}, Reason: "`perl -MFindBin -e1` failed", Hint: "sudo dnf install perl-core"}},
		{Package: "openssl/openssl", Version: "3.3.2", Result: Result{Req: manifest.Requirement{Name: "make"}, Reason: `"make" was not found on PATH`}},
		{Package: "libsdl-org/sdl", Version: "2.30.8", Result: Result{Req: manifest.Requirement{Name: "X11 headers", Optional: true}, Reason: `pkg-config does not know "x11"`, Hint: "sudo dnf install libX11-devel"}},
	}
	req, opt := Split(fs)
	if len(req) != 2 || len(opt) != 1 {
		t.Fatalf("%d required, %d optional", len(req), len(opt))
	}
	out := Format(req)
	for _, want := range []string{"openssl/openssl 3.3.2", "missing: Perl core modules", "to fix:  sudo dnf install perl-core", "missing: make"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "libsdl-org") {
		t.Errorf("optional failures are not part of the required report:\n%s", out)
	}
	if strings.Contains(strings.SplitAfter(out, "make")[1], "to fix") {
		t.Errorf("a requirement without a hint must not print an empty 'to fix' line:\n%s", out)
	}
}

func TestFileAndEnvRequirements(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Include", "vulkan"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Include", "vulkan", "vulkan.h"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	env := machine(nil, nil, Platform{OS: "windows"})
	env.Getenv = func(k string) string {
		if k == "VULKAN_SDK" {
			return dir
		}
		return ""
	}
	cases := []struct {
		name string
		req  manifest.Requirement
		ok   bool
		why  string
	}{
		{"file via a variable", manifest.Requirement{Name: "Vulkan headers", File: "${VULKAN_SDK}/Include/vulkan/vulkan.h"}, true, ""},
		{"file glob", manifest.Requirement{Name: "Vulkan", File: "$VULKAN_SDK/Include/*/vulkan.h"}, true, ""},
		{"file missing", manifest.Requirement{Name: "GL", File: "${VULKAN_SDK}/Include/GL/gl.h"}, false, "nothing exists at " + dir},
		{"file with unset variable", manifest.Requirement{Name: "x", File: "${NOPE}/x.h"}, false, "nothing exists at /x.h"},
		{"env set", manifest.Requirement{Name: "SDK", Env: "VULKAN_SDK"}, true, ""},
		{"env unset", manifest.Requirement{Name: "SDK", Env: "OTHER_SDK"}, false, "OTHER_SDK is not set"},
	}
	for _, c := range cases {
		res := Check([]manifest.Requirement{c.req}, nil, env)
		if len(res) != 1 || res[0].OK != c.ok || !strings.Contains(res[0].Reason, c.why) {
			t.Errorf("%s: %+v", c.name, res)
		}
	}
}

func TestOneManifestCanAskDifferentThingsPerPlatform(t *testing.T) {
	reqs := []manifest.Requirement{
		{Name: "GTK 3", PkgConfig: "gtk+-3.0", When: map[string]any{"os": "linux"}},
		{Name: "Windows SDK", Env: "WindowsSdkDir", When: map[string]any{"os": "windows"}},
	}
	linux := machine(nil, nil, fedora)
	win := machine(nil, nil, Platform{OS: "windows"})
	if res := Check(reqs, map[string]string{"os": "linux"}, linux); len(res) != 1 || res[0].Req.Name != "GTK 3" {
		t.Errorf("linux: %+v", res)
	}
	if res := Check(reqs, map[string]string{"os": "windows"}, win); len(res) != 1 || res[0].Req.Name != "Windows SDK" {
		t.Errorf("windows: %+v", res)
	}
	if res := Check(reqs, map[string]string{"os": "macos"}, win); len(res) != 0 {
		t.Errorf("macos needs neither: %+v", res)
	}
}

func TestUseRequirementsAreLabelled(t *testing.T) {
	out := Format([]Failure{{Package: "a/b", Version: "1.0.0", Use: true, Result: Result{Req: manifest.Requirement{Name: "GTK 3"}, Reason: "no"}}})
	if !strings.Contains(out, "missing: GTK 3 (needed to use the package)") {
		t.Errorf("%s", out)
	}
}
