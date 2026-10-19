package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pixelpolishers/kiln-cli/internal/build"
	"github.com/pixelpolishers/kiln-cli/internal/install"
	"github.com/pixelpolishers/kiln-cli/internal/source"
)

func cmdInstall(args []string, io IO) int {
	return runInstall("install", args, io, install.ModeInstall)
}
func cmdUpdate(args []string, io IO) int { return runInstall("update", args, io, install.ModeUpdate) }

// installFlags are the options shared by install, update, require and remove.
type installFlags struct {
	common     *commonFlags
	dir        *string
	local      string
	binaryOnly *bool
	sourceOnly *bool
	jobs       *int
	verbose    *bool
	checkOnly  *bool
	noSources  *bool
	locked     *bool
	sflags     stringList
	oflags     stringList
}

func addInstallFlags(fs *flag.FlagSet) *installFlags {
	f := &installFlags{common: addCommon(fs)}
	f.dir = fs.String("dir", ".", "project directory `DIR`")
	alias(fs, "d", "dir")
	localFlag(fs, &f.local)
	f.binaryOnly = fs.Bool("binary-only", false, "never build from source")
	f.sourceOnly = fs.Bool("source-only", false, "never use a prebuilt binary (already cached builds are reused)")
	f.jobs = fs.Int("jobs", 0, "parallel build jobs `N` (default: number of CPUs)")
	alias(fs, "j", "jobs")
	f.verbose = fs.Bool("verbose", false, "show the output of source builds")
	alias(fs, "v", "verbose")
	f.noSources = fs.Bool("no-sources", os.Getenv("KILN_NO_SOURCES") == "1", "do not copy the library sources into vendor/kiln (also: KILN_NO_SOURCES=1)")
	f.checkOnly = fs.Bool("check", false, "check the system requirements of the builds that would be needed, then stop (nothing is built or written)")
	f.locked = fs.Bool("locked", false, "fail unless an up-to-date kiln.lock exists; never create one (for CI)")
	fs.Var(&f.sflags, "setting", "override a build setting `key=value`, e.g. -s build_type=debug (repeatable)")
	alias(fs, "s", "setting")
	fs.Var(&f.oflags, "option", "set a package option `vendor/pkg:option=value`, e.g. -o fmtlib/fmt:header_only=true (repeatable)")
	alias(fs, "o", "option")
	return f
}

// run installs the project in --dir with the given mode settings and prints the outcome.
func (f *installFlags) run(io IO, cfg install.Config) int {
	if *f.binaryOnly && *f.sourceOnly {
		fmt.Fprintln(io.Err, "kiln: --binary-only and --source-only exclude each other")
		return exitUsage
	}
	st, detected, code := loadSettings(io, f.sflags)
	if code >= 0 {
		return code
	}
	reqs, err := parseOptionFlags(f.oflags)
	if err != nil {
		fmt.Fprintf(io.Err, "kiln: %v\n", err)
		return exitUsage
	}
	env, _, err := installEnv(f.common.registry, f.local, st.OS, io)
	if err != nil {
		return fail(io, err)
	}
	env.Policy = install.Policy{BinaryOnly: *f.binaryOnly, SourceOnly: *f.sourceOnly}
	env.Fetch = source.Git{AllowFile: os.Getenv("KILN_ALLOW_FILE_SOURCES") == "1"}
	env.Detected = &detected.Settings
	env.Jobs = *f.jobs
	if cxx := detected.Compiler; cxx != "" {
		env.Toolchain.CXX = cxx
		env.Toolchain.CC = os.Getenv("CC")
		if fields := strings.Fields(env.Toolchain.CC); len(fields) > 0 {
			env.Toolchain.CC = fields[0]
		} else {
			env.Toolchain.CC = build.CFor(cxx)
		}
	}
	if *f.verbose {
		env.BuildOutput = io.Err
	}

	abs, err := filepath.Abs(*f.dir)
	if err != nil {
		return fail(io, err)
	}
	fmt.Fprintf(io.Out, "Installing for %s %s, %s %s, %s, %s\n", st.OS, st.Arch, st.Compiler.Name, st.Compiler.Version, st.BuildType, st.Linkage)
	cfg.Dir, cfg.Settings, cfg.Requests = abs, st, reqs
	cfg.CheckOnly = *f.checkOnly
	cfg.NoSources = *f.noSources
	cfg.RequireLock = *f.locked
	res, err := install.Run(context.Background(), env, cfg)
	if err != nil {
		return failInstall(io, err)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(io.Err, "warning: %s\n", w)
	}
	for _, n := range res.Notes {
		fmt.Fprintf(io.Err, "note: %s\n", n)
	}
	if *f.checkOnly {
		if len(res.ToBuild) == 0 {
			fmt.Fprintln(io.Out, "All system requirements are met. No source builds are needed: every package is cached or has a prebuilt binary.")
		} else {
			fmt.Fprintf(io.Out, "All system requirements are met, including those of the %d package(s) that need a source build: %s\n", len(res.ToBuild), strings.Join(res.ToBuild, ", "))
		}
		return exitOK
	}
	lockNote := "left unchanged"
	switch {
	case res.LockWritten && res.LockCreated:
		lockNote = "created (commit it so everyone gets the same versions)"
	case res.LockWritten:
		lockNote = "rewritten"
	case res.LockUsed:
		lockNote = "followed exactly, left unchanged"
	}
	fmt.Fprintf(io.Out, "%d package(s) ready.\n  build integration: %s\n  lockfile:          %s (%s)\n", len(res.Plan.Packages), res.CMakeFile, res.LockFile, lockNote)
	if res.SourcesDir != "" {
		fmt.Fprintf(io.Out, "  library sources:   %s\n", res.SourcesDir)
	}
	return exitOK
}

func runInstall(name string, args []string, io IO, mode install.Mode) int {
	fs := newFlagSet(name, io)
	f := addInstallFlags(fs)
	fs.Usage = func() {
		if mode == install.ModeUpdate {
			fmt.Fprintln(io.Err, "Usage: kiln update [vendor/pkg...] [-d DIR] [--local DIR] [-s key=value]... [-o vendor/pkg:option=value]...")
			fmt.Fprintln(io.Err, "\nResolves the dependencies in kiln.json again, installs them and rewrites kiln.lock. With package names,")
			fmt.Fprintln(io.Err, "only those packages (and anything new they need) may change; everything else stays at its locked version.")
		} else {
			fmt.Fprintln(io.Err, "Usage: kiln install [-d DIR] [--local DIR] [-s key=value]... [-o vendor/pkg:option=value]...")
			fmt.Fprintln(io.Err, "\nInstalls the dependencies in kiln.json. If kiln.lock exists it is followed exactly (and never changed);")
			fmt.Fprintln(io.Err, "otherwise versions are chosen and kiln.lock is created. Each binary comes from the cache, the local")
			fmt.Fprintln(io.Err, "repository, or a build from source, then DIR/vendor/kiln/kiln.cmake is written.")
			fmt.Fprintln(io.Err, "Building from source runs the package's own build scripts; use --binary-only to forbid it.")
		}
		printFlags(fs, io.Err)
	}
	pos, code := parse(fs, args)
	if code >= 0 {
		return code
	}
	if mode == install.ModeInstall && len(pos) > 0 {
		return usageErr(io, fs, "install takes no arguments (use kiln require to add a package, kiln update <package> to change a locked one)")
	}
	if mode == install.ModeUpdate && *f.locked {
		return usageErr(io, fs, "--locked makes no sense for update, which rewrites the lockfile")
	}
	return f.run(io, install.Config{Mode: mode, UpdatePackages: pos})
}
