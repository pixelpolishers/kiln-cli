// Package selfupdate replaces the running kiln executable by a newer release.
//
// Releases are published on GitHub by the release workflow: one archive per
// system (kiln_<os>_<arch>.tar.gz, kiln_windows_<arch>.zip) and a SHA256SUMS
// file. The newest release is read from the releases feed, which, unlike the
// GitHub API, has no rate limit for people who share an address.
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/pixelpolishers/kiln-core/semver"
)

// DefaultBaseURL is the repository the releases of kiln are published in.
const DefaultBaseURL = "https://github.com/pixelpolishers/kiln-cli"

// maxDownload bounds what is read from the network or from an archive.
const maxDownload = 200 << 20

// NetworkError reports that a release could not be fetched.
type NetworkError struct {
	URL string
	Err error
}

func (e *NetworkError) Error() string { return fmt.Sprintf("cannot fetch %s: %v", e.URL, e.Err) }
func (e *NetworkError) Unwrap() error { return e.Err }

// VerifyError reports a download that does not match its checksum.
type VerifyError struct{ Msg string }

func (e *VerifyError) Error() string { return e.Msg }

// Client talks to the releases of one repository.
type Client struct {
	BaseURL string // https://github.com/owner/repo
	HTTP    *http.Client
}

// New returns a client for baseURL (DefaultBaseURL when empty).
func New(baseURL string) Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 2 * time.Minute}}
}

func (c Client) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, &NetworkError{url, err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &NetworkError{url, fmt.Errorf("HTTP %d", resp.StatusCode)}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, &NetworkError{url, err}
	}
	if len(data) > maxDownload {
		return nil, &NetworkError{url, errors.New("the file is larger than expected")}
	}
	return data, nil
}

var tagInFeed = regexp.MustCompile(`/releases/tag/([^"<]+)"`)

// Tags returns the release tags in the releases feed, newest first.
func (c Client) Tags(ctx context.Context) ([]string, error) {
	feed, err := c.get(ctx, c.BaseURL+"/releases.atom")
	if err != nil {
		return nil, err
	}
	var tags []string
	seen := map[string]bool{}
	for _, m := range tagInFeed.FindAllStringSubmatch(string(feed), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			tags = append(tags, m[1])
		}
	}
	return tags, nil
}

// Newest picks the highest version among tags (v1.2.3 or 1.2.3). Pre-releases
// are only considered when includePre is set. It returns "" when nothing fits.
func Newest(tags []string, includePre bool) string {
	best, bestTag := semver.Version{}, ""
	for _, t := range tags {
		v, err := semver.Parse(strings.TrimPrefix(t, "v"))
		if err != nil || (v.IsPrerelease() && !includePre) {
			continue
		}
		if bestTag == "" || semver.Compare(v, best) > 0 {
			best, bestTag = v, t
		}
	}
	return bestTag
}

// AssetName is the name of the archive for a system.
func AssetName(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "freebsd/amd64":
		return fmt.Sprintf("kiln_%s_%s.tar.gz", goos, goarch), nil
	case "windows/amd64", "windows/arm64":
		return fmt.Sprintf("kiln_%s_%s.zip", goos, goarch), nil
	}
	return "", fmt.Errorf("there is no prebuilt kiln for %s/%s", goos, goarch)
}

// ExeName is the name of the program inside the archive.
func ExeName(goos string) string {
	if goos == "windows" {
		return "kiln.exe"
	}
	return "kiln"
}

// Fetch downloads the archive of the release tag for a system, checks it against
// the SHA256SUMS file of the release and returns the kiln program in it.
func (c Client) Fetch(ctx context.Context, tag, goos, goarch string) ([]byte, error) {
	asset, err := AssetName(goos, goarch)
	if err != nil {
		return nil, err
	}
	base := c.BaseURL + "/releases/download/" + tag + "/"
	archive, err := c.get(ctx, base+asset)
	if err != nil {
		return nil, fmt.Errorf("%w (does release %s have a binary for %s/%s?)", err, tag, goos, goarch)
	}
	sums, err := c.get(ctx, base+"SHA256SUMS")
	if err != nil {
		return nil, err
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			want = strings.ToLower(f[0])
		}
	}
	if want == "" {
		return nil, &VerifyError{"release " + tag + " lists no checksum for " + asset}
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != want {
		return nil, &VerifyError{fmt.Sprintf("checksum mismatch for %s: the release says %s, the download is %s; nothing was changed", asset, want, got)}
	}
	return extract(archive, ExeName(goos))
}

// extract returns the file called name from a .tar.gz or .zip archive.
func extract(archive []byte, name string) ([]byte, error) {
	if len(archive) > 2 && archive[0] == 'P' && archive[1] == 'K' {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if f.Name == name && f.Mode().IsRegular() {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return readLimited(rc)
			}
		}
		return nil, fmt.Errorf("the archive has no %s", name)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("the archive has no %s", name)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && h.Name == name {
			return readLimited(tr)
		}
	}
}

func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxDownload+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDownload {
		return nil, errors.New("the program in the archive is larger than expected")
	}
	return data, nil
}

// Replace puts program in place of the executable at exe. The new file is
// written next to it and renamed over it, so the executable is never half
// written. A running executable cannot be overwritten on Windows but can be
// renamed, so there the old file is moved aside to exe+".old" (removed by
// CleanUp the next time).
func Replace(exe string, program []byte) error {
	dir := filepath.Dir(exe)
	tmp := filepath.Join(dir, ".kiln.new")
	if err := os.WriteFile(tmp, program, 0o755); err != nil {
		return fmt.Errorf("cannot write to %s: %w (run the command with the permissions needed to write there)", dir, err)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, exe); err != nil {
			os.Rename(old, exe) // put the old one back
			os.Remove(tmp)
			return err
		}
		return nil
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// CleanUp removes the file an earlier update left behind on Windows.
func CleanUp(exe string) { os.Remove(exe + ".old") }
