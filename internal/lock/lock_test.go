package lock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pixelpolishers/kiln-core/manifest"
)

func validator(t *testing.T) *manifest.Validator {
	t.Helper()
	v, err := manifest.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func sample() *File {
	return &File{
		LockVersion: Version,
		ContentHash: "abc",
		Packages: []Package{
			{Name: "fmtlib/fmt", Version: "11.0.2", Commit: strings.Repeat("a", 40)},
			{Name: "gabime/spdlog", Version: "1.14.1", Requires: []string{"fmtlib/fmt"}},
		},
	}
}

func TestRoundTrip(t *testing.T) {
	v := validator(t)
	path := filepath.Join(t.TempDir(), "kiln.lock")
	if err := sample().Write(path, v); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path, v)
	if err != nil || got == nil {
		t.Fatalf("%v %v", got, err)
	}
	if got.ContentHash != "abc" || len(got.Packages) != 2 || got.Packages[0].Commit != strings.Repeat("a", 40) {
		t.Errorf("%+v", got)
	}
	if p, ok := got.Get("gabime/spdlog"); !ok || p.Requires[0] != "fmtlib/fmt" || p.Commit != "" {
		t.Errorf("%+v %v", p, ok)
	}
	if _, ok := got.Get("nope/nope"); ok {
		t.Error("Get must report absent packages")
	}
	vs, err := got.Versions()
	if err != nil || vs["fmtlib/fmt"].String() != "11.0.2" {
		t.Errorf("%v %v", vs, err)
	}
	if left, _ := filepath.Glob(path + ".tmp"); len(left) != 0 {
		t.Error("temporary file left behind")
	}
}

// The lock must not contain anything about the machine that wrote it.
func TestLockHoldsNothingAboutTheMachine(t *testing.T) {
	v := validator(t)
	path := filepath.Join(t.TempDir(), "kiln.lock")
	if err := sample().Write(path, v); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var doc map[string]any
	json.Unmarshal(raw, &doc)
	for _, key := range []string{"settings", "os", "arch", "compiler"} {
		if _, ok := doc[key]; ok {
			t.Errorf("top-level %q must not be in the lock", key)
		}
	}
	for _, pkg := range doc["packages"].([]any) {
		for _, key := range []string{"id", "origin", "file", "source", "options", "settings"} {
			if _, ok := pkg.(map[string]any)[key]; ok {
				t.Errorf("package field %q is machine-specific and must not be in the lock", key)
			}
		}
	}
	// and the schema refuses such fields if someone adds them
	bad := strings.Replace(string(raw), `"version": "11.0.2"`, `"version": "11.0.2", "id": "x"`, 1)
	if err := v.ValidateLock([]byte(bad)); err == nil {
		t.Error("a package entry with a machine-specific field must not validate")
	}
	withSettings := strings.Replace(string(raw), `"packages"`, `"settings": {}, "packages"`, 1)
	if err := v.ValidateLock([]byte(withSettings)); err == nil {
		t.Error("a lock with a settings section must not validate")
	}
}

func TestSameVersionsGiveTheSameBytes(t *testing.T) {
	v := validator(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.lock"), filepath.Join(dir, "b.lock")
	sample().Write(a, v)
	sample().Write(b, v)
	x, _ := os.ReadFile(a)
	y, _ := os.ReadFile(b)
	if string(x) != string(y) {
		t.Error("writing the same lock twice must give identical bytes")
	}
}

func TestReadAbsentAndInvalid(t *testing.T) {
	v := validator(t)
	dir := t.TempDir()
	if f, err := Read(filepath.Join(dir, "nope.lock"), v); f != nil || err != nil {
		t.Errorf("an absent lock is not an error: %v %v", f, err)
	}
	for name, content := range map[string]string{
		"not json":       "{",
		"missing fields": `{"lock-version":2}`,
		"wrong version":  `{"lock-version":3,"content-hash":"x","packages":[]}`,
		"bad commit":     `{"lock-version":2,"content-hash":"x","packages":[{"name":"a/b","version":"1.0.0","commit":"short"}]}`,
		"bad name":       `{"lock-version":2,"content-hash":"x","packages":[{"name":"NoVendor","version":"1.0.0"}]}`,
	} {
		p := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".lock")
		os.WriteFile(p, []byte(content), 0o644)
		if f, err := Read(p, v); err == nil {
			t.Errorf("%s: must be rejected, got %+v", name, f)
		}
	}
}

func TestOldFormatIsRecognized(t *testing.T) {
	v := validator(t)
	p := filepath.Join(t.TempDir(), "kiln.lock")
	os.WriteFile(p, []byte(`{"lock-version":1,"content-hash":"x","settings":{"os":"linux"},"packages":[]}`), 0o644)
	if _, err := Read(p, v); err != ErrOldFormat {
		t.Errorf("a version 1 lock must be reported as such, got %v", err)
	}
}

func TestVersionsRejectsGarbage(t *testing.T) {
	f := &File{Packages: []Package{{Name: "a/b", Version: "not-a-version"}}}
	if _, err := f.Versions(); err == nil {
		t.Error("want error")
	}
}

func TestEmptyLockIsValid(t *testing.T) {
	v := validator(t)
	f := &File{LockVersion: Version, ContentHash: "abc"} // nil packages
	path := filepath.Join(t.TempDir(), "kiln.lock")
	if err := f.Write(path, v); err != nil {
		t.Fatalf("a project without dependencies has a valid, empty lock: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"packages": []`) {
		t.Errorf("packages must be an empty list, not null:\n%s", raw)
	}
}
