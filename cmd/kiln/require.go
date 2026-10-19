package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pixelpolishers/kiln-cli/internal/client"
	"github.com/pixelpolishers/kiln-cli/internal/install"
	"github.com/pixelpolishers/kiln-cli/internal/project"
	"github.com/pixelpolishers/kiln-core/api"
	"github.com/pixelpolishers/kiln-core/constraint"
	"github.com/pixelpolishers/kiln-core/manifest"
	"github.com/pixelpolishers/kiln-core/semver"
)

// readManifest returns the project's kiln.json as written. existed is false,
// with no error, when there is none yet.
func readManifest(dir string) (path string, raw []byte, existed bool, err error) {
	path = filepath.Join(dir, project.ManifestFile)
	raw, err = os.ReadFile(path)
	if os.IsNotExist(err) {
		return path, nil, false, nil
	}
	return path, raw, err == nil, err
}

// writeFile replaces a file atomically.
func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type requirement struct{ name, constraint string }

// chooseConstraint decides what to write for a package: the constraint the user
// gave, or ^<latest stable version>. It checks that the registry has the
// package and a version that satisfies the constraint.
func chooseConstraint(ctx context.Context, c *client.Client, name, given string) (string, error) {
	pkg, err := c.Package(ctx, name)
	if err != nil {
		if client.IsNotFound(err) && !strings.Contains(err.Error(), "not a kiln registry") {
			return "", fmt.Errorf("package %s not found in the registry (try `kiln search`)", name)
		}
		return "", err
	}
	if given == "" {
		latest := newestStable(pkg)
		if latest == "" {
			return "", fmt.Errorf("%s has no installable stable version; name one explicitly, e.g. %s:1.0.0-rc.1", name, name)
		}
		return "^" + latest, nil
	}
	con, err := constraint.Parse(given)
	if err != nil {
		return "", err
	}
	if !anyMatch(pkg, con) {
		return "", fmt.Errorf("no version of %s matches %s (available: %s)", name, given, versionList(pkg))
	}
	return given, nil
}

// newestStable is the highest unflagged, non-prerelease version. The registry
// reports one as "latest", but the CLI does not depend on that field.
func newestStable(pkg api.PackageDetail) string {
	best := ""
	var bestV semver.Version
	for _, v := range pkg.Versions {
		sv, err := semver.Parse(v.Version)
		if err != nil || v.Flagged || sv.IsPrerelease() {
			continue
		}
		if best == "" || semver.Compare(sv, bestV) > 0 {
			best, bestV = sv.String(), sv
		}
	}
	return best
}

func anyMatch(pkg api.PackageDetail, con constraint.Constraint) bool {
	for _, v := range pkg.Versions {
		if sv, err := semver.Parse(v.Version); err == nil && !v.Flagged && con.Matches(sv) {
			return true
		}
	}
	return false
}

// applyAndInstall writes the edited manifest, installs, and puts things back if
// that fails: the original manifest, or no manifest at
// all if there was none before.
func applyAndInstall(io IO, f *installFlags, path string, original, edited []byte, noInstall bool, cfg install.Config) int {
	v, err := manifest.NewValidator()
	if err != nil {
		return fail(io, err)
	}
	if _, _, err := v.Parse(edited); err != nil {
		fmt.Fprintf(io.Err, "kiln: the edited %s would be invalid, nothing was changed:\n%v\n", project.ManifestFile, err)
		return exitError
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fail(io, err)
	}
	if err := writeFile(path, edited); err != nil {
		return fail(io, err)
	}
	if noInstall {
		fmt.Fprintf(io.Out, "%s updated. kiln.lock is now out of date; run `kiln update` to resolve and install.\n", project.ManifestFile)
		return exitOK
	}
	code := f.run(io, cfg)
	if code != exitOK {
		switch {
		case original == nil:
			if err := os.Remove(path); err != nil {
				fmt.Fprintf(io.Err, "kiln: could not remove the %s it created: %v\n", project.ManifestFile, err)
			} else {
				fmt.Fprintf(io.Err, "kiln: installation failed, the %s it created was removed again.\n", project.ManifestFile)
			}
		default:
			if err := writeFile(path, original); err != nil {
				fmt.Fprintf(io.Err, "kiln: could not restore %s: %v\n", project.ManifestFile, err)
			} else {
				fmt.Fprintf(io.Err, "kiln: installation failed, %s was reverted to its original content.\n", project.ManifestFile)
			}
		}
	}
	return code
}

func rejectCheckAndLocked(io IO, f *installFlags, cmd string) bool {
	if *f.checkOnly || *f.locked {
		fmt.Fprintf(io.Err, "kiln: --check and --locked do not apply to %s\n", cmd)
		return true
	}
	return false
}

