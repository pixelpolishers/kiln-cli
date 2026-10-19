package cache

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pixelpolishers/kiln-cli/internal/archive"
	"github.com/pixelpolishers/kiln-core/manifest"
)

const id = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var k = Key{Name: "fmtlib/fmt", Version: "11.0.2", ID: id}

func pack(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "include"), 0o755)
	os.WriteFile(filepath.Join(root, "include", "a.h"), []byte("x"), 0o644)
	p := filepath.Join(t.TempDir(), "p.tar.gz")
	if err := archive.Create(p, root, nil); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPutHasMeta(t *testing.T) {
	c := Cache{Root: filepath.Join(t.TempDir(), "cache")}
	if c.Has(k) {
		t.Fatal("empty cache must not have the id")
	}
	e := Entry{Origin: "source-build", Source: &SourceRef{Repository: "https://x/y", Commit: "abc"}, Asset: manifest.Asset{ID: id}}
	if err := c.Put(k, pack(t), e); err != nil {
		t.Fatal(err)
	}
	if !c.Has(k) {
		t.Fatal("entry missing")
	}
	if _, err := os.Stat(filepath.Join(c.Dir(k), "include", "a.h")); err != nil {
		t.Error(err)
	}
	if got, err := c.Entry(k); err != nil || got.Asset.ID != id || got.Origin != "source-build" || got.Source.Commit != "abc" {
		t.Errorf("entry: %+v %v", got, err)
	}
	if err := c.Put(k, pack(t), Entry{}); err != nil {
		t.Errorf("putting an existing entry again must succeed: %v", err)
	}
	left, _ := filepath.Glob(filepath.Join(c.Root, ".unpack-*"))
	if len(left) != 0 {
		t.Errorf("temporary directories left behind: %v", left)
	}
}

