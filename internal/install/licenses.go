package install

import (
	"context"
	"sort"
)

// LicensePackage is the license of one locked package.
type LicensePackage struct {
	Name, Version string
	// License is the SPDX expression from the package manifest; empty when it has none.
	License string
	// Direct is true when kiln.json itself requires the package.
	Direct bool
}

// LicensesResult lists the licenses of everything in kiln.lock.
type LicensesResult struct {
	Packages []LicensePackage
	Stale    bool
}

// Licenses reads kiln.lock and the manifests of the locked versions and returns their licenses.
func Licenses(ctx context.Context, env Env, dir string) (LicensesResult, error) {
	g, err := env.loadGraph(ctx, dir)
	if err != nil {
		return LicensesResult{}, err
	}
	res := LicensesResult{Stale: g.stale}
	for _, p := range g.lock.Packages {
		_, direct := g.require[p.Name]
		res.Packages = append(res.Packages, LicensePackage{p.Name, p.Version, g.manifest[p.Name].License, direct})
	}
	sort.Slice(res.Packages, func(i, j int) bool { return res.Packages[i].Name < res.Packages[j].Name })
	return res, nil
}
