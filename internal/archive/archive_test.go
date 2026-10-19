package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "include", "x"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "lib"), 0o755))
	must(os.WriteFile(filepath.Join(root, "include", "x", "a.h"), []byte("// a\n"), 0o644))
	must(os.WriteFile(filepath.Join(root, "lib", "libx.a"), []byte("binary"), 0o644))
	must(os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o755))
	return root
}

func TestRoundTrip(t *testing.T) {
	root := writeTree(t)
	dst := filepath.Join(t.TempDir(), "p.tar.gz")
	if err := Create(dst, root, map[string][]byte{"kiln-package.json": []byte("{}\n")}); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := Extract(dst, out); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"include/x/a.h": "// a\n", "lib/libx.a": "binary", "kiln-package.json": "{}\n"} {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(path)))
		if err != nil || string(got) != want {
			t.Errorf("%s: %q %v", path, got, err)
		}
	}
	if st, _ := os.Stat(filepath.Join(out, "run.sh")); runtime.GOOS != "windows" && (st == nil || st.Mode()&0o111 == 0) {
		t.Error("the executable bit must survive")
	}
}

func TestCreateIsDeterministic(t *testing.T) {
	root := writeTree(t)
	d := t.TempDir()
	a, b := filepath.Join(d, "a.tar.gz"), filepath.Join(d, "b.tar.gz")
	for _, dst := range []string{a, b} {
		if err := Create(dst, root, map[string][]byte{"z.json": []byte("1"), "a.json": []byte("2")}); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(root, anyTime, anyTime) // timestamps must not leak into the archive
	}
	sa, _, _ := SHA256(a)
	sb, _, _ := SHA256(b)
	if sa != sb {
		t.Error("identical input must give an identical archive")
	}
}

func TestCreateRejectsExtraFileCollision(t *testing.T) {
	root := writeTree(t)
	err := Create(filepath.Join(t.TempDir(), "p.tar.gz"), root, map[string][]byte{"run.sh": []byte("x")})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("%v", err)
	}
}

// craft builds a tar.gz with arbitrary (possibly hostile) entries.
func craft(t *testing.T, entries ...tar.Header) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		h := h
		body := "data"
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(body))
		}
	}
	tw.Close()
	gz.Close()
	p := filepath.Join(t.TempDir(), "evil.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractRefusesHostileArchives(t *testing.T) {
	cases := map[string]tar.Header{
		"parent traversal": {Name: "../evil.txt", Typeflag: tar.TypeReg, Mode: 0o644},
		"nested traversal": {Name: "a/../../evil.txt", Typeflag: tar.TypeReg, Mode: 0o644},
		"absolute path":    {Name: "/etc/evil.txt", Typeflag: tar.TypeReg, Mode: 0o644},
		"symlink":          {Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		"hardlink":         {Name: "hard", Typeflag: tar.TypeLink, Linkname: "other"},
		"device":           {Name: "dev", Typeflag: tar.TypeChar},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dst := filepath.Join(parent, "out")
			os.Mkdir(dst, 0o755)
			if err := Extract(craft(t, h), dst); err == nil {
				t.Error("must refuse")
			}
			if _, err := os.Stat(filepath.Join(parent, "evil.txt")); err == nil {
				t.Error("a file escaped the destination")
			}
		})
	}
}

func TestExtractRejectsNonGzip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.tar.gz")
	os.WriteFile(p, []byte("not an archive"), 0o644)
	if err := Extract(p, t.TempDir()); err == nil {
		t.Error("must fail")
	}
}
