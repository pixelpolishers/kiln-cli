package selfupdate_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	. "github.com/pixelpolishers/kiln-cli/internal/selfupdate"
	"github.com/pixelpolishers/kiln-cli/internal/selfupdate/fakerelease"
)

func TestTagsAndNewest(t *testing.T) {
	srv := fakerelease.Server(t, fakerelease.Release{Tag: "v0.2.0-rc1"}, fakerelease.Release{Tag: "v0.1.0"}, fakerelease.Release{Tag: "v0.1.1"}, fakerelease.Release{Tag: "v0.1.0-rc1"})
	tags, err := New(srv.URL).Tags(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"v0.2.0-rc1", "v0.1.0", "v0.1.1", "v0.1.0-rc1"}; !reflect.DeepEqual(tags, want) {
		t.Errorf("tags %v, want %v", tags, want)
	}
	if got := Newest(tags, false); got != "v0.1.1" {
		t.Errorf("newest stable %q, want v0.1.1 (the feed lists newest first, but versions decide)", got)
	}
	if got := Newest(tags, true); got != "v0.2.0-rc1" {
		t.Errorf("newest including pre-releases %q, want v0.2.0-rc1", got)
	}
	if got := Newest([]string{"v0.1.0-rc1"}, false); got != "" {
		t.Errorf("only a pre-release exists: got %q, want none", got)
	}
	if got := Newest([]string{"nightly", "v1.0"}, false); got != "v1.0" {
		t.Errorf("tags that are not versions must be skipped, got %q", got)
	}
}

func TestAssetNames(t *testing.T) {
	for goos, want := range map[string]string{"linux": "kiln_linux_arm64.tar.gz", "windows": "kiln_windows_arm64.zip", "darwin": "kiln_darwin_arm64.tar.gz"} {
		if got, err := AssetName(goos, "arm64"); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", goos, got, err, want)
		}
	}
	if _, err := AssetName("plan9", "amd64"); err == nil {
		t.Error("an unsupported system must be refused")
	}
	if _, err := AssetName("freebsd", "arm64"); err == nil {
		t.Error("there is no freebsd/arm64 binary yet")
	}
}

func TestFetchVerifiesTheChecksum(t *testing.T) {
	ctx := context.Background()
	srv := fakerelease.Server(t, fakerelease.Release{Tag: "v1.0.0", Program: "NEW PROGRAM"}, fakerelease.Release{Tag: "v1.1.0", Program: "x", BadSum: true}, fakerelease.Release{Tag: "v1.2.0", Program: "x", NoSums: true})
	c := New(srv.URL)
	got, err := c.Fetch(ctx, "v1.0.0", runtime.GOOS, runtime.GOARCH)
	if err != nil || string(got) != "NEW PROGRAM" {
		t.Fatalf("got %q, %v", got, err)
	}
	var ve *VerifyError
	if _, err := c.Fetch(ctx, "v1.1.0", runtime.GOOS, runtime.GOARCH); !errors.As(err, &ve) || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("a wrong checksum must be a VerifyError, got %v", err)
	}
	if _, err := c.Fetch(ctx, "v1.2.0", runtime.GOOS, runtime.GOARCH); !errors.As(err, &ve) {
		t.Errorf("a release without a checksum for the archive must be refused, got %v", err)
	}
	var ne *NetworkError
	if _, err := c.Fetch(ctx, "v9.9.9", runtime.GOOS, runtime.GOARCH); !errors.As(err, &ne) {
		t.Errorf("a missing release is a NetworkError, got %v", err)
	}
}

func TestReplaceSwapsTheExecutable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "kiln")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Replace(exe, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Errorf("executable holds %q", b)
	}
	if st, _ := os.Stat(exe); runtime.GOOS != "windows" && st.Mode().Perm() != 0o755 {
		t.Errorf("mode %v, want 0755", st.Mode().Perm())
	}
	CleanUp(exe)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "kiln" {
			t.Errorf("left behind: %s", e.Name())
		}
	}
	if err := Replace(filepath.Join(dir, "no", "such", "kiln"), []byte("x")); err == nil {
		t.Error("an unwritable directory must be an error")
	}
}
