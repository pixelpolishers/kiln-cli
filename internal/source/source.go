// Package source fetches the source code of a package version from git.
//
// Manifests are untrusted input and their repository URLs reach the git
// command line, so URLs are restricted to https (and file, only if enabled
// for tests), git's protocol allow-list is set, and values that could be read
// as options are rejected.
package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pixelpolishers/kiln-core/manifest"
	"github.com/pixelpolishers/kiln-core/semver"
)

// Request says which source to fetch.
type Request struct {
	Package    string
	Version    string
	Repository string   // git URL
	Tags       []string // candidate tags, tried in order
	Commit     string   // commit pinned by the registry; all zeros means "not pinned"
	Submodules bool     // also check out the repository's git submodules (recursively)
}

// Result describes what was fetched.
type Result struct {
	Commit string // the commit that was checked out
	Tag    string
	Pinned bool // the registry's pinned commit was verified
}

// Fetcher retrieves source into an empty directory.
type Fetcher interface {
	Fetch(ctx context.Context, req Request, dest string) (Result, error)
}

var (
	commitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
	zeroSHA  = strings.Repeat("0", 40)
	refRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+-]*$`)
)

// Git fetches with the git command line tool.
type Git struct {
	// AllowFile permits file:// repositories (tests and local development).
	AllowFile bool
}

// CheckURL reports whether a repository URL may be passed to git.
func (g Git) CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return fmt.Errorf("repository %q is not a URL", raw)
	}
	switch u.Scheme {
	case "https":
		if u.Host == "" {
			return fmt.Errorf("repository %q has no host", raw)
		}
		return nil
	case "file":
		if g.AllowFile {
			return nil
		}
	}
	return fmt.Errorf("repository %q: only https URLs are allowed", raw)
}

func (g Git) env() []string {
	proto := "https"
	if g.AllowFile {
		proto += ":file"
	}
	return append(os.Environ(), "GIT_ALLOW_PROTOCOL="+proto, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
}

func (g Git) run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = g.env()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// Fetch clones the first candidate tag that exists into dest (which must not
// exist or be empty) and checks the pinned commit.
func (g Git) Fetch(ctx context.Context, req Request, dest string) (Result, error) {
	if err := g.CheckURL(req.Repository); err != nil {
		return Result{}, err
	}
	if len(req.Tags) == 0 {
		return Result{}, fmt.Errorf("%s %s: no tag to fetch", req.Package, req.Version)
	}
	var failures []string
	for _, tag := range req.Tags {
		if !refRe.MatchString(tag) || strings.Contains(tag, "..") {
			return Result{}, fmt.Errorf("tag %q is not acceptable", tag)
		}
		_ = os.RemoveAll(dest)
		if _, err := g.run(ctx, "", "-c", "advice.detachedHead=false", "clone", "--quiet", "--depth", "1",
			"--branch="+tag, "--", req.Repository, dest); err != nil {
			failures = append(failures, tag+": "+firstLine(err.Error()))
			continue
		}
		commit, err := g.run(ctx, dest, "rev-parse", "HEAD")
		if err != nil {
			return Result{}, err
		}
		res := Result{Commit: commit, Tag: tag}
		if commitRe.MatchString(req.Commit) && req.Commit != zeroSHA {
			if commit != req.Commit {
				return Result{}, fmt.Errorf("tag %s of %s points at %s, but the registry pinned %s: the tag was moved, refusing to build", tag, req.Repository, commit, req.Commit)
			}
			res.Pinned = true
		}
		if req.Submodules {
			// the protocol allow-list in env() already limits submodule URLs to https
			args := []string{"submodule", "--quiet", "update", "--init", "--recursive", "--depth", "1"}
			if g.AllowFile {
				args = append([]string{"-c", "protocol.file.allow=always"}, args...)
			}
			if _, err := g.run(ctx, dest, args...); err != nil {
				return Result{}, fmt.Errorf("%s %s: submodules: %w", req.Package, req.Version, err)
			}
		}
		removeGit(dest) // builds must not depend on git state
		return res, nil
	}
	return Result{}, fmt.Errorf("could not fetch %s %s from %s (tried tags: %s)", req.Package, req.Version, req.Repository, strings.Join(failures, "; "))
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// Tags expands a tag template for a version into candidates. The placeholders are
// {version} (1.3.1), {major}, {minor} and {patch}, so curl-8_19_0 is "curl-{major}_{minor}_{patch}".
// {minor:02} and {patch:02} pad to two digits (re2 tags 2025-11-05, version 2025.11.5: "{major}-{minor:02}-{patch:02}").
// Upstream projects are not always 3-part semver (zlib tags 1.3, not 1.3.0),
// so shorter forms are tried after the full one; a form that lacks a part the
// template asks for is skipped.
func Tags(template, version string) []string {
	if template == "" {
		template = "v{version}"
	}
	v, err := semver.Parse(version)
	if err != nil {
		return nil
	}
	type form struct {
		text                string
		major, minor, patch string
	}
	pad := func(n string) string {
		if n != "" && len(n) < 2 {
			return "0" + n
		}
		return n
	}
	maj, min, pat := fmt.Sprint(v.Major), fmt.Sprint(v.Minor), fmt.Sprint(v.Patch)
	forms := []form{{v.String(), maj, min, pat}}
	if v.Pre == "" && v.Build == "" && v.Patch == 0 {
		forms = append(forms, form{maj + "." + min, maj, min, ""})
		if v.Minor == 0 {
			forms = append(forms, form{maj, maj, "", ""})
		}
	}
	var out []string
	for _, f := range forms {
		if (f.minor == "" && strings.Contains(template, "{minor")) || (f.patch == "" && strings.Contains(template, "{patch")) {
			continue
		}
		tag := strings.NewReplacer("{version}", f.text, "{major}", f.major, "{minor}", f.minor, "{patch}", f.patch,
			"{minor:02}", pad(f.minor), "{patch:02}", pad(f.patch)).Replace(template)
		if len(out) == 0 || out[len(out)-1] != tag {
			out = append(out, tag)
		}
	}
	return out
}

// Repository returns the git URL of a package: its repository.url, or the
// repository of a github provider.
func Repository(m manifest.Package) (string, error) {
	if m.Repository != nil && m.Repository.URL != "" {
		return m.Repository.URL, nil
	}
	for _, p := range m.Providers {
		if p.Type == "github" && p.Repository != "" {
			return "https://github.com/" + p.Repository, nil
		}
	}
	return "", fmt.Errorf("package %s declares no repository to build from", m.Name)
}

// TagTemplate returns the tag template of the first github provider.
func TagTemplate(m manifest.Package) string {
	for _, p := range m.Providers {
		if p.Type == "github" && p.Tag != "" {
			return p.Tag
		}
	}
	return "v{version}"
}

// removeGit deletes the .git directory of a checkout and, for submodules, the .git files inside it.
func removeGit(root string) {
	_ = os.RemoveAll(filepath.Join(root, ".git"))
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Name() == ".git" {
			_ = os.RemoveAll(path)
			if d.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
}
