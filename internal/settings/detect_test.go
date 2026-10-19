package settings

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseCompiler(t *testing.T) {
	cases := []struct{ name, out, wantName, wantVer string }{
		{"fedora g++", "g++ (GCC) 15.2.1 20250808 (Red Hat 15.2.1-1)\nCopyright (C) 2025 Free Software Foundation, Inc.", "gcc", "15"},
		{"ubuntu c++", "c++ (Ubuntu 13.2.0-23ubuntu4) 13.2.0\nCopyright (C) 2023 Free Software Foundation, Inc.", "gcc", "13"},
		{"debian g++", "g++ (Debian 12.2.0-14) 12.2.0", "gcc", "12"},
		{"clang", "Ubuntu clang version 17.0.6 (++20231209124227+6009708b4367-1~exp1~20231209124336.77)\nTarget: x86_64-pc-linux-gnu", "clang", "17"},
		{"apple clang", "Apple clang version 15.0.0 (clang-1500.3.9.4)\nTarget: arm64-apple-darwin23.4.0", "apple-clang", "15"},
		{"msvc", "Microsoft (R) C/C++ Optimizing Compiler Version 19.40.33811 for x64\nCopyright (C) Microsoft Corporation.", "msvc", "19.40"},
	}
	for _, c := range cases {
		n, v, ok := ParseCompiler(c.out)
		if !ok || n != c.wantName || v != c.wantVer {
			t.Errorf("%s: got %q %q %v; want %q %q", c.name, n, v, ok, c.wantName, c.wantVer)
		}
	}
	if _, _, ok := ParseCompiler("tcc version 0.9.27"); ok {
		t.Error("unknown compiler must not be recognized")
	}
}

func TestParseMacros(t *testing.T) {
	m := ParseMacros("#define __GLIBCXX__ 20250808\n#define _GLIBCXX_USE_CXX11_ABI 1\n#define __cplusplus 201703L\n#define EMPTY\nnot a define\n")
	if m["__GLIBCXX__"] != "20250808" || m["_GLIBCXX_USE_CXX11_ABI"] != "1" {
		t.Errorf("%v", m)
	}
	if v, ok := m["EMPTY"]; !ok || v != "" {
		t.Errorf("EMPTY: %q %v", v, ok)
	}
}

// fakeEnv simulates a machine with one compiler.
func fakeEnv(goos, goarch, prog, version, macros string, glob map[string][]string) Env {
	return Env{
		GOOS: goos, GOARCH: goarch,
		Getenv: func(string) string { return "" },
		LookPath: func(n string) (string, error) {
			if n == prog {
				return "/usr/bin/" + n, nil
			}
			return "", fmt.Errorf("not found")
		},
		Run: func(name string, args []string, stdin string) (string, string, error) {
			if len(args) > 0 && args[0] == "--version" {
				return version, "", nil
			}
			if name == "cl" {
				return "", version, nil
			}
			if strings.Contains(strings.Join(args, " "), "-dM") {
				if macros == "" {
					return "", "", fmt.Errorf("preprocess failed")
				}
				return macros, "", nil
			}
			return "", "", fmt.Errorf("unexpected call")
		},
		Glob: func(p string) ([]string, error) { return glob[p], nil },
	}
}

func TestDetectLinuxGCC(t *testing.T) {
	r := Detect(fakeEnv("linux", "amd64", "g++", "g++ (GCC) 13.2.1 20230728",
		"#define __GLIBCXX__ 20230728\n#define _GLIBCXX_USE_CXX11_ABI 1\n#define __GLIBC__ 2\n", nil))
	s := r.Settings
	if s.OS != "linux" || s.Arch != "x86_64" || s.Compiler != (Compiler{"gcc", "13"}) || s.Stdlib != "libstdc++" || s.Libc != "glibc" {
		t.Errorf("%+v", s)
	}
	if s.CXX11ABI == nil || !*s.CXX11ABI {
		t.Error("cxx11_abi should be true")
	}
	if s.CppStandard != "17" || s.BuildType != "release" || s.Linkage != "static" {
		t.Errorf("defaults: %+v", s)
	}
	if r.Sources["stdlib"] != Detected || r.Sources["cpp_standard"] != Default {
		t.Errorf("sources: %v", r.Sources)
	}
}

