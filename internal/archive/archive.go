// Package archive creates and unpacks the tar.gz files that carry built
// packages. Creation is deterministic (sorted entries, no timestamps or
// ownership) so identical input gives an identical file and hash. Unpacking
// refuses anything that could write outside the destination.
package archive

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Create writes a tar.gz of everything under root, plus the extra files
// (archive path -> content). Symbolic links are stored as copies of their target.
func Create(dst, root string, extra map[string][]byte) error {
	type entry struct {
		name string
		abs  string
		dir  bool
		data []byte
		mode int64
	}
	var entries []entry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return err
		}
		info, err := os.Stat(p) // follows symlinks
		if err != nil {
			return err
		}
		e := entry{name: filepath.ToSlash(rel), abs: p, dir: info.IsDir()}
		if !e.dir {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s: only regular files can be packaged", rel)
			}
			e.mode = 0o644
			if info.Mode()&0o111 != 0 {
				e.mode = 0o755
			}
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return err
	}
	for name, data := range extra {
		for _, e := range entries {
			if e.name == name {
				return fmt.Errorf("%s already exists in the package directory", name)
			}
		}
		entries = append(entries, entry{name: name, data: data, mode: 0o644})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	gz, err := gzip.NewWriterLevel(out, gzip.BestCompression)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: e.mode, Format: tar.FormatPAX}
		switch {
		case e.dir:
			h.Typeflag, h.Name, h.Mode = tar.TypeDir, e.name+"/", 0o755
		case e.abs != "":
			f, err := os.Open(e.abs)
			if err != nil {
				return err
			}
			st, err := f.Stat()
			if err != nil {
				f.Close()
				return err
			}
			h.Typeflag, h.Size = tar.TypeReg, st.Size()
			if err := tw.WriteHeader(h); err != nil {
				f.Close()
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			if err != nil {
				return err
			}
			continue
		default:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.data))
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			if _, err := tw.Write(e.data); err != nil {
				return err
			}
			continue
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return out.Close()
}

// maxExtracted bounds the total unpacked size (a decompression bomb guard).
const maxExtracted = 2 << 30

// Extract unpacks src into dst (which must exist). It accepts only regular
// files and directories whose names stay inside dst.
func Extract(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s: not a gzip file: %w", filepath.Base(src), err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(src), err)
		}
		name := path.Clean(strings.TrimSuffix(h.Name, "/"))
		if name == "." {
			continue
		}
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, `\`) {
			return fmt.Errorf("unsafe path in archive: %q", h.Name)
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if total += h.Size; total > maxExtracted {
				return fmt.Errorf("archive is larger than %d bytes when unpacked", int64(maxExtracted))
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, io.LimitReader(tr, h.Size)); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported entry %q (type %c): only files and directories are allowed", h.Name, h.Typeflag)
		}
	}
}

// SHA256 returns the hex SHA-256 and the size of a file.
func SHA256(file string) (sum string, size int64, err error) {
	f, err := os.Open(file)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err = io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}
