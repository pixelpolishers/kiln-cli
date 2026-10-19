package localrepo

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pixelpolishers/kiln-cli/internal/archive"
	"github.com/pixelpolishers/kiln-core/manifest"
)

const (
	id1    = "1111111111111111111111111111111111111111111111111111111111111111"
	commit = "0123456789abcdef0123456789abcdef01234567"
)

// fixture creates a packaged archive and its sidecar in a dist directory.
func fixture(t *testing.T, content string) (archivePath, sidecarPath string) {
	t.Helper()
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "f.txt"), []byte(content), 0o644)
	dist := t.TempDir()
	archivePath = filepath.Join(dist, "pkg-1.0.0-"+id1[:12]+".tar.gz")
	if err := archive.Create(archivePath, src, nil); err != nil {
		t.Fatal(err)
	}
	sum, size, _ := archive.SHA256(archivePath)
	a := manifest.Asset{
		ID:          id1,
		Settings:    map[string]any{"os": "linux", "arch": "x86_64", "compiler": map[string]any{"name": "gcc", "version": "13"}, "stdlib": "libstdc++", "cpp_standard": "17", "build_type": "release", "linkage": "static"},
		File:        manifest.File{URL: filepath.Base(archivePath), SHA256: sum, Size: size},
		Attestation: manifest.Attestation{Type: "unsigned"},
	}
	raw, _ := json.Marshal(a)
	sidecarPath = filepath.Join(dist, id1+SidecarSuffix)
	os.WriteFile(sidecarPath, raw, 0o644)
	return
}

func newRepo(t *testing.T) Repo {
	t.Helper()
	v, err := manifest.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	return Repo{Root: t.TempDir(), Validator: v}
}

var src = manifest.AssetSource{Repository: "https://github.com/acme/pkg", Commit: commit}

func TestAddFinalizeFind(t *testing.T) {
	r := newRepo(t)
	arc, side := fixture(t, "hello")
	if added, err := r.Add("acme/pkg", "1.0.0", arc, side); err != nil || !added {
		t.Fatalf("%v %v", added, err)
	}
	if _, _, err := r.Find("acme/pkg", "1.0.0", id1); !errors.Is(err, ErrNoAssets) {
		t.Errorf("before finalize: %v", err)
	}
	if n, err := r.Finalize("acme/pkg", "1.0.0", src); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	a, file, err := r.Find("acme/pkg", "1.0.0", id1)
	if err != nil || a.ID != id1 {
		t.Fatalf("%v %v", a, err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("file path does not exist: %v", err)
	}
	if _, _, err := r.Find("acme/pkg", "1.0.0", strings.Repeat("2", 64)); !errors.Is(err, ErrNoAsset) {
		t.Errorf("unknown id: %v", err)
	}
}

func TestAddIsIdempotentAndImmutable(t *testing.T) {
	r := newRepo(t)
	arc, side := fixture(t, "hello")
	r.Add("acme/pkg", "1.0.0", arc, side)
	if added, err := r.Add("acme/pkg", "1.0.0", arc, side); err != nil || added {
		t.Errorf("same content again: %v %v", added, err)
	}
	arc2, side2 := fixture(t, "DIFFERENT")
	if _, err := r.Add("acme/pkg", "1.0.0", arc2, side2); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Errorf("different binary under an existing id: %v", err)
	}
}

func TestAddValidatesInput(t *testing.T) {
	r := newRepo(t)
	arc, side := fixture(t, "hello")
	os.WriteFile(arc, []byte("corrupted"), 0o644)
	if _, err := r.Add("acme/pkg", "1.0.0", arc, side); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Errorf("archive not matching its sidecar: %v", err)
	}
	arc, side = fixture(t, "hello")
	other := filepath.Join(filepath.Dir(arc), "other.tar.gz")
	os.Rename(arc, other)
	if _, err := r.Add("acme/pkg", "1.0.0", other, side); err == nil {
		t.Error("sidecar naming a different file must be refused")
	}
}

func TestFinalizeRefusesEmptyAndInvalid(t *testing.T) {
	r := newRepo(t)
	if _, err := r.Finalize("acme/pkg", "1.0.0", src); err == nil {
		t.Error("nothing published")
	}
	arc, side := fixture(t, "hello")
	r.Add("acme/pkg", "1.0.0", arc, side)
	if _, err := r.Finalize("acme/pkg", "1.0.0", manifest.AssetSource{Repository: "https://x", Commit: "short"}); err == nil {
		t.Error("an invalid commit must fail schema validation")
	}
}

func TestFindRejectsEscapingFileURL(t *testing.T) {
	r := newRepo(t)
	arc, side := fixture(t, "hello")
	r.Add("acme/pkg", "1.0.0", arc, side)
	r.Finalize("acme/pkg", "1.0.0", src)
	p := filepath.Join(r.Root, "acme", "pkg", "1.0.0", AssetManifestFile)
	raw, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(raw), filepath.Base(arc), "../../../../etc/passwd", 1)), 0o644)
	if _, _, err := r.Find("acme/pkg", "1.0.0", id1); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Errorf("%v", err)
	}
}

func TestManifestMustMatchItsLocation(t *testing.T) {
	r := newRepo(t)
	arc, side := fixture(t, "hello")
	r.Add("acme/pkg", "1.0.0", arc, side)
	r.Finalize("acme/pkg", "1.0.0", src)
	from := filepath.Join(r.Root, "acme", "pkg", "1.0.0")
	to := filepath.Join(r.Root, "evil", "other", "1.0.0")
	os.MkdirAll(filepath.Dir(to), 0o755)
	os.Rename(from, to)
	if _, err := r.Manifest("evil/other", "1.0.0"); err == nil || !strings.Contains(err.Error(), "is for acme/pkg") {
		t.Errorf("a manifest copied to another package's place must be refused: %v", err)
	}
}