func TestDetectOldABI(t *testing.T) {
	r := Detect(fakeEnv("linux", "amd64", "g++", "g++ (GCC) 7.5.0",
		"#define __GLIBCXX__ 20200101\n#define _GLIBCXX_USE_CXX11_ABI 0\n#define __GLIBC__ 2\n", nil))
	if r.Settings.CXX11ABI == nil || *r.Settings.CXX11ABI {
		t.Error("cxx11_abi should be false")
	}
}

func TestDetectMuslClangLibcxx(t *testing.T) {
	r := Detect(fakeEnv("linux", "arm64", "clang++", "clang version 18.1.0",
		"#define _LIBCPP_VERSION 180100\n", map[string][]string{"/lib/ld-musl-*": {"/lib/ld-musl-aarch64.so.1"}}))
	s := r.Settings
	if s.Arch != "arm64" || s.Compiler.Name != "clang" || s.Stdlib != "libc++" || s.Libc != "musl" || s.CXX11ABI != nil {
		t.Errorf("%+v", s)
	}
}

func TestDetectMacOS(t *testing.T) {
	r := Detect(fakeEnv("darwin", "arm64", "c++", "Apple clang version 15.0.0 (clang-1500.3.9.4)",
		"#define _LIBCPP_VERSION 170006\n", nil))
	s := r.Settings
	if s.OS != "macos" || s.Compiler.Name != "apple-clang" || s.Stdlib != "libc++" || s.Libc != "system" {
		t.Errorf("%+v", s)
	}
}

func TestDetectMSVC(t *testing.T) {
	r := Detect(fakeEnv("windows", "amd64", "cl", "Microsoft (R) C/C++ Optimizing Compiler Version 19.40.33811 for x64", "", nil))
	s := r.Settings
	if s.OS != "windows" || s.Compiler != (Compiler{"msvc", "19.40"}) || s.Stdlib != "msvc-stl" || s.MSVCRuntime != "dynamic" || s.Libc != "ucrt" {
		t.Errorf("%+v", s)
	}
}

func TestDetectProbeFailureFallsBack(t *testing.T) {
	r := Detect(fakeEnv("linux", "amd64", "g++", "g++ (GCC) 13.2.1", "", nil))
	if r.Settings.Stdlib != "libstdc++" || r.Sources["stdlib"] != Default || len(r.Notes) == 0 {
		t.Errorf("%+v %v %v", r.Settings, r.Sources, r.Notes)
	}
}

func TestDetectNoCompiler(t *testing.T) {
	r := Detect(fakeEnv("linux", "amd64", "none", "", "", nil))
	if r.Settings.Compiler.Name != "" || len(r.Notes) == 0 {
		t.Errorf("%+v %v", r.Settings, r.Notes)
	}
}

func TestCXXEnvTakesPrecedence(t *testing.T) {
	env := fakeEnv("linux", "amd64", "clang++-18", "clang version 18.1.0", "#define _LIBCPP_VERSION 1\n", nil)
	env.Getenv = func(k string) string {
		if k == "CXX" {
			return "clang++-18 -m64"
		}
		return ""
	}
	if r := Detect(env); r.Compiler != "clang++-18" || r.Settings.Compiler.Name != "clang" {
		t.Errorf("%+v", r)
	}
}

func TestApply(t *testing.T) {
	var s Settings
	src := Sources{}
	if err := s.Apply([]string{"build_type=debug", "compiler.version=14", "lto=true"}, src); err != nil {
		t.Fatal(err)
	}
	if s.BuildType != "debug" || s.Compiler.Version != "14" || s.LTO == nil || !*s.LTO || src["build_type"] != Override {
		t.Errorf("%+v %v", s, src)
	}
	for _, bad := range []string{"nokey", "=x", "k=", "bogus=1", "rtti=maybe"} {
		if err := s.Apply([]string{bad}, src); err == nil {
			t.Errorf("Apply(%q) should fail", bad)
		}
	}
}
