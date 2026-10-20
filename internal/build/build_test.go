package build

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/pixelpolishers/kiln-core/manifest"
	"github.com/pixelpolishers/kiln-core/when"
)

func skipOnWindows(t *testing.T, why string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix-only test: " + why)
	}
}

var baseSettings = map[string]any{
	"os": "linux", "arch": "x86_64", "build_type": "release", "linkage": "static", "cpp_standard": "17",
	"compiler": map[string]any{"name": "gcc", "version": "13"}, "stdlib": "libstdc++",
}

func scalar(v any) manifest.OptionValue {
	return manifest.OptionValue{Cases: []manifest.OptionCase{{Value: v}}}
}

func TestCMakeConfigureArgs(t *testing.T) {
	p := Params{
		Build: manifest.Build{System: "cmake", Options: map[string]manifest.OptionValue{
			"FMT_TEST": scalar(false),
			"FMT_DOC":  scalar(false),
			"LEVEL":    scalar(float64(3)),
			"SDL_STATIC": {Cases: []manifest.OptionCase{
				{Value: true, When: map[string]any{"linkage": "static"}},
				{Value: false},
			}},
			"ONLY_SHARED": {Cases: []manifest.OptionCase{{Value: true, When: map[string]any{"linkage": "shared"}}}},
		}},
		Settings: baseSettings, SourceDir: "/src", Prefix: "/prefix", WorkDir: "/work",
		DepPrefixes: []string{"/cache/a", "/cache/b"}, Toolchain: Toolchain{CC: "gcc", CXX: "g++"},
	}
	args, err := CMakeConfigureArgs(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"-S", "/src", "-B", "/work",
		"-DCMAKE_INSTALL_PREFIX=/prefix", "-DCMAKE_INSTALL_LIBDIR=lib", "-DCMAKE_BUILD_TYPE=Release",
		"-DBUILD_SHARED_LIBS=OFF", "-DCMAKE_CXX_COMPILER=g++", "-DCMAKE_C_COMPILER=gcc",
		"-DCMAKE_CXX_STANDARD=17", "-DCMAKE_PREFIX_PATH=/cache/a;/cache/b",
		"-DFMT_TEST=OFF", "-DFMT_DOC=OFF", "-DLEVEL=3", "-DSDL_STATIC=ON",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-DONLY_SHARED") {
			t.Errorf("an option with no matching case must be left out: %s", a)
		}
	}

	shared := map[string]any{}
	for k, v := range baseSettings {
		shared[k] = v
	}
	shared["linkage"], shared["build_type"] = "shared", "debug"
	p.Settings = shared
	args, _ = CMakeConfigureArgs(p)
	for _, want := range []string{"-DBUILD_SHARED_LIBS=ON", "-DCMAKE_BUILD_TYPE=Debug", "-DSDL_STATIC=OFF", "-DONLY_SHARED=ON"} {
		if !slices.Contains(args, want) {
			t.Errorf("shared/debug: missing %q in %v", want, args)
		}
	}

	p.Settings = map[string]any{"build_type": "fast"}
	if _, err := CMakeConfigureArgs(p); err == nil {
		t.Error("an unknown build type must fail")
	}
}

func TestSourceDirOption(t *testing.T) {
	p := Params{Build: manifest.Build{System: "cmake", SourceDir: "cmake"}, Settings: baseSettings, SourceDir: "/src", Prefix: "/p", WorkDir: "/w"}
	args, _ := CMakeConfigureArgs(p)
	if args[1] != filepath.Join("/src", "cmake") {
		t.Errorf("source-dir ignored: %v", args[:2])
	}
}

