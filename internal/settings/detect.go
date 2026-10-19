package settings

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// DefaultCppStandard is the C++ standard kiln uses unless told otherwise. It
// is deliberately independent of the compiler's own default: kiln owns the
// build flags of what it builds, so the standard must not vary per machine.
const DefaultCppStandard = "17"

// Env abstracts everything detection needs from the machine, so it can be
// tested with canned compiler output.
type Env struct {
	GOOS, GOARCH string
	Getenv       func(string) string
	LookPath     func(string) (string, error)
	// Run executes a program and returns its stdout and stderr.
	Run  func(name string, args []string, stdin string) (stdout, stderr string, err error)
	Glob func(pattern string) ([]string, error)
}

func SystemEnv() Env {
	return Env{
		GOOS:     runtime.GOOS,
		GOARCH:   runtime.GOARCH,
		Getenv:   os.Getenv,
		LookPath: exec.LookPath,
		Glob:     filepath.Glob,
		Run: func(name string, args []string, stdin string) (string, string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Stdin = strings.NewReader(stdin)
			var out, errb bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errb
			err := cmd.Run()
			return out.String(), errb.String(), err
		},
	}
}

// Result is the outcome of detection.
type Result struct {
	Settings Settings
	Sources  Sources
	Compiler string   // program used, e.g. "g++"
	Notes    []string // anything the user should know (fallbacks, failures)
}

// Detect inspects the machine. It never fails outright: what it cannot
// determine is left empty and explained in Notes, so overrides can fill it in.
func Detect(env Env) Result {
	r := Result{Sources: Sources{}}
	s := &r.Settings
	set := func(key, kind string) { r.Sources[key] = kind }

	switch env.GOOS {
	case "darwin":
		s.OS = "macos"
	case "linux", "windows", "freebsd", "android", "ios":
		s.OS = env.GOOS
	default:
		r.Notes = append(r.Notes, fmt.Sprintf("unsupported operating system %q", env.GOOS))
	}
	switch env.GOARCH {
	case "amd64":
		s.Arch = "x86_64"
	case "386":
		s.Arch = "x86"
	case "arm64", "arm", "riscv64":
		s.Arch = env.GOARCH
	case "wasm":
		s.Arch = "wasm32"
	default:
		r.Notes = append(r.Notes, fmt.Sprintf("unsupported architecture %q", env.GOARCH))
	}
	if s.OS != "" {
		set("os", Detected)
	}
	if s.Arch != "" {
		set("arch", Detected)
	}

	prog, name, version := findCompiler(env, &r)
	if prog != "" {
		r.Compiler = prog
		s.Compiler = Compiler{Name: name, Version: version}
		set("compiler.name", Detected)
		set("compiler.version", Detected)
	}

	var macros map[string]string
	if prog != "" && name != "msvc" {
		out, _, err := env.Run(prog, []string{"-x", "c++", "-E", "-dM", "-"}, "#include <cstddef>\n")
		if err != nil {
			r.Notes = append(r.Notes, fmt.Sprintf("%s could not preprocess <cstddef>; standard library not probed", prog))
		} else {
			macros = ParseMacros(out)
		}
	}

	switch {
	case name == "msvc":
		s.Stdlib = "msvc-stl"
		set("stdlib", Detected)
	case macros["_LIBCPP_VERSION"] != "":
		s.Stdlib = "libc++"
		set("stdlib", Detected)
	case macros["__GLIBCXX__"] != "":
		s.Stdlib = "libstdc++"
		set("stdlib", Detected)
	case name != "":
		// Probe failed: fall back to the platform convention.
		s.Stdlib = "libstdc++"
		if name == "apple-clang" || (name == "clang" && s.OS == "macos") {
			s.Stdlib = "libc++"
		}
		set("stdlib", Default)
		r.Notes = append(r.Notes, "standard library assumed from the compiler and platform")
	}

	if s.Stdlib == "libstdc++" {
		abi := true
		if v, ok := macros["_GLIBCXX_USE_CXX11_ABI"]; ok {
			abi = v != "0"
			set("cxx11_abi", Detected)
		} else {
			set("cxx11_abi", Default)
		}
		s.CXX11ABI = &abi
	}

	switch s.OS {
	case "linux":
		switch {
		case macros["__GLIBC__"] != "":
			s.Libc = "glibc"
			set("libc", Detected)
		case globs(env, "/lib/ld-musl-*", "/usr/lib/ld-musl-*"):
			s.Libc = "musl"
			set("libc", Detected)
		default:
			s.Libc = "glibc"
			set("libc", Default)
		}
	case "windows":
		s.Libc = "ucrt"
		set("libc", Default)
	case "macos", "freebsd":
		s.Libc = "system"
		set("libc", Default)
	}

	s.CppStandard, s.BuildType, s.Linkage = DefaultCppStandard, "release", "static"
	set("cpp_standard", Default)
	set("build_type", Default)
	set("linkage", Default)
	if name == "msvc" {
		s.MSVCRuntime = "dynamic"
		set("msvc_runtime", Default)
	}
	return r
}