func cmdRequire(args []string, io IO) int {
	fs := newFlagSet("require", io)
	f := addInstallFlags(fs)
	dev := fs.Bool("dev", false, "add to require-dev instead of require (dependencies only the root project needs)")
	noInstall := fs.Bool("no-install", false, "only edit kiln.json; run `kiln update` afterwards")
	fs.Usage = func() {
		fmt.Fprintln(io.Err, "Usage: kiln require <vendor/pkg>[:<constraint>]... [--dev] [--no-install] [-d DIR]")
		fmt.Fprintln(io.Err, "\nAdds packages to kiln.json and installs them. Without a constraint the newest stable version is used")
		fmt.Fprintln(io.Err, "and ^<version> is written. Everything already in kiln.lock stays at its locked version; only the")
		fmt.Fprintln(io.Err, "new packages (and what they need) are resolved. If the install fails, kiln.json is put back.")
		fmt.Fprintln(io.Err, "\nExamples:  kiln require gabime/spdlog     kiln require nlohmann/json:3.12.0     kiln require catch2/catch2:^3 --dev")
		printFlags(fs, io.Err)
	}
	pos, code := parse(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) == 0 {
		return usageErr(io, fs, "require needs at least one package, e.g. kiln require gabime/spdlog")
	}
	if rejectCheckAndLocked(io, f, "require") {
		return exitUsage
	}

	path, original, existed, err := readManifest(*f.dir)
	if err != nil {
		return fail(io, err)
	}
	base := original
	if !existed {
		base = project.Initial("") // a project that only uses libraries needs no name
		fmt.Fprintf(io.Out, "Creating %s\n", path)
	}
	section := project.SectionRequire
	if *dev {
		section = project.SectionRequireDev
	}
	c := client.New(f.common.registry)
	edited := base
	var names []string
	for _, arg := range pos {
		name, given := splitNameVersion(arg)
		chosen, err := chooseConstraint(context.Background(), c, name, given)
		if err != nil {
			return fail(io, err)
		}
		if edited, err = project.SetRequire(edited, section, name, chosen); err != nil {
			return fail(io, err)
		}
		fmt.Fprintf(io.Out, "Adding %s %s to %s\n", name, chosen, section)
		names = append(names, name)
	}
	return applyAndInstall(io, f, path, original, edited, *noInstall,
		install.Config{Mode: install.ModeUpdate, UpdatePackages: names, KeepLocked: true})
}

func cmdRemove(args []string, io IO) int {
	fs := newFlagSet("remove", io)
	f := addInstallFlags(fs)
	noInstall := fs.Bool("no-install", false, "only edit kiln.json; run `kiln update` afterwards")
	fs.Usage = func() {
		fmt.Fprintln(io.Err, "Usage: kiln remove <vendor/pkg>... [--no-install] [-d DIR]")
		fmt.Fprintln(io.Err, "\nRemoves packages from kiln.json (require or require-dev) and updates kiln.lock. Everything else stays")
		fmt.Fprintln(io.Err, "at its locked version; packages only the removed ones needed disappear from the lock.")
		printFlags(fs, io.Err)
	}
	pos, code := parse(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) == 0 {
		return usageErr(io, fs, "remove needs at least one package")
	}
	if rejectCheckAndLocked(io, f, "remove") {
		return exitUsage
	}
	path, original, existed, err := readManifest(*f.dir)
	if err != nil {
		return fail(io, err)
	}
	if !existed {
		return fail(io, fmt.Errorf("there is no %s in %s, so there is nothing to remove from", project.ManifestFile, *f.dir))
	}
	edited := original
	for _, name := range pos {
		var found bool
		if edited, found, err = project.RemoveRequire(edited, name); err != nil {
			return fail(io, err)
		}
		if !found {
			return fail(io, fmt.Errorf("%s is not in require or require-dev of %s; nothing was changed", name, project.ManifestFile))
		}
		fmt.Fprintf(io.Out, "Removing %s\n", name)
	}
	return applyAndInstall(io, f, path, original, edited, *noInstall,
		install.Config{Mode: install.ModeUpdate, KeepLocked: true})
}

func cmdInit(args []string, io IO) int {
	fs := newFlagSet("init", io)
	dir := fs.String("dir", ".", "directory `DIR` for the new kiln.json")
	alias(fs, "d", "dir")
	fs.Usage = func() {
		fmt.Fprintln(io.Err, "Usage: kiln init [vendor/name] [-d DIR]")
		fmt.Fprintln(io.Err, "\nCreates a minimal kiln.json. A project that only uses libraries needs no name, so by default there is none;")
		fmt.Fprintln(io.Err, "give one (vendor/name) if you are going to package or publish this project. (`kiln require` also creates")
		fmt.Fprintln(io.Err, "the file when it is missing, so init is optional.)")
		printFlags(fs, io.Err)
	}
	pos, code := parse(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) > 1 {
		return usageErr(io, fs, "init takes at most one name")
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return fail(io, err)
	}
	name := ""
	if len(pos) == 1 {
		name = pos[0]
	}
	path := filepath.Join(abs, project.ManifestFile)
	if _, err := os.Stat(path); err == nil {
		return fail(io, fmt.Errorf("%s already exists; it was not touched", path))
	}
	content := project.Initial(name)
	v, err := manifest.NewValidator()
	if err != nil {
		return fail(io, err)
	}
	if _, _, err := v.Parse(content); err != nil {
		fmt.Fprintf(io.Err, "kiln: %q is not a valid package name (want vendor/name, lowercase letters, digits, - _ .):\n%v\n", name, err)
		return exitUsage
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return fail(io, err)
	}
	if err := writeFile(path, content); err != nil {
		return fail(io, err)
	}
	what := "Created " + path
	if name != "" {
		what += " for " + name
	}
	fmt.Fprintf(io.Out, "%s. Add dependencies with `kiln require <vendor/pkg>`.\n", what)
	return exitOK
}
