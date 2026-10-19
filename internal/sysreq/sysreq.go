// Package sysreq checks that a machine has what a source build needs (tools,
// modules, development packages) and turns a failure into advice, before any
// build starts.
package sysreq

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pixelpolishers/kiln-core/manifest"
	"github.com/pixelpolishers/kiln-core/when"
)

// Platform selects install hints: the operating system and the distribution
// ids (most specific first, e.g. ["ubuntu", "debian"]).
type Platform struct {
	OS     string
	Distro []string
}

// Env is everything a check needs from the machine.
type Env struct {
	LookPath func(string) (string, error)
	// Probe runs a command and reports a non-zero exit (or failure to start) as an error.
	Probe func(argv []string) error
	// Glob lists the paths matching a pattern, Getenv reads an environment variable;
	// both default to the real ones.
	Glob     func(pattern string) ([]string, error)
	Getenv   func(string) string
	Platform Platform
}

// SystemEnv returns the environment of this machine.
func SystemEnv(osName string) Env {
	return Env{
		LookPath: exec.LookPath,
		Probe: func(argv []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			return exec.CommandContext(ctx, argv[0], argv[1:]...).Run()
		},
		Platform: Platform{OS: osName, Distro: Distro("/etc/os-release")},
	}
}

// Distro reads the distribution ids from an os-release file (ID, then ID_LIKE).
// It returns nil if the file cannot be read.
func Distro(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var id string
	var like []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch k {
		case "ID":
			id = v
		case "ID_LIKE":
			like = strings.Fields(v)
		}
	}
	var out []string
	if id != "" {
		out = append(out, id)
	}
	return append(out, like...)
}

// Result is the outcome of one requirement.
type Result struct {
	Req    manifest.Requirement
	OK     bool
	Reason string // why it failed
	Hint   string // how to fix it, if known
}

// Implicit returns the requirements every source build of m has, whether or
// not the manifest lists them.
func Implicit(m manifest.Package) []manifest.Requirement {
	reqs := []manifest.Requirement{{
		Name: "git (to fetch the source)", Tool: "git",
		Hint: manifest.Hint{Default: "install git", By: map[string]string{
			"fedora": "sudo dnf install git", "debian": "sudo apt install git",
			"macos": "xcode-select --install", "windows": "install Git for Windows from https://git-scm.com/download/win",
		}},
	}}
	if m.Build != nil && m.Build.System == "cmake" {
		reqs = append(reqs, manifest.Requirement{
			Name: "cmake (the package builds with CMake)", Tool: "cmake",
			Hint: manifest.Hint{Default: "install CMake 3.15 or newer from https://cmake.org/download/", By: map[string]string{
				"fedora": "sudo dnf install cmake", "debian": "sudo apt install cmake", "macos": "brew install cmake",
			}},
		})
	}
	return reqs
}

// Check evaluates requirements. Those whose "when" does not hold under ctx
// (see when.Context) are skipped. The returned results cover only the ones
// that apply.
func Check(reqs []manifest.Requirement, ctx map[string]string, env Env) []Result {
	var out []Result
	for _, r := range reqs {
		if !when.Match(r.When, ctx) {
			continue
		}
		res := Result{Req: r, OK: true}
		if reason := evaluate(r, env); reason != "" {
			res.OK, res.Reason, res.Hint = false, reason, hintFor(r.Hint, env.Platform)
		}
		out = append(out, res)
	}
	return out
}

func evaluate(r manifest.Requirement, env Env) (failure string) {
	switch {
	case r.Tool != "":
		if _, err := env.LookPath(r.Tool); err != nil {
			return fmt.Sprintf("%q was not found on PATH", r.Tool)
		}
	case len(r.Check) > 0:
		if err := env.Probe(r.Check); err != nil {
			return fmt.Sprintf("`%s` failed", strings.Join(r.Check, " "))
		}
	case r.PkgConfig != "":
		if _, err := env.LookPath("pkg-config"); err != nil {
			return "pkg-config is not installed, so " + fmt.Sprintf("%q", r.PkgConfig) + " cannot be looked up"
		}
		if err := env.Probe([]string{"pkg-config", "--exists", r.PkgConfig}); err != nil {
			return fmt.Sprintf("pkg-config does not know %q", r.PkgConfig)
		}
	case r.File != "":
		glob := env.Glob
		if glob == nil {
			glob = filepath.Glob
		}
		getenv := env.Getenv
		if getenv == nil {
			getenv = os.Getenv
		}
		path := os.Expand(r.File, getenv)
		if matches, err := glob(path); err != nil || len(matches) == 0 {
			return fmt.Sprintf("nothing exists at %s", path)
		}
	case r.Env != "":
		getenv := env.Getenv
		if getenv == nil {
			getenv = os.Getenv
		}
		if getenv(r.Env) == "" {
			return fmt.Sprintf("the environment variable %s is not set", r.Env)
		}
	default:
		return "the requirement names no tool, check, pkg-config package, file or environment variable"
	}
	return ""
}

func hintFor(h manifest.Hint, p Platform) string {
	for _, id := range p.Distro {
		if s, ok := h.By[id]; ok {
			return s
		}
	}
	if s, ok := h.By[p.OS]; ok {
		return s
	}
	return h.Default
}

// Failure is a failed requirement of a particular package.
type Failure struct {
	Package string
	Version string
	// Use is true for a requirement of using the package (system-requires), as opposed to building it.
	Use bool
	Result
}

// Required returns the failures that block a build; Optional those that only
// warn.
func Split(fs []Failure) (required, optional []Failure) {
	for _, f := range fs {
		if f.Req.Optional {
			optional = append(optional, f)
		} else {
			required = append(required, f)
		}
	}
	return
}

// Format renders failures grouped by package, with a hint under each.
func Format(fs []Failure) string {
	byPkg := map[string][]Failure{}
	var order []string
	for _, f := range fs {
		key := f.Package + " " + f.Version
		if _, seen := byPkg[key]; !seen {
			order = append(order, key)
		}
		byPkg[key] = append(byPkg[key], f)
	}
	sort.Strings(order)
	var b strings.Builder
	for _, key := range order {
		fmt.Fprintf(&b, "  %s\n", key)
		for _, f := range byPkg[key] {
			use := ""
			if f.Use {
				use = " (needed to use the package)"
			}
			fmt.Fprintf(&b, "    missing: %s%s\n      %s\n", f.Req.Name, use, f.Reason)
			if f.Hint != "" {
				fmt.Fprintf(&b, "      to fix:  %s\n", f.Hint)
			}
		}
	}
	return b.String()
}
