package install

import (
	"fmt"

	"github.com/pixelpolishers/kiln-cli/internal/archive"
	"github.com/pixelpolishers/kiln-cli/internal/plan"
	"github.com/pixelpolishers/kiln-core/assetid"
	"github.com/pixelpolishers/kiln-core/manifest"
)

// verifyAsset checks a binary before it is trusted. fromLocal says the asset
// comes from a repository the user chose explicitly. It returns a warning to
// show the user, if any.
func verifyAsset(p plan.Package, a manifest.Asset, file string, fromLocal bool) (warning string, err error) {
	// The id must really describe the metadata: otherwise a binary could claim
	// an id it was not built for.
	deps := map[string]assetid.Dep{}
	for name, d := range a.Dependencies {
		deps[name] = assetid.Dep{Version: d.Version, ID: d.ID}
	}
	got, err := assetid.Compute(assetid.Input{
		Package: p.Name, Version: p.Version.String(),
		Settings: assetid.Relevant(a.Settings, p.Kind), Options: a.Options, Deps: deps,
	})
	if err != nil {
		return "", &VerifyError{p.Name, err.Error()}
	}
	if got != a.ID {
		return "", &VerifyError{p.Name, fmt.Sprintf("the asset claims id %s but its own settings, options and dependencies give %s", a.ID, got)}
	}
	if a.ID != p.ID {
		return "", &VerifyError{p.Name, "the asset does not have the id that was asked for"}
	}

	sum, size, err := archive.SHA256(file)
	if err != nil {
		return "", &VerifyError{p.Name, err.Error()}
	}
	if sum != a.File.SHA256 {
		return "", &VerifyError{p.Name, fmt.Sprintf("file hash is %s, the asset manifest says %s", sum, a.File.SHA256)}
	}
	if a.File.Size != 0 && size != a.File.Size {
		return "", &VerifyError{p.Name, fmt.Sprintf("file size is %d, the asset manifest says %d", size, a.File.Size)}
	}

	switch a.Attestation.Type {
	case "unsigned":
		if !fromLocal {
			return "", &VerifyError{p.Name, "unsigned binaries are only accepted from a local repository"}
		}
		return fmt.Sprintf("%s %s is unsigned (no build provenance)", p.Name, p.Version), nil
	default:
		return "", &VerifyError{p.Name, fmt.Sprintf("attestation type %q cannot be verified by this client yet", a.Attestation.Type)}
	}
}
