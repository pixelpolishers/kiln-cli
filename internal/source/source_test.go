package source

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/pixelpolishers/kiln-core/manifest"
)

// fileURL is the file:// URL of a directory; on Windows it needs three slashes
// and forward slashes (file:///C:/dir).
func fileURL(dir string) string {
	d := filepath.ToSlash(dir)
	if !strings.HasPrefix(d, "/") {
		d = "/" + d
	}
	return "file://" + d
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// repo creates a git repository with two tagged commits and returns its file URL and commits.
func repo(t *testing.T) (url, first, second string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one"), 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "one")
	git(t, dir, "tag", "v1.0.0")
	first = git(t, dir, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two"), 0o644)
	git(t, dir, "commit", "-q", "-am", "two")
	git(t, dir, "tag", "1.3") // a two-part tag without a "v", like some upstream projects
	second = git(t, dir, "rev-parse", "HEAD")
	return fileURL(dir), first, second
}

func TestFetchTagAndPinnedCommit(t *testing.T) {
	url, first, _ := repo(t)
	g := Git{AllowFile: true}
	dest := filepath.Join(t.TempDir(), "src")
	res, err := g.Fetch(context.Background(), Request{Package: "a/b", Version: "1.0.0", Repository: url, Tags: []string{"v1.0.0"}, Commit: first}, dest)
	if err != nil || res.Commit != first || !res.Pinned || res.Tag != "v1.0.0" {
		t.Fatalf("%+v %v", res, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "a.txt")); string(b) != "one" {
		t.Errorf("checked out %q", b)
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); err == nil {
		t.Error(".git must be removed")
	}
}

func TestFetchRefusesMovedTag(t *testing.T) {
	url, _, second := repo(t)
	// the registry pinned the second commit for v1.0.0, but the tag points at the first
	_, err := Git{AllowFile: true}.Fetch(context.Background(), Request{Package: "a/b", Version: "1.0.0", Repository: url, Tags: []string{"v1.0.0"}, Commit: second}, filepath.Join(t.TempDir(), "src"))
	if err == nil || !strings.Contains(err.Error(), "was moved") {
		t.Errorf("%v", err)
	}
}

func TestFetchUnpinnedAndCandidateOrder(t *testing.T) {
	url, _, second := repo(t)
	zero := strings.Repeat("0", 40)
	// "1.3.0" does not exist, "1.3" does
	res, err := Git{AllowFile: true}.Fetch(context.Background(), Request{Package: "a/b", Version: "1.3.0", Repository: url, Tags: Tags("{version}", "1.3.0"), Commit: zero}, filepath.Join(t.TempDir(), "src"))
	if err != nil || res.Tag != "1.3" || res.Pinned || res.Commit != second {
		t.Fatalf("%+v %v", res, err)
	}
	_, err = Git{AllowFile: true}.Fetch(context.Background(), Request{Package: "a/b", Version: "9.9.9", Repository: url, Tags: []string{"v9.9.9"}}, filepath.Join(t.TempDir(), "src"))
	if err == nil || !strings.Contains(err.Error(), "tried tags: v9.9.9") {
		t.Errorf("%v", err)
	}
}

func TestFetchRefusesDangerousInput(t *testing.T) {
	url, _, _ := repo(t)
	dest := filepath.Join(t.TempDir(), "src")
	cases := map[string]Request{
		"ext transport": {Repository: "ext::sh -c touch /tmp/pwned", Tags: []string{"v1"}},
		"ssh":           {Repository: "ssh://git@github.com/a/b", Tags: []string{"v1"}},
		"plain http":    {Repository: "http://example.com/a/b", Tags: []string{"v1"}},
		"scp style":     {Repository: "git@github.com:a/b.git", Tags: []string{"v1"}},
		"file when off": {Repository: url, Tags: []string{"v1.0.0"}},
		"option as tag": {Repository: "https://example.com/a/b", Tags: []string{"--upload-pack=touch /tmp/pwned"}},
		"traversal tag": {Repository: "https://example.com/a/b", Tags: []string{"v1/../../x"}},
		"no tag":        {Repository: "https://example.com/a/b"},
		"empty":         {},
	}
	for name, req := range cases {
		if _, err := (Git{}).Fetch(context.Background(), req, dest); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
	if _, err := os.Stat("/tmp/pwned"); err == nil {
		t.Fatal("a command from a manifest ran")
	}
}

func TestTags(t *testing.T) {
	cases := []struct {
		tmpl, version string
		want          []string
	}{
		{"", "1.3.1", []string{"v1.3.1"}},
		{"v{version}", "1.3.0", []string{"v1.3.0", "v1.3"}},
		{"{version}", "11.0.2", []string{"11.0.2"}},
		{"release-{version}", "2.0.0", []string{"release-2.0.0", "release-2.0", "release-2"}},
		{"openssl-{version}", "3.3.2", []string{"openssl-3.3.2"}},
		{"v{version}", "2.0.0-rc.1", []string{"v2.0.0-rc.1"}},
		{"v{version}", "garbage", nil},
		{"curl-{major}_{minor}_{patch}", "8.19.0", []string{"curl-8_19_0"}},
		{"curl-{major}_{minor}_{patch}", "8.19.2", []string{"curl-8_19_2"}},
		{"r{major}.{minor}", "2.1.0", []string{"r2.1"}},
		{"r{major}", "2.0.0", []string{"r2"}},
		{"{major}-{minor:02}-{patch:02}", "2025.11.5", []string{"2025-11-05"}},
		{"{major}-{minor:02}-{patch:02}", "2025.8.12", []string{"2025-08-12"}},
	}
	for _, c := range cases {
		if got := Tags(c.tmpl, c.version); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Tags(%q, %q) = %v; want %v", c.tmpl, c.version, got, c.want)
		}
	}
}

func TestRepositoryAndTemplate(t *testing.T) {
	m := manifest.Package{Name: "a/b", Repository: &manifest.Repository{URL: "https://github.com/a/b"}, Providers: []manifest.Provider{{Type: "github", Tag: "rel-{version}"}}}
	if u, err := Repository(m); err != nil || u != "https://github.com/a/b" || TagTemplate(m) != "rel-{version}" {
		t.Errorf("%q %v %q", u, err, TagTemplate(m))
	}
	m = manifest.Package{Name: "a/b", Providers: []manifest.Provider{{Type: "github", Repository: "o/r"}}}
	if u, _ := Repository(m); u != "https://github.com/o/r" || TagTemplate(m) != "v{version}" {
		t.Errorf("%q", u)
	}
	if _, err := Repository(manifest.Package{Name: "a/b"}); err == nil {
		t.Error("no repository must be an error")
	}
}

func TestCopyKeepsFilesModesAndLinks(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(src, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a", "b", "f.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := runtime.GOOS != "windows" && os.Symlink("a/b/f.txt", filepath.Join(src, "link")) == nil

	dst := filepath.Join(t.TempDir(), "dst")
	if err := Copy(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "a", "b", "f.txt")); err != nil || string(b) != "hello" {
		t.Errorf("%q %v", b, err)
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(filepath.Join(dst, "run.sh")); err != nil || st.Mode().Perm()&0o100 == 0 {
			t.Errorf("the executable bit was lost: %v %v", st, err)
		}
	}
	if link {
		if got, err := os.Readlink(filepath.Join(dst, "link")); err != nil || got != "a/b/f.txt" {
			t.Errorf("link: %q %v", got, err)
		}
	}
}

func TestFetchChecksOutSubmodulesOnlyWhenAsked(t *testing.T) {
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "protocol.file.allow=always", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	main := filepath.Join(root, "main")
	for _, d := range []string{sub, main} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		run(d, "init", "--quiet", "-b", "main")
	}
	if err := os.WriteFile(filepath.Join(sub, "lib.txt"), []byte("lib"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(sub, "add", ".")
	run(sub, "commit", "--quiet", "-m", "sub")
	run(main, "submodule", "--quiet", "add", "file://"+sub, "3rdparty/sub")
	run(main, "commit", "--quiet", "-m", "main")
	run(main, "tag", "v1.0.0")

	without := filepath.Join(t.TempDir(), "src")
	if _, err := (Git{AllowFile: true}).Fetch(context.Background(), Request{Package: "a/b", Version: "1.0.0", Repository: "file://" + main, Tags: []string{"v1.0.0"}}, without); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(without, "3rdparty", "sub", "lib.txt")); err == nil {
		t.Error("submodules were not asked for")
	}

	with := filepath.Join(t.TempDir(), "src")
	if _, err := (Git{AllowFile: true}).Fetch(context.Background(), Request{Package: "a/b", Version: "1.0.0", Repository: "file://" + main, Tags: []string{"v1.0.0"}, Submodules: true}, with); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(with, "3rdparty", "sub", "lib.txt")); err != nil {
		t.Errorf("the submodule is missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(with, "3rdparty", "sub", ".git")); err == nil {
		t.Error("git state of the submodule was left behind")
	}
}
