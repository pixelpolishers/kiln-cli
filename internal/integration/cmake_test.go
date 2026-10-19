package integration

import (
	"strings"
	"testing"

	"github.com/pixelpolishers/kiln-core/manifest"
)

func TestCMakeChain(t *testing.T) {
	fmtPkg := Installed{Root: "/c/fmt", Info: manifest.PackageInfo{Name: "fmtlib/fmt", Version: "11.0.2", Targets: map[string]manifest.TargetInfo{
		"fmt":             {Type: "library", Include: []string{"include"}, Libs: []string{"lib/libfmt.a"}},
		"fmt-header-only": {Type: "header-only", Include: []string{"include"}, Defines: map[string]any{"FMT_HEADER_ONLY": true}},
	}}}
	spd := Installed{Root: "/c/spdlog", Info: manifest.PackageInfo{Name: "gabime/spdlog", Version: "1.14.1", Targets: map[string]manifest.TargetInfo{
		"spdlog": {
			Type: "library", Include: []string{"include"}, Libs: []string{"lib/libspdlog.a"},
			Defines:  map[string]any{"SPDLOG_COMPILED_LIB": true, "LEVEL": float64(3), "OFF": false, "BARE": nil},
			Links:    []string{"pthread"},
			Requires: []manifest.TargetRequire{{Package: "fmtlib/fmt", Targets: []string{"fmt"}}},
		},
	}}}
	out, err := CMake([]Installed{fmtPkg, spd})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`add_library(fmtlib::fmt UNKNOWN IMPORTED)`,
		`IMPORTED_LOCATION "/c/fmt/lib/libfmt.a"`,
		`add_library(fmtlib::fmt-header-only INTERFACE IMPORTED)`,
		`INTERFACE_COMPILE_DEFINITIONS "FMT_HEADER_ONLY"`,
		`INTERFACE_INCLUDE_DIRECTORIES "/c/spdlog/include"`,
		`INTERFACE_COMPILE_DEFINITIONS "BARE;LEVEL=3;SPDLOG_COMPILED_LIB"`, // false is dropped, null is a bare define, keys sorted
		`INTERFACE_LINK_LIBRARIES "fmtlib::fmt;pthread"`,                   // only the selected target of fmt, then system libs
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "fmtlib::fmt-header-only;") || strings.Contains(out, `"fmtlib::fmt;fmtlib::fmt-header-only`) {
		t.Errorf("target selection ignored:\n%s", out)
	}
}

func TestCMakeSiblingsAndAllTargets(t *testing.T) {
	dep := Installed{Root: "/c/d", Info: manifest.PackageInfo{Name: "v/dep", Version: "1.0.0", Targets: map[string]manifest.TargetInfo{
		"a": {Type: "header-only"}, "b": {Type: "header-only"},
	}}}
	eng := Installed{Root: "/c/e", Info: manifest.PackageInfo{Name: "acme/engine", Version: "1.0.0", Targets: map[string]manifest.TargetInfo{
		"core": {Type: "library", Libs: []string{"lib/libcore.a"}, Requires: []manifest.TargetRequire{{Package: "v/dep"}}},
		"math": {Type: "library", Libs: []string{"lib/libmath.a"}, Uses: []string{"core"}},
	}}}
	out, err := CMake([]Installed{dep, eng})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `INTERFACE_LINK_LIBRARIES "v::a;v::b"`) || !strings.Contains(out, `INTERFACE_LINK_LIBRARIES "acme::core"`) {
		t.Errorf("%s", out)
	}
}

func TestCMakeErrors(t *testing.T) {
	cases := map[string]Installed{
		"unknown sibling":  {Info: manifest.PackageInfo{Name: "a/a", Targets: map[string]manifest.TargetInfo{"t": {Type: "header-only", Uses: []string{"ghost"}}}}},
		"missing package":  {Info: manifest.PackageInfo{Name: "a/a", Targets: map[string]manifest.TargetInfo{"t": {Type: "header-only", Requires: []manifest.TargetRequire{{Package: "x/y"}}}}}},
		"library no files": {Info: manifest.PackageInfo{Name: "a/a", Targets: map[string]manifest.TargetInfo{"t": {Type: "library"}}}},
	}
	for name, p := range cases {
		if _, err := CMake([]Installed{p}); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	dep := Installed{Info: manifest.PackageInfo{Name: "v/dep", Targets: map[string]manifest.TargetInfo{"a": {Type: "header-only"}}}}
	user := Installed{Info: manifest.PackageInfo{Name: "a/a", Targets: map[string]manifest.TargetInfo{"t": {Type: "header-only", Requires: []manifest.TargetRequire{{Package: "v/dep", Targets: []string{"nope"}}}}}}}
	if _, err := CMake([]Installed{dep, user}); err == nil {
		t.Error("selecting a target the dependency lacks must fail")
	}
}

func TestCMakeExecutableTargetsAndPrefixPath(t *testing.T) {
	pb := Installed{Root: "/c/protobuf", Info: manifest.PackageInfo{Name: "protocolbuffers/protobuf", Version: "36.2.0", Targets: map[string]manifest.TargetInfo{
		"protoc": {Type: "executable", Exe: "bin/protoc"},
	}}}
	out, err := CMake([]Installed{pb})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`add_executable(protocolbuffers::protoc IMPORTED)`,
		`IMPORTED_LOCATION "/c/protobuf/bin/protoc"`,
		"list(APPEND CMAKE_PREFIX_PATH\n  \"/c/protobuf\")", // find_package(Qt6) and friends work too
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	pb.Info.Targets["protoc"] = manifest.TargetInfo{Type: "executable"}
	if _, err := CMake([]Installed{pb}); err == nil {
		t.Error("an executable target without a program file must be refused")
	}
}