func TestExpand(t *testing.T) {
	p := Params{
		Settings: baseSettings, Options: map[string]any{"fast": true},
		Prefix: "/prefix", WorkDir: "/work", SourceDir: "/src", Jobs: 8,
		DepPrefixes: []string{"/a", "/b"}, Toolchain: Toolchain{CC: "gcc", CXX: "g++"},
	}
	cases := map[string]string{
		"--prefix={prefix}":        "--prefix=/prefix",
		"-j{jobs}":                 "-j8",
		"CC={cc} CXX={cxx}":        "CC=gcc CXX=g++",
		"{source_dir}:{build_dir}": "/src:/work",
		"{deps}":                   "/a" + string(os.PathListSeparator) + "/b",
		"{settings.build_type}":    "release",
		"{settings.compiler.name}-{settings.compiler.version}": "gcc-13",
		"{options.fast}": "true",
		"plain":          "plain",
	}
	for in, want := range cases {
		if got, err := Expand(in, p); err != nil || got != want {
			t.Errorf("Expand(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"{bogus}", "{settings.nope}", "{options.nope}"} {
		if _, err := Expand(bad, p); err == nil {
			t.Errorf("Expand(%q) must fail", bad)
		}
	}
}

func TestResolveOption(t *testing.T) {
	ctx := when.Context(baseSettings, nil)
	o := manifest.OptionValue{Cases: []manifest.OptionCase{
		{Value: "first", When: map[string]any{"os": "windows"}},
		{Value: "second", When: map[string]any{"os": []any{"linux", "macos"}}},
		{Value: "third"},
	}}
	if v, ok := ResolveOption(o, ctx); !ok || v != "second" {
		t.Errorf("first matching case must win: %v %v", v, ok)
	}
}

func TestCFor(t *testing.T) {
	for in, want := range map[string]string{"g++": "gcc", "/usr/bin/g++": "/usr/bin/gcc", "x86_64-linux-gnu-g++-13": "", "clang++": "clang", "c++": "cc", "/usr/bin/c++": "/usr/bin/cc", "weird": ""} {
		if got := CFor(in); got != want {
			t.Errorf("CFor(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestCustomBuild(t *testing.T) {
	skipOnWindows(t, "uses sh, mkdir and touch")
	src, prefix := t.TempDir(), filepath.Join(t.TempDir(), "prefix")
	cmd := func(when map[string]any, argv ...any) manifest.Command {
		c := manifest.Command{When: when}
		for _, a := range argv {
			switch v := a.(type) {
			case string:
				c.Run = append(c.Run, manifest.Conditional{Value: v})
			case manifest.Conditional:
				c.Run = append(c.Run, v)
			}
		}
		return c
	}
	p := Params{
		Package: "t/t", SourceDir: src, Prefix: prefix, WorkDir: t.TempDir(), Settings: baseSettings, Options: map[string]any{},
		LogPath: filepath.Join(t.TempDir(), "log", "build.log"),
		Build: manifest.Build{System: "custom", Commands: []manifest.Command{
			cmd(nil, "mkdir", "-p", "{prefix}/lib"),
			cmd(nil, "sh", "-c", "echo {settings.linkage} > {prefix}/lib/mode.txt"),
			cmd(map[string]any{"os": "windows"}, "sh", "-c", "exit 1"), // other platform: skipped
			cmd(nil, "touch", "{prefix}/lib/always", manifest.Conditional{Value: "{prefix}/lib/only-shared", When: map[string]any{"linkage": "shared"}}),
		}},
	}
	if err := Run(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(prefix, "lib", "mode.txt")); strings.TrimSpace(string(b)) != "static" {
		t.Errorf("placeholder not expanded: %q", b)
	}
	if _, err := os.Stat(filepath.Join(prefix, "lib", "always")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(prefix, "lib", "only-shared")); err == nil {
		t.Error("a conditional argument must be dropped when its condition fails")
	}
}

func TestCustomBuildFailureReportsTail(t *testing.T) {
	skipOnWindows(t, "uses sh")
	p := Params{
		Package: "t/t", SourceDir: t.TempDir(), Prefix: t.TempDir(), WorkDir: t.TempDir(), Settings: baseSettings,
		LogPath: filepath.Join(t.TempDir(), "build.log"),
		Build: manifest.Build{System: "custom", Commands: []manifest.Command{
			{Run: []manifest.Conditional{{Value: "sh"}, {Value: "-c"}, {Value: "echo compiler exploded >&2; exit 3"}}},
		}},
	}
	err := Run(context.Background(), p)
	var be *Error
	if !errors.As(err, &be) || !strings.Contains(be.Tail, "compiler exploded") || be.LogPath != p.LogPath {
		t.Fatalf("%v", err)
	}
}

func TestCustomBuildNeedsAnApplicableCommand(t *testing.T) {
	p := Params{
		Package: "t/t", SourceDir: t.TempDir(), Prefix: t.TempDir(), WorkDir: t.TempDir(), Settings: baseSettings, LogPath: filepath.Join(t.TempDir(), "b.log"),
		Build: manifest.Build{System: "custom", Commands: []manifest.Command{{When: map[string]any{"os": "windows"}, Run: []manifest.Conditional{{Value: "true"}}}}},
	}
	if err := Run(context.Background(), p); err == nil || !strings.Contains(err.Error(), "no build command applies") {
		t.Errorf("%v", err)
	}
	p.Build.System = "meson"
	if err := Run(context.Background(), p); err == nil || !strings.Contains(err.Error(), "not supported yet") {
		t.Errorf("%v", err)
	}
	p.Build = manifest.Build{}
	if err := Run(context.Background(), p); err == nil || !strings.Contains(err.Error(), "no build section") {
		t.Errorf("%v", err)
	}
}

// A real CMake build of a tiny library, to prove the configure/build/install steps work.
func TestCMakeBuildOfFixtureLibrary(t *testing.T) {
	for _, tool := range []string{"cmake", "g++"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	src := t.TempDir()
	write := func(name, content string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(src, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("CMakeLists.txt", `cmake_minimum_required(VERSION 3.15)
project(tiny CXX)
set(TINY_VALUE 1 CACHE STRING "value baked into the library")
add_library(tiny src/tiny.cc)
target_include_directories(tiny PUBLIC $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include> $<INSTALL_INTERFACE:include>)
target_compile_definitions(tiny PRIVATE TINY_VALUE=${TINY_VALUE})
install(TARGETS tiny ARCHIVE DESTINATION lib LIBRARY DESTINATION lib)
install(FILES include/tiny.h DESTINATION include)
`)
	write("include/tiny.h", "#pragma once\nint tiny_value();\n")
	write("src/tiny.cc", "#include <tiny.h>\nint tiny_value() { return TINY_VALUE; }\n")

	prefix := filepath.Join(t.TempDir(), "prefix")
	p := Params{
		Package: "t/tiny", SourceDir: src, Prefix: prefix, WorkDir: filepath.Join(t.TempDir(), "build"),
		Settings: baseSettings, Toolchain: Toolchain{CC: CFor("g++"), CXX: "g++"}, Jobs: 2,
		LogPath: filepath.Join(t.TempDir(), "build.log"),
		Build:   manifest.Build{System: "cmake", Options: map[string]manifest.OptionValue{"TINY_VALUE": scalar(float64(7))}},
	}
	if err := Run(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"include/tiny.h", "lib/libtiny.a"} {
		if _, err := os.Stat(filepath.Join(prefix, f)); err != nil {
			t.Errorf("not installed: %v", err)
		}
	}
	// link and run a program against the installed library, to prove it is the real thing
	main := filepath.Join(t.TempDir(), "main.cc")
	os.WriteFile(main, []byte("#include <tiny.h>\n#include <cstdio>\nint main(){ std::printf(\"%d\\n\", tiny_value()); }\n"), 0o644)
	exe := filepath.Join(t.TempDir(), "main")
	if out, err := exec.Command("g++", "-I"+filepath.Join(prefix, "include"), main, filepath.Join(prefix, "lib", "libtiny.a"), "-o", exe).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := exec.Command(exe).Output(); err != nil || strings.TrimSpace(string(out)) != "7" {
		t.Errorf("the option value must reach the compiled library: %q %v", out, err)
	}
}

func TestGeneratorIsOnlyChosenOnWindows(t *testing.T) {
	if got := Generator(baseSettings, "Unix Makefiles"); got != "" {
		t.Errorf("a generator from CMAKE_GENERATOR must be respected, got %q", got)
	}
	got := Generator(baseSettings, "")
	if runtime.GOOS != "windows" {
		if got != "" {
			t.Errorf("on %s CMake's own choice is fine, got %q", runtime.GOOS, got)
		}
		return
	}
	if got != "Ninja" && got != "MinGW Makefiles" && got != "" {
		t.Errorf("unexpected generator %q", got)
	}
}