func globs(env Env, patterns ...string) bool {
	if env.Glob == nil {
		return false
	}
	for _, p := range patterns {
		if m, err := env.Glob(p); err == nil && len(m) > 0 {
			return true
		}
	}
	return false
}

// findCompiler returns the program, normalized compiler name and version.
func findCompiler(env Env, r *Result) (prog, name, version string) {
	var candidates []string
	if cxx := strings.Fields(env.Getenv("CXX")); len(cxx) > 0 {
		candidates = append(candidates, cxx[0])
	}
	if env.GOOS == "windows" {
		candidates = append(candidates, "cl", "clang++", "g++")
	} else {
		candidates = append(candidates, "c++", "g++", "clang++")
	}
	tried := map[string]bool{}
	for _, c := range candidates {
		if tried[c] {
			continue
		}
		tried[c] = true
		if _, err := env.LookPath(c); err != nil {
			continue
		}
		args := []string{"--version"}
		if c == "cl" || strings.HasSuffix(c, "cl.exe") {
			args = nil // cl prints its banner to stderr when run without input
		}
		stdout, stderr, _ := env.Run(c, args, "")
		if n, v, ok := ParseCompiler(stdout + "\n" + stderr); ok {
			return c, n, v
		}
		r.Notes = append(r.Notes, fmt.Sprintf("%s found but its version output was not recognized", c))
	}
	r.Notes = append(r.Notes, "no C++ compiler found: set CXX, or pass -s compiler.name=... -s compiler.version=...")
	return "", "", ""
}

var (
	reApple = regexp.MustCompile(`(?i)apple (?:clang|llvm) version (\d+)`)
	reClang = regexp.MustCompile(`(?i)clang version (\d+)`)
	reMSVC  = regexp.MustCompile(`(?i)optimizing compiler version (\d+\.\d+)`)
	reDots  = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)
)

// ParseCompiler recognizes the output of `<cxx> --version` (or cl's banner).
// Versions are reported at the granularity that matters for ABI: the major
// version for gcc/clang/apple-clang, major.minor for MSVC.
func ParseCompiler(out string) (name, version string, ok bool) {
	if m := reApple.FindStringSubmatch(out); m != nil {
		return "apple-clang", m[1], true
	}
	if m := reClang.FindStringSubmatch(out); m != nil {
		return "clang", m[1], true
	}
	if m := reMSVC.FindStringSubmatch(out); m != nil {
		return "msvc", m[1], true
	}
	first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	l := strings.ToLower(first)
	if strings.Contains(l, "gcc") || strings.Contains(l, "g++") || strings.Contains(l, "c++ (") {
		if all := reDots.FindAllString(first, -1); len(all) > 0 {
			major, _, _ := strings.Cut(all[len(all)-1], ".")
			return "gcc", major, true
		}
	}
	return "", "", false
}

// ParseMacros parses `-E -dM` output ("#define NAME VALUE") into a map.
func ParseMacros(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "#define ")
		if !ok {
			continue
		}
		name, value, _ := strings.Cut(rest, " ")
		m[name] = strings.TrimSpace(value)
	}
	return m
}
