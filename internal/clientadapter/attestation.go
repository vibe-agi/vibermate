package clientadapter

import (
	"errors"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrInvalidCompanionAttestation = errors.New(
	"client companion attestation is invalid",
)

// ValidateCompanionAttestation validates a detection produced by the same
// release verifier on a remote ViberMate companion. It proves that the claim
// is internally consistent with this exact catalog; authentication and the
// Server's admission policy separately decide whether that companion may make
// the claim. It never turns a generic detection into verified evidence.
func (catalog Catalog) ValidateCompanionAttestation(
	detection Detection,
) (Detection, error) {
	normalized, err := NewCatalogWithSigners(
		catalog.revision,
		catalog.releases,
		catalog.signers,
	)
	if err != nil || detection.CatalogRevision != normalized.revision ||
		!validAttestedPath(detection.CanonicalPath) ||
		!validInvocationLabel(detection.ExecutableLabel) {
		return Detection{}, ErrInvalidCompanionAttestation
	}
	knownLabel := normalized.knowsInvocationLabel(detection.ExecutableLabel)
	switch detection.Status {
	case StatusVerified:
		if detection.Recognition != RecognitionVerified ||
			detection.Evidence == nil || detection.Signer != nil {
			return Detection{}, ErrInvalidCompanionAttestation
		}
		expected, release, found := normalized.expectedRelease(
			detection.Evidence.ID,
			detection.Evidence.Version,
		)
		if !found || expected != *detection.Evidence ||
			release.InvocationLabel != detection.ExecutableLabel {
			return Detection{}, ErrInvalidCompanionAttestation
		}
	case StatusGeneric:
		if detection.Evidence != nil {
			return Detection{}, ErrInvalidCompanionAttestation
		}
		switch detection.Recognition {
		case RecognitionUnknown:
			if knownLabel || detection.Signer != nil {
				return Detection{}, ErrInvalidCompanionAttestation
			}
		case RecognitionUnverified:
			if !knownLabel || detection.Signer != nil {
				return Detection{}, ErrInvalidCompanionAttestation
			}
		case RecognitionRecognized:
			if detection.Signer == nil ||
				!normalized.matchesSigner(
					detection.ExecutableLabel,
					*detection.Signer,
				) {
				return Detection{}, ErrInvalidCompanionAttestation
			}
		default:
			return Detection{}, ErrInvalidCompanionAttestation
		}
	default:
		return Detection{}, ErrInvalidCompanionAttestation
	}
	return detection.clone(), nil
}

func (catalog Catalog) expectedRelease(
	id string,
	version string,
) (Evidence, Release, bool) {
	for _, release := range catalog.releases {
		if release.ID != id || release.Version != version {
			continue
		}
		return Evidence{
			ID:              release.ID,
			Revision:        release.Revision,
			Version:         release.Version,
			CatalogRevision: catalog.revision,
			InstallShape:    release.InstallShape,
			ReleaseSHA256:   releaseEvidenceDigest(release),
			LaunchRecipe:    release.LaunchRecipe,
			Features:        release.Features,
		}, release, true
	}
	return Evidence{}, Release{}, false
}

func (catalog Catalog) knowsInvocationLabel(label string) bool {
	for _, release := range catalog.releases {
		if release.InvocationLabel == label {
			return true
		}
	}
	for _, signer := range catalog.signers {
		if signer.InvocationLabel == label {
			return true
		}
	}
	return false
}

func (catalog Catalog) matchesSigner(
	label string,
	evidence SignerEvidence,
) bool {
	if evidence.Validate() != nil {
		return false
	}
	for _, signer := range catalog.signers {
		if signer.InvocationLabel == label &&
			signer.ID == evidence.ID &&
			signer.Revision == evidence.Revision &&
			catalog.revision == evidence.CatalogRevision &&
			signer.InstallShape == evidence.InstallShape &&
			signer.LaunchRecipe == evidence.LaunchRecipe {
			return true
		}
	}
	return false
}

func validAttestedPath(value string) bool {
	return value != "" && len(value) <= maxArtifactPathBytes &&
		utf8.ValidString(value) && filepath.IsAbs(value) &&
		filepath.Clean(value) == value && !strings.ContainsRune(value, '\x00')
}

func validInvocationLabel(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value || strings.ContainsAny(value, `/\\`) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
