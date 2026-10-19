package plan

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/pixelpolishers/kiln-core/manifest"
	"github.com/pixelpolishers/kiln-core/options"
	"github.com/pixelpolishers/kiln-core/semver"
	"github.com/pixelpolishers/kiln-core/solver"
)

type fakeSource map[string]manifest.Package // key name@version

func (f fakeSource) Manifest(_ context.Context, name string, v semver.Version) (manifest.Package, string, error) {
	m, ok := f[name+"@"+v.String()]
	if !ok {
		return manifest.Package{}, "", fmt.Errorf("no manifest for %s@%s", name, v)
	}
	return m, "c0ffee", nil
}

func ver(s string) semver.Version { v, _ := semver.Parse(s); return v }

func solution(pkgs ...solver.Package) solver.Solution { return solver.Solution{Packages: pkgs} }

var settings = map[string]any{"os": "linux", "arch": "x86_64", "build_type": "release", "linkage": "static", "stdlib": "libstdc++", "cpp_standard": "17"}

func lib() manifest.Package {
	return manifest.Package{Targets: map[string]manifest.Target{"t": {Type: "library"}}}
}

func idOf(t *testing.T, p Plan, name string) string {
	t.Helper()
	pkg, ok := p.Get(name)
	if !ok {
		t.Fatalf("%s missing from the plan", name)
	}
	return pkg.ID
}

func build(t *testing.T, src fakeSource, sol solver.Solution, settings map[string]any, reqs ...options.Request) (Plan, error) {
	t.Helper()
	return Build(context.Background(), src, Input{Solution: sol, Settings: settings, Requests: reqs})
}

func TestDependencyBuildIsPartOfTheID(t *testing.T) {
	app := lib()
	app.Require = map[string]manifest.Dependency{"v/dep": {Version: "^1.0"}}
	src := fakeSource{"v/app@1.0.0": app, "v/dep@1.0.0": lib(), "v/dep@1.0.1": lib()}

	p1, err := build(t, src, solution(
		solver.Package{Name: "v/dep", Version: ver("1.0.0")},
		solver.Package{Name: "v/app", Version: ver("1.0.0"), Requires: []string{"v/dep"}}), settings)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := build(t, src, solution(
		solver.Package{Name: "v/dep", Version: ver("1.0.1")},
		solver.Package{Name: "v/app", Version: ver("1.0.0"), Requires: []string{"v/dep"}}), settings)
	if err != nil {
		t.Fatal(err)
	}
	if idOf(t, p1, "v/dep") == idOf(t, p2, "v/dep") {
		t.Error("a new dependency version is a new binary")
	}
	if idOf(t, p1, "v/app") == idOf(t, p2, "v/app") {
		t.Error("the dependent's id must change when its dependency changes, even though the constraint ^1.0 still holds")
	}
	if p1.Packages[0].Name != "v/dep" {
		t.Error("dependencies first")
	}
}

func TestSettingsReachOnlyWhereTheyMatter(t *testing.T) {
	hdr := manifest.Package{Targets: map[string]manifest.Target{"t": {Type: "header-only"}}}
	c := lib()
	c.Language = "c"
	cxx := lib()
	src := fakeSource{"h/h@1.0.0": hdr, "c/c@1.0.0": c, "x/x@1.0.0": cxx}
	sol := solution(solver.Package{Name: "h/h", Version: ver("1.0.0")}, solver.Package{Name: "c/c", Version: ver("1.0.0")}, solver.Package{Name: "x/x", Version: ver("1.0.0")})

	debug20 := map[string]any{}
	for k, v := range settings {
		debug20[k] = v
	}
	debug20["build_type"], debug20["cpp_standard"] = "debug", "20"

	a, _ := build(t, src, sol, settings)
	b, _ := build(t, src, sol, debug20)
	if idOf(t, a, "h/h") != idOf(t, b, "h/h") {
		t.Error("a header-only package must have one id under any settings")
	}
	if idOf(t, a, "c/c") == idOf(t, b, "c/c") {
		t.Error("a C library still depends on the build type")
	}
	only20 := map[string]any{}
	for k, v := range settings {
		only20[k] = v
	}
	only20["cpp_standard"] = "20"
	c1, _ := build(t, src, sol, settings)
	c2, _ := build(t, src, sol, only20)
	if idOf(t, c1, "c/c") != idOf(t, c2, "c/c") {
		t.Error("a C library must not depend on the C++ standard")
	}
	if idOf(t, c1, "x/x") == idOf(t, c2, "x/x") {
		t.Error("a C++ library does depend on the C++ standard")
	}
}

func TestOptionsFlowFromDependents(t *testing.T) {
	dep := lib()
	dep.Options = map[string]manifest.Option{"fast": {Default: false}}
	a := lib()
	a.Require = map[string]manifest.Dependency{"v/dep": {Version: "*", Options: map[string]any{"fast": true}}}
	b := lib()
	b.Require = map[string]manifest.Dependency{"v/dep": {Version: "*", Options: map[string]any{"fast": false}}}
	src := fakeSource{"v/dep@1.0.0": dep, "v/a@1.0.0": a, "v/b@1.0.0": b}
	pk := func(names ...string) solver.Solution {
		s := solver.Solution{Packages: []solver.Package{{Name: "v/dep", Version: ver("1.0.0")}}}
		for _, n := range names {
			s.Packages = append(s.Packages, solver.Package{Name: n, Version: ver("1.0.0"), Requires: []string{"v/dep"}})
		}
		return s
	}

	p, err := build(t, src, pk("v/a"), settings)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := p.Get("v/dep"); d.Options["fast"] != true {
		t.Errorf("dependent's option request ignored: %v", d.Options)
	}
	_, err = build(t, src, pk("v/a", "v/b"), settings)
	var oe *OptionsError
	if err == nil || !strings.Contains(err.Error(), "conflicting values") {
		t.Fatalf("%v", err)
	}
	_ = oe
	p, err = build(t, src, pk("v/a", "v/b"), settings, options.Request{Package: "v/dep", Option: "fast", Raw: "true", From: "-o", Root: true})
	if err != nil {
		t.Fatalf("root override must settle the conflict: %v", err)
	}
	if d, _ := p.Get("v/dep"); d.Options["fast"] != true {
		t.Errorf("%v", d.Options)
	}
	// and the option is part of the id
	q, _ := build(t, src, pk(), settings)
	if idOf(t, p, "v/dep") == idOf(t, q, "v/dep") {
		t.Error("options are part of the id")
	}
}

func TestKindOf(t *testing.T) {
	cases := []struct {
		m          manifest.Package
		lang       string
		headerOnly bool
	}{
		{manifest.Package{}, "c++", false}, // no targets: not header-only
		{manifest.Package{Targets: map[string]manifest.Target{"a": {Type: "header-only"}}}, "c++", true},
		{manifest.Package{Targets: map[string]manifest.Target{"a": {Type: "header-only"}, "b": {Type: "library"}}}, "c++", false},
		{manifest.Package{Language: "c", Targets: map[string]manifest.Target{"a": {Type: "library"}}}, "c", false},
	}
	for i, c := range cases {
		if k := KindOf(c.m); k.Language != c.lang || k.HeaderOnly != c.headerOnly {
			t.Errorf("case %d: %+v", i, k)
		}
	}
}
