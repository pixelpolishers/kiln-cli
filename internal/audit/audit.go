// Package audit checks the packages in kiln.lock against a vulnerability database.
package audit

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/pixelpolishers/kiln-cli/internal/lock"
	"github.com/pixelpolishers/kiln-cli/internal/osv"
	"github.com/pixelpolishers/kiln-cli/internal/project"
	"github.com/pixelpolishers/kiln-cli/internal/registrysrc"
	"github.com/pixelpolishers/kiln-cli/internal/source"
	"github.com/pixelpolishers/kiln-core/manifest"
	"github.com/pixelpolishers/kiln-core/semver"
)

// Package is the outcome for one locked package.
type Package struct {
	Name, Version string
	// Query says how it was looked up: "commit abc..." or "tag v1.0 of https://...".
	Query string
	Vulns []osv.Vuln
	// Ignored counts advisories left out because the user accepted them.
	Ignored int
	// NotChecked is set when no query could be made, with the reason.
	NotChecked string
}

// Result is the outcome of an audit.
type Result struct {
	Packages []Package
}

// Count is the number of advisories across all packages.
func (r Result) Count() int {
	n := 0
	for _, p := range r.Packages {
		n += len(p.Vulns)
	}
	return n
}

// Options controls Run.
type Options struct {
	Dir string
	// Ignore lists advisory ids (or aliases) the user accepts.
	Ignore []string
}

// Run audits the lock in opts.Dir. The names and versions of the locked
// packages, with their repositories, are sent to the OSV service.
func Run(ctx context.Context, src *registrysrc.Source, db *osv.Client, v *manifest.Validator, opts Options) (Result, error) {
	lk, err := lock.Read(filepath.Join(opts.Dir, project.LockFile), v)
	if err != nil {
		return Result{}, err
	}
	if lk == nil {
		return Result{}, ErrNoLock
	}

	type job struct {
		pkg     Package
		queries []osv.Query
	}
	var jobs []*job
	for _, lp := range lk.Packages {
		j := &job{pkg: Package{Name: lp.Name, Version: lp.Version}}
		jobs = append(jobs, j)
		ver, err := semver.Parse(lp.Version)
		if err != nil {
			j.pkg.NotChecked = err.Error()
			continue
		}
		m, _, err := src.Manifest(ctx, lp.Name, ver)
		if err != nil {
			return Result{}, fmt.Errorf("%s %s: %w", lp.Name, lp.Version, err)
		}
		repo, err := source.Repository(m)
		if err != nil {
			j.pkg.NotChecked = "the package declares no repository"
			continue
		}
		repo = strings.TrimSuffix(strings.TrimSuffix(repo, "/"), ".git")
		if lp.Commit != "" {
			j.queries = []osv.Query{{Commit: lp.Commit}}
			j.pkg.Query = "commit " + lp.Commit[:12]
			continue
		}
		for _, tag := range source.Tags(source.TagTemplate(m), lp.Version) {
			j.queries = append(j.queries, osv.Query{Repository: repo, Tag: tag})
		}
		if len(j.queries) == 0 {
			j.pkg.NotChecked = "no tag to look up"
			continue
		}
		j.pkg.Query = fmt.Sprintf("tag %s of %s", j.queries[0].Tag, repo)
	}

	var all []osv.Query
	for _, j := range jobs {
		all = append(all, j.queries...)
	}
	idsPer, err := db.IDs(ctx, all)
	if err != nil {
		return Result{}, err
	}
	next := 0
	wanted := map[string]bool{}
	ids := make([][]string, len(jobs))
	for i, j := range jobs {
		seen := map[string]bool{}
		for range j.queries {
			for _, id := range idsPer[next] {
				if !seen[id] {
					seen[id] = true
					ids[i] = append(ids[i], id)
					wanted[id] = true
				}
			}
			next++
		}
	}

	details, err := fetchAll(ctx, db, wanted)
	if err != nil {
		return Result{}, err
	}
	ignore := map[string]bool{}
	for _, id := range opts.Ignore {
		ignore[strings.ToUpper(id)] = true
	}
	var res Result
	for i, j := range jobs {
		var vulns []osv.Vuln
		for _, id := range ids[i] {
			d := details[id]
			if d.Withdrawn != "" {
				continue
			}
			vulns = append(vulns, d)
		}
		vulns = dedupe(vulns)
		var kept []osv.Vuln
		for _, d := range vulns {
			if isIgnored(d, ignore) {
				j.pkg.Ignored++
				continue
			}
			kept = append(kept, d)
		}
		j.pkg.Vulns = kept
		res.Packages = append(res.Packages, j.pkg)
	}
	return res, nil
}

// ErrNoLock means there is nothing to audit.
var ErrNoLock = fmt.Errorf("there is no %s: run kiln install first, so the audit checks exactly the versions you use", project.LockFile)

func isIgnored(v osv.Vuln, ignore map[string]bool) bool {
	if ignore[strings.ToUpper(v.ID)] {
		return true
	}
	for _, a := range v.Aliases {
		if ignore[strings.ToUpper(a)] {
			return true
		}
	}
	return false
}

// fetchAll loads the advisories, a few at a time.
func fetchAll(ctx context.Context, db *osv.Client, ids map[string]bool) (map[string]osv.Vuln, error) {
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	sort.Strings(list)
	out := make(map[string]osv.Vuln, len(list))
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
	)
	sem := make(chan struct{}, 8)
	for _, id := range list {
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			v, err := db.Vuln(ctx, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			out[id] = v
		}(id)
	}
	wg.Wait()
	return out, firstErr
}

// dedupe merges advisories that are the same vulnerability under different
// ids (a CVE and its GitHub advisory), preferring the CVE id, and sorts them.
func dedupe(vulns []osv.Vuln) []osv.Vuln {
	key := func(v osv.Vuln) string {
		if strings.HasPrefix(v.ID, "CVE-") {
			return v.ID
		}
		for _, a := range v.Aliases {
			if strings.HasPrefix(a, "CVE-") {
				return a
			}
		}
		return v.ID
	}
	seen := map[string]int{}
	var out []osv.Vuln
	for _, v := range vulns {
		k := key(v)
		if i, ok := seen[k]; ok {
			if strings.HasPrefix(v.ID, "CVE-") && !strings.HasPrefix(out[i].ID, "CVE-") {
				out[i] = v
			}
			continue
		}
		seen[k] = len(out)
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
