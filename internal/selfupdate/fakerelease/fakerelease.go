// Package fakerelease serves a fake GitHub repository (a releases feed, and for
// each release the archive of this system and its SHA256SUMS) for the tests of
// self-update.
package fakerelease

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/pixelpolishers/kiln-cli/internal/selfupdate"
)

// archive builds the release archive of this system holding program.
func archive(t testing.TB, goos string, program []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if goos == "windows" {
		zw := zip.NewWriter(&buf)
		for _, f := range []struct{ name, body string }{{"LICENSE", "license"}, {"kiln.exe", string(program)}} {
			w, err := zw.Create(f.name)
			if err != nil {
				t.Fatal(err)
			}
			w.Write([]byte(f.body))
		}
		zw.Close()
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct{ name, body string }{{"LICENSE", "license"}, {"kiln", string(program)}} {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(f.body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// Release serves a fake repository: a feed with tags, and for each tag the
// archive of this system and a SHA256SUMS file. sumFor lets a test lie.
type Release struct {
	Tag     string
	Program string
	BadSum  bool
	NoSums  bool
}

func Server(t testing.TB, releases ...Release) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var feed strings.Builder
	feed.WriteString(`<feed>`)
	for _, r := range releases {
		fmt.Fprintf(&feed, `<entry><link rel="alternate" href="https://github.com/o/r/releases/tag/%s"/></entry>`, r.Tag)
		asset, err := selfupdate.AssetName(runtime.GOOS, runtime.GOARCH)
		if err != nil {
			t.Skip(err)
		}
		data := archive(t, runtime.GOOS, []byte(r.Program))
		sum := sha256.Sum256(data)
		hash := hex.EncodeToString(sum[:])
		if r.BadSum {
			hash = strings.Repeat("0", 64)
		}
		mux.HandleFunc("/releases/download/"+r.Tag+"/"+asset, func(w http.ResponseWriter, _ *http.Request) { w.Write(data) })
		sums := fmt.Sprintf("%s  %s\n%s  other.tar.gz\n", hash, asset, strings.Repeat("1", 64))
		if r.NoSums {
			sums = strings.Repeat("1", 64) + "  other.tar.gz\n"
		}
		mux.HandleFunc("/releases/download/"+r.Tag+"/SHA256SUMS", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(sums)) })
	}
	feed.WriteString(`</feed>`)
	mux.HandleFunc("/releases.atom", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(feed.String())) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
