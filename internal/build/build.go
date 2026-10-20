// Package build compiles and installs a package from source, following the
// "build" section of its manifest.
//
// Supported build systems: "cmake" (settings are passed on automatically) and
// "custom" (explicit argv steps with placeholders and conditions). Building
// runs the package's own build scripts, so it must only be done for packages
// the user has chosen to build.
package build

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/pixelpolishers/kiln-core/manifest"
	"github.com/pixelpolishers/kiln-core/when"
)

// Toolchain names the compiler programs used for the build.
type Toolchain struct {
	CC, CXX string
}

// CFor derives the C compiler that goes with a C++ compiler program.
func CFor(cxx string) string {
	switch {
	case strings.HasSuffix(cxx, "clang++"): // before g++: "clang++" ends in "g++"
		return strings.TrimSuffix(cxx, "clang++") + "clang"
	case strings.HasSuffix(cxx, "g++"):
		return strings.TrimSuffix(cxx, "g++") + "gcc"
	case cxx == "c++" || strings.HasSuffix(cxx, "/c++"):
		return strings.TrimSuffix(cxx, "c++") + "cc"
	}
	return ""
}

// Params describes one build.
type Params struct {
	Package   string
	Build     manifest.Build
	Settings  map[string]any // full build settings
	Options   map[string]any // resolved package options
	SourceDir string         // root of the fetched source
	Prefix    string         // where to install
	WorkDir   string         // scratch directory for the build tree
	// DepPrefixes are the install roots of the dependencies already built.
	DepPrefixes []string
	Toolchain   Toolchain
	Jobs        int
	// LogPath receives the output of every step.
	LogPath string
	// Echo, if set, also receives the output (verbose mode).
	Echo io.Writer
}

// Error is a failed build step with the end of its output.
type Error struct {
	Package string
	Step    string
	Err     error
	Tail    string
	LogPath string
}

func (e *Error) Error() string {
	return fmt.Sprintf("building %s failed at %q: %v\n--- last lines of %s ---\n%s", e.Package, e.Step, e.Err, e.LogPath, e.Tail)
}
func (e *Error) Unwrap() error { return e.Err }

// Run builds and installs the package into p.Prefix.
func Run(ctx context.Context, p Params) error {
	if p.Jobs <= 0 {
		p.Jobs = runtime.NumCPU()
	}
	if err := os.MkdirAll(filepath.Dir(p.LogPath), 0o755); err != nil {
		return err
	}
	logf, err := os.Create(p.LogPath)
	if err != nil {
		return err
	}
	defer logf.Close()
	var out io.Writer = logf
	if p.Echo != nil {
		out = io.MultiWriter(logf, p.Echo)
	}
	r := &runner{p: p, out: out}

	switch p.Build.System {
	case "cmake":
		return r.cmake(ctx)
	case "custom":
		return r.custom(ctx)
	case "":
		return fmt.Errorf("%s has no build section, so it cannot be built from source", p.Package)
	}
	return fmt.Errorf("%s: build system %q is not supported yet (supported: cmake, custom)", p.Package, p.Build.System)
}

type runner struct {
	p   Params
	out io.Writer
}

