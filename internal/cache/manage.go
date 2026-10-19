package cache

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Part is one of the three kinds of data in the cache.
type Part string

const (
	Packages Part = "packages" // built libraries
	Sources  Part = "sources"  // source checkouts
	Logs     Part = "logs"     // build logs
)

// Parts lists every part, in display order.
var Parts = []Part{Packages, Sources, Logs}

// StaleTemp is how old a leftover temporary directory must be before it counts as
// abandoned: a younger one may belong to a kiln that is running right now.
const StaleTemp = time.Hour

// Item is one thing in the cache.
type Item struct {
	Part Part
	// Name is the entry as shown: "vendor/name version id12" for a package, "vendor/name version" for a source.
	Name string
	Path string
	Size int64
}

// depth is how many directory levels below the part directory an entry lies.
var depth = map[Part]int{Packages: 4, Sources: 3, Logs: 1}

// guard refuses roots that cannot be a cache directory.
func (c Cache) guard() error {
	root := filepath.Clean(c.Root)
	if c.Root == "" || root == "." || root == string(filepath.Separator) || root == filepath.VolumeName(root)+string(filepath.Separator) {
		return fmt.Errorf("refusing to manage %q as a cache directory", c.Root)
	}
	if home, err := os.UserHomeDir(); err == nil && root == filepath.Clean(home) {
		return fmt.Errorf("refusing to manage your home directory %q as a cache directory (check KILN_CACHE_DIR)", c.Root)
	}
	return nil
}

// List returns every entry of the given parts, sorted by part and name. A cache that does
// not exist yet is empty.
func (c Cache) List(parts ...Part) ([]Item, error) {
	if err := c.guard(); err != nil {
		return nil, err
	}
	var items []Item
	for _, part := range parts {
		base := filepath.Join(c.Root, string(part))
		var walk func(dir string, level int) error
		walk = func(dir string, level int) error {
			entries, err := os.ReadDir(dir)
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			for _, e := range entries {
				p := filepath.Join(dir, e.Name())
				if level < depth[part] {
					if e.IsDir() {
						if err := walk(p, level+1); err != nil {
							return err
						}
					}
					continue
				}
				rel, _ := filepath.Rel(base, p)
				items = append(items, Item{Part: part, Name: displayName(part, rel), Path: p, Size: Size(p)})
			}
			return nil
		}
		if err := walk(base, 1); err != nil {
			return nil, err
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Part != items[j].Part {
			return items[i].Part < items[j].Part
		}
		return items[i].Name < items[j].Name
	})
	return items, nil
}

func displayName(part Part, rel string) string {
	segs := strings.Split(filepath.ToSlash(rel), "/")
	switch {
	case part == Packages && len(segs) == 4, part == Sources && len(segs) == 3:
		return segs[0] + "/" + segs[1] + " " + strings.Join(segs[2:], " ")
	}
	return strings.Join(segs, "/")
}

// Size is the total size in bytes of a file or directory tree (links are not followed).
func Size(path string) int64 {
	var n int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if info, err := d.Info(); err == nil && !d.IsDir() {
			n += info.Size()
		}
		return nil
	})
	return n
}

// Stale returns the temporary directories left behind by a kiln that was interrupted
// (unpacking, building, fetching), older than StaleTemp.
func (c Cache) Stale(now time.Time) ([]Item, error) {
	if err := c.guard(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(c.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Item
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() || !(strings.HasPrefix(n, ".unpack-") || strings.HasPrefix(n, ".build-") || strings.HasPrefix(n, ".src-")) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < StaleTemp {
			continue
		}
		p := filepath.Join(c.Root, n)
		out = append(out, Item{Name: n, Path: p, Size: Size(p)})
	}
	return out, nil
}

// Remove deletes one directory tree or file inside the cache. It refuses anything outside the
// cache root, and it makes read-only directories (some builds create them) writable first.
func (c Cache) Remove(path string) error {
	if err := c.guard(); err != nil {
		return err
	}
	rel, err := filepath.Rel(filepath.Clean(c.Root), filepath.Clean(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to remove %q: it is not inside the cache", path)
	}
	if err := os.RemoveAll(path); err == nil {
		return nil
	}
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
	return os.RemoveAll(path)
}

// Prune removes empty directories left in the part directories after entries were removed.
func (c Cache) Prune(parts ...Part) {
	for _, part := range parts {
		base := filepath.Join(c.Root, string(part))
		var prune func(dir string) bool
		prune = func(dir string) bool {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return false
			}
			for _, e := range entries {
				if e.IsDir() {
					prune(filepath.Join(dir, e.Name()))
				}
			}
			if rest, err := os.ReadDir(dir); err == nil && len(rest) == 0 && dir != base {
				return os.Remove(dir) == nil
			}
			return false
		}
		prune(base)
	}
}