func TestFailedPutLeavesNothing(t *testing.T) {
	c := Cache{Root: t.TempDir()}
	bad := filepath.Join(t.TempDir(), "bad.tar.gz")
	os.WriteFile(bad, []byte("not an archive"), 0o644)
	if err := c.Put(k, bad, Entry{}); err == nil {
		t.Fatal("want error")
	}
	if c.Has(k) {
		t.Error("a failed unpack must not look like a cache entry")
	}
	if left, _ := filepath.Glob(filepath.Join(c.Root, ".unpack-*")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}

func TestPutRejectsBadKeys(t *testing.T) {
	c := Cache{Root: t.TempDir()}
	for _, bad := range []Key{
		{"fmtlib/fmt", "11.0.2", ""}, {"fmtlib/fmt", "11.0.2", "../escape"}, {"fmtlib/fmt", "11.0.2", "short"},
		{"fmtlib/fmt", "11.0.2", strings.ToUpper(id)}, {"../x/y", "1.0.0", id}, {"fmtlib/../..", "1.0.0", id},
		{"fmtlib/fmt", "../1.0.0", id}, {"fmt", "1.0.0", id},
	} {
		if err := c.Put(bad, pack(t), Entry{}); err == nil {
			t.Errorf("key %+v must be rejected", bad)
		}
	}
}

func TestDirNamesThePackageAndVersion(t *testing.T) {
	c := Cache{Root: "/c"}
	want := filepath.FromSlash("/c/packages/fmtlib/fmt/11.0.2/0123456789ab")
	if got := c.Dir(k); got != want {
		t.Errorf("%s, want %s", got, want)
	}
}

func TestAnEntryForAnotherAssetIsNotAccepted(t *testing.T) {
	c := Cache{Root: t.TempDir()}
	if err := c.Put(k, pack(t), Entry{Origin: "binary", Asset: manifest.Asset{ID: id}}); err != nil {
		t.Fatal(err)
	}
	// same directory name, different full id: the first 12 characters collide
	other := Key{Name: k.Name, Version: k.Version, ID: id[:12] + strings.Repeat("0", 52)}
	if _, err := c.Entry(other); err == nil {
		t.Error("an entry whose full id differs must not be returned")
	}
}

func TestSourcePutAndGet(t *testing.T) {
	c := Cache{Root: filepath.Join(t.TempDir(), "cache")}
	if _, err := c.Source("fmtlib/fmt", "11.0.2"); err == nil {
		t.Fatal("empty cache must not have the source")
	}
	tmp, err := c.NewSourceTemp()
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(tmp, "CMakeLists.txt"), []byte("x"), 0o644)
	si := SourceInfo{Name: "fmtlib/fmt", Version: "11.0.2", Repository: "https://x/y", Tag: "11.0.2", Commit: "abc"}
	if err := c.PutSource(tmp, si); err != nil {
		t.Fatal(err)
	}
	got, err := c.Source("fmtlib/fmt", "11.0.2")
	if err != nil || got != si {
		t.Errorf("%+v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(c.SourceDir("fmtlib/fmt", "11.0.2"), "CMakeLists.txt")); err != nil {
		t.Error(err)
	}
	if _, err := c.Source("../x/y", "1.0.0"); err == nil {
		t.Error("a bad name must be rejected")
	}
}

func TestDefaultHonorsEnv(t *testing.T) {
	t.Setenv("KILN_CACHE_DIR", "/custom/dir")
	if c, err := Default(); err != nil || c.Root != "/custom/dir" {
		t.Errorf("%v %v", c, err)
	}
}

func TestListStaleRemoveAndPrune(t *testing.T) {
	c := Cache{Root: t.TempDir()}
	mk := func(rel, content string) string {
		p := filepath.Join(c.Root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mk("packages/v/lib/1.0.0/aaaaaaaaaaaa/lib/libx.a", "12345")
	mk("packages/v/lib/1.0.0/bbbbbbbbbbbb/lib/libx.a", "1234567")
	mk("sources/v/lib/1.0.0/src.c", "123")
	mk("logs/lib-1.0.0-aaaaaaaaaaaa.log", "12")
	old := mk(".build-old/x", "1")
	fresh := mk(".build-new/x", "1")
	mk("other-file", "keep")
	past := time.Now().Add(-2 * StaleTemp)
	if err := os.Chtimes(filepath.Dir(old), past, past); err != nil {
		t.Fatal(err)
	}

	items, err := c.List(Packages, Sources, Logs)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	var sizes []int64
	for _, it := range items {
		names = append(names, string(it.Part)+": "+it.Name)
		sizes = append(sizes, it.Size)
	}
	want := []string{"logs: lib-1.0.0-aaaaaaaaaaaa.log", "packages: v/lib 1.0.0 aaaaaaaaaaaa", "packages: v/lib 1.0.0 bbbbbbbbbbbb", "sources: v/lib 1.0.0"}
	if !reflect.DeepEqual(names, want) || !reflect.DeepEqual(sizes, []int64{2, 5, 7, 3}) {
		t.Errorf("%v %v", names, sizes)
	}

	stale, err := c.Stale(time.Now())
	if err != nil || len(stale) != 1 || stale[0].Name != ".build-old" {
		t.Errorf("only the old temporary directory is abandoned (%s is recent): %v %v", filepath.Base(filepath.Dir(fresh)), stale, err)
	}

	if err := c.Remove(items[1].Path); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(filepath.Join(c.Root, "..")); err == nil {
		t.Error("a path outside the cache must be refused")
	}
	if err := c.Remove(c.Root); err == nil {
		t.Error("the cache root itself must be refused")
	}
	// a read-only directory (some builds leave them) can still be removed
	ro := filepath.Dir(mk("packages/v/ro/1.0.0/cccccccccccc/dir/f", "x"))
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(filepath.Join(c.Root, "packages", "v", "ro")); err != nil {
		t.Errorf("read-only directory: %v", err)
	}
	if err := c.Remove(items[2].Path); err != nil {
		t.Fatal(err)
	}
	c.Prune(Packages)
	if _, err := os.Stat(filepath.Join(c.Root, "packages", "v")); err == nil {
		t.Error("empty directories are pruned")
	}
	if _, err := os.Stat(filepath.Join(c.Root, "packages")); err != nil {
		t.Error("the part directory itself stays")
	}
	if _, err := os.Stat(filepath.Join(c.Root, "other-file")); err != nil {
		t.Error("files kiln does not own are never touched")
	}
}

func TestACacheRootThatCannotBeACacheIsRefused(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, root := range []string{"", "/", ".", home} {
		if _, err := (Cache{Root: root}).List(Packages); err == nil {
			t.Errorf("%q must be refused", root)
		}
	}
	if items, err := (Cache{Root: filepath.Join(t.TempDir(), "nothing-yet")}).List(Packages); err != nil || len(items) != 0 {
		t.Errorf("a cache that does not exist is empty: %v %v", items, err)
	}
}