func (r *runner) exec(ctx context.Context, step, dir string, env []string, name string, args ...string) error {
	fmt.Fprintf(r.out, "\n$ %s %s\n", name, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = r.out, r.out
	if err := cmd.Run(); err != nil {
		return &Error{Package: r.p.Package, Step: step, Err: err, Tail: tail(r.p.LogPath, 40), LogPath: r.p.LogPath}
	}
	return nil
}

func tail(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	return strings.Join(lines, "\n")
}

func (r *runner) env() []string {
	env := []string{"CXX=" + r.p.Toolchain.CXX}
	if r.p.Toolchain.CC != "" {
		env = append(env, "CC="+r.p.Toolchain.CC)
	}
	return env
}

// ---- cmake ----

var cmakeBuildTypes = map[string]string{
	"debug": "Debug", "release": "Release", "relwithdebinfo": "RelWithDebInfo", "minsizerel": "MinSizeRel",
}

func onOff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

// CMakeConfigureArgs returns the arguments of the configure step. Exported for tests.
func CMakeConfigureArgs(p Params) ([]string, error) {
	str := func(key string) string { s, _ := p.Settings[key].(string); return s }
	buildType, ok := cmakeBuildTypes[str("build_type")]
	if !ok {
		return nil, fmt.Errorf("unknown build type %q", str("build_type"))
	}
	src := p.SourceDir
	if p.Build.SourceDir != "" && p.Build.SourceDir != "." {
		src = filepath.Join(p.SourceDir, filepath.FromSlash(p.Build.SourceDir))
	}
	args := []string{
		"-S", src, "-B", p.WorkDir,
		"-DCMAKE_INSTALL_PREFIX=" + p.Prefix,
		"-DCMAKE_INSTALL_LIBDIR=lib", // not lib64, so the layout is the same on every distribution
		"-DCMAKE_BUILD_TYPE=" + buildType,
		"-DBUILD_SHARED_LIBS=" + onOff(str("linkage") == "shared"),
		"-DCMAKE_POSITION_INDEPENDENT_CODE=ON",
		"-DCMAKE_CXX_COMPILER=" + p.Toolchain.CXX,
	}
	if p.Toolchain.CC != "" {
		args = append(args, "-DCMAKE_C_COMPILER="+p.Toolchain.CC)
	}
	if std := str("cpp_standard"); std != "" {
		args = append(args, "-DCMAKE_CXX_STANDARD="+std, "-DCMAKE_CXX_STANDARD_REQUIRED=ON")
	}
	if len(p.DepPrefixes) > 0 {
		args = append(args, "-DCMAKE_PREFIX_PATH="+strings.Join(p.DepPrefixes, ";"))
	}

	ctx := when.Context(p.Settings, p.Options)
	names := make([]string, 0, len(p.Build.Options))
	for n := range p.Build.Options {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		v, ok := ResolveOption(p.Build.Options[n], ctx)
		if !ok {
			continue
		}
		if b, isBool := v.(bool); isBool {
			args = append(args, fmt.Sprintf("-D%s=%s", n, onOff(b)))
		} else {
			args = append(args, fmt.Sprintf("-D%s=%v", n, v))
		}
	}
	return args, nil
}

// ResolveOption picks the value of a build option: the first case whose
// condition holds. ok is false if none does (the option is then left out).
func ResolveOption(o manifest.OptionValue, ctx map[string]string) (any, bool) {
	for _, c := range o.Cases {
		if when.Match(c.When, ctx) {
			return c.Value, true
		}
	}
	return nil, false
}

// Generator picks the CMake generator to ask for, or "" to leave the choice to
// CMake. On Windows CMake prefers the Visual Studio generators, which ignore
// the compiler that was detected (a MinGW gcc would be silently replaced by
// MSVC and produce .lib files). Ninja works with every compiler (for MSVC from
// a Developer Command Prompt); without Ninja, gcc and clang use MinGW Makefiles.
// A generator chosen by the user through CMAKE_GENERATOR is respected.
func Generator(settings map[string]any, fromEnv string) string {
	if fromEnv != "" || runtime.GOOS != "windows" { // builds always run on the host
		return ""
	}
	if _, err := exec.LookPath("ninja"); err == nil {
		return "Ninja"
	}
	if c, _ := settings["compiler"].(map[string]any); c != nil {
		if name, _ := c["name"].(string); name == "gcc" {
			return "MinGW Makefiles"
		}
	}
	return ""
}

func (r *runner) cmake(ctx context.Context) error {
	if _, err := exec.LookPath("cmake"); err != nil {
		return fmt.Errorf("%s needs cmake to build, but it was not found on PATH", r.p.Package)
	}
	args, err := CMakeConfigureArgs(r.p)
	if err != nil {
		return err
	}
	if gen := Generator(r.p.Settings, os.Getenv("CMAKE_GENERATOR")); gen != "" {
		args = append([]string{"-G", gen}, args...)
	}
	if err := r.exec(ctx, "cmake configure", r.p.SourceDir, r.env(), "cmake", args...); err != nil {
		return err
	}
	config := cmakeBuildTypes[r.p.Settings["build_type"].(string)]
	if err := r.exec(ctx, "cmake build", r.p.SourceDir, r.env(), "cmake", "--build", r.p.WorkDir, "--config", config, "--parallel", fmt.Sprint(r.p.Jobs)); err != nil {
		return err
	}
	return r.exec(ctx, "cmake install", r.p.SourceDir, r.env(), "cmake", "--install", r.p.WorkDir, "--config", config)
}

// ---- custom ----

var placeholderRe = regexp.MustCompile(`\{([^{}]+)\}`)

// Expand replaces the placeholders of s: {prefix} {jobs} {cc} {cxx}
// {source_dir} {build_dir} {deps} {settings.<field>} {options.<name>}.
func Expand(s string, p Params) (string, error) {
	ctx := when.Context(p.Settings, p.Options)
	var firstErr error
	out := placeholderRe.ReplaceAllStringFunc(s, func(m string) string {
		name := m[1 : len(m)-1]
		var v string
		switch {
		case name == "prefix":
			v = p.Prefix
		case name == "jobs":
			v = fmt.Sprint(p.Jobs)
		case name == "cc":
			v = p.Toolchain.CC
		case name == "cxx":
			v = p.Toolchain.CXX
		case name == "source_dir":
			v = p.SourceDir
		case name == "build_dir":
			v = p.WorkDir
		case name == "deps":
			v = strings.Join(p.DepPrefixes, string(os.PathListSeparator))
		case strings.HasPrefix(name, "settings."):
			got, ok := ctx[strings.TrimPrefix(name, "settings.")]
			if !ok && firstErr == nil {
				firstErr = fmt.Errorf("placeholder {%s}: no such setting", name)
			}
			v = got
		case strings.HasPrefix(name, "options."):
			got, ok := ctx[name]
			if !ok && firstErr == nil {
				firstErr = fmt.Errorf("placeholder {%s}: package has no such option", name)
			}
			v = got
		default:
			if firstErr == nil {
				firstErr = fmt.Errorf("unknown placeholder {%s}", name)
			}
		}
		return v
	})
	return out, firstErr
}

func (r *runner) custom(ctx context.Context) error {
	if len(r.p.Build.Commands) == 0 {
		return fmt.Errorf("%s: build system \"custom\" has no commands", r.p.Package)
	}
	wctx := when.Context(r.p.Settings, r.p.Options)
	ran := 0
	for i, cmd := range r.p.Build.Commands {
		if !when.Match(cmd.When, wctx) {
			continue
		}
		var argv []string
		for _, item := range cmd.Run {
			if !when.Match(item.When, wctx) {
				continue
			}
			s, err := Expand(item.Value, r.p)
			if err != nil {
				return fmt.Errorf("%s: build command %d: %w", r.p.Package, i+1, err)
			}
			argv = append(argv, s)
		}
		if len(argv) == 0 {
			continue
		}
		ran++
		if err := r.exec(ctx, strings.Join(argv, " "), r.p.SourceDir, r.env(), argv[0], argv[1:]...); err != nil {
			return err
		}
	}
	if ran == 0 {
		return fmt.Errorf("%s: no build command applies to these settings (os=%s arch=%s)", r.p.Package, wctx["os"], wctx["arch"])
	}
	return nil
}
