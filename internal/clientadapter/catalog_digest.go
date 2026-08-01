package clientadapter

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
)

// CatalogDigest is the content of a catalog revision, in one value.
//
// Design 06 §4.2 says a catalog update must not silently widen what may be
// decrypted. The revision is what a CaptureRun records as evidence of which
// catalog was consulted, so a catalog whose content changed while its revision
// did not makes every record that cites it wrong. This is what makes that
// impossible to do by accident: the digest is pinned against the revision, and
// changing one without the other fails.
func (catalog Catalog) Digest() (string, error) {
	canonical := canonicalCatalog{
		Revision: uint64(catalog.revision),
		Releases: make([]canonicalRelease, 0, len(catalog.releases)),
	}
	for _, release := range catalog.releases {
		entry := canonicalRelease{
			ID:                      release.ID,
			Revision:                uint64(release.Revision),
			Version:                 release.Version,
			OperatingSystem:         release.OperatingSystem,
			Architecture:            release.Architecture,
			InstallShape:            string(release.InstallShape),
			InvocationLabel:         release.InvocationLabel,
			CanonicalEntrypointName: release.CanonicalEntrypointName,
			ArtifactRoot:            release.ArtifactRoot,
			LaunchRecipe:            string(release.LaunchRecipe),
			Features:                uint64(release.Features),
			Artifacts: make(
				[]canonicalArtifact,
				0,
				len(release.Artifacts),
			),
		}
		for _, artifact := range release.Artifacts {
			entry.Artifacts = append(entry.Artifacts, canonicalArtifact{
				Role:         string(artifact.Role),
				RelativePath: artifact.RelativePath,
				SHA256:       artifact.SHA256,
				MaxBytes:     artifact.MaxBytes,
			})
		}
		// The order a catalog happens to list its artifacts in is not part of
		// what it says, so it is normalized before it is hashed.
		slices.SortFunc(entry.Artifacts, func(left, right canonicalArtifact) int {
			if left.Role != right.Role {
				if left.Role < right.Role {
					return -1
				}
				return 1
			}
			if left.RelativePath == right.RelativePath {
				return 0
			}
			if left.RelativePath < right.RelativePath {
				return -1
			}
			return 1
		})
		canonical.Releases = append(canonical.Releases, entry)
	}
	slices.SortFunc(canonical.Releases, func(left, right canonicalRelease) int {
		if left.ID == right.ID {
			if left.Revision == right.Revision {
				return 0
			}
			if left.Revision < right.Revision {
				return -1
			}
			return 1
		}
		if left.ID < right.ID {
			return -1
		}
		return 1
	})
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("canonicalize client catalog: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

type canonicalCatalog struct {
	Revision uint64             `json:"revision"`
	Releases []canonicalRelease `json:"releases"`
}

type canonicalRelease struct {
	ID                      string              `json:"id"`
	Revision                uint64              `json:"revision"`
	Version                 string              `json:"version"`
	OperatingSystem         string              `json:"operatingSystem"`
	Architecture            string              `json:"architecture"`
	InstallShape            string              `json:"installShape"`
	InvocationLabel         string              `json:"invocationLabel"`
	CanonicalEntrypointName string              `json:"canonicalEntrypointName"`
	ArtifactRoot            string              `json:"artifactRoot"`
	LaunchRecipe            string              `json:"launchRecipe"`
	Features                uint64              `json:"features"`
	Artifacts               []canonicalArtifact `json:"artifacts"`
}

type canonicalArtifact struct {
	Role         string `json:"role"`
	RelativePath string `json:"relativePath"`
	SHA256       string `json:"sha256"`
	MaxBytes     int64  `json:"maxBytes"`
}
