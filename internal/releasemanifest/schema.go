package releasemanifest

import (
	"errors"
	"fmt"
	"math/big"
	"mime"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	Schema           = "vibermate.release/v1"
	MaxDocumentBytes = 1 << 20
	MaxArtifactBytes = 1 << 40

	ArtifactRoleAppTreeLedger        = "app-tree-ledger"
	ArtifactRoleDMG                  = "dmg"
	ArtifactRoleDesktopBuildManifest = "desktop-build-manifest"
	ArtifactRoleSBOM                 = "sbom"
	ArtifactRoleKnownIssues          = "known-issues"
)

var (
	ErrInvalidManifest = errors.New("invalid release manifest")

	semverPattern     = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-((?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)
	commitPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	revisionPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:+/-]{0,127}$`)
	decimalPattern    = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
	mediaTypePattern  = regexp.MustCompile(`^[a-z0-9!#$&^_.+-]+/[a-z0-9!#$&^_.+-]+$`)
	sha256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	rfc3339Pattern    = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,9})?(?:Z|[+-][0-9]{2}:[0-9]{2})$`)
)

type Channel string

const (
	ChannelStable  Channel = "stable"
	ChannelPreview Channel = "preview"
	ChannelNightly Channel = "nightly"
)

type SupportLevel string

const (
	SupportStable      SupportLevel = "stable"
	SupportPreview     SupportLevel = "preview"
	SupportCommunity   SupportLevel = "community"
	SupportUnsupported SupportLevel = "unsupported"
)

type CapabilitySupport string

const (
	CapabilitySupported   CapabilitySupport = "supported"
	CapabilityDegraded    CapabilitySupport = "degraded"
	CapabilityUnsupported CapabilitySupport = "unsupported"
	CapabilityUnknown     CapabilitySupport = "unknown"
)

type EvidenceStatus string

const (
	EvidenceCurrent EvidenceStatus = "current"
	EvidenceStale   EvidenceStatus = "stale"
	EvidenceMissing EvidenceStatus = "missing"
)

type Manifest struct {
	Schema          string                 `json:"schema"`
	Version         string                 `json:"version"`
	Channel         Channel                `json:"channel"`
	Commit          string                 `json:"commit"`
	ProtocolSchema  string                 `json:"protocolSchema"`
	PluginSDK       string                 `json:"pluginSDK"`
	PlatformSupport []PlatformSupportEntry `json:"platformSupport"`
	Artifacts       []Artifact             `json:"artifacts"`
	SBOM            ArtifactReference      `json:"sbom"`
	EvidenceScope   EvidenceScope          `json:"evidenceScope"`
	Migration       MigrationCompatibility `json:"migration"`
	HostSupport     []HostSupportEntry     `json:"hostSupport"`
	KnownIssues     ArtifactReference      `json:"knownIssues"`
	PublishedAt     string                 `json:"publishedAt"`
}

type PlatformSupportEntry struct {
	OS                  string             `json:"os"`
	Range               string             `json:"range"`
	Architectures       []string           `json:"architectures"`
	InstallShape        string             `json:"installShape"`
	SupportLevel        SupportLevel       `json:"supportLevel"`
	ConformanceRevision string             `json:"conformanceRevision"`
	HostCapabilities    []CapabilityStatus `json:"hostCapabilities"`
}

type CapabilityStatus struct {
	Capability           string            `json:"capability"`
	Status               CapabilitySupport `json:"status"`
	EvidenceStatus       EvidenceStatus    `json:"evidenceStatus"`
	EvidenceArtifactRole string            `json:"evidenceArtifactRole"`
}

type Artifact struct {
	Path      string `json:"path"`
	MediaType string `json:"mediaType"`
	Role      string `json:"role"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

type ArtifactReference struct {
	Role string `json:"role"`
}

type EvidenceScope struct {
	Level                  string `json:"level"`
	ArtifactState          string `json:"artifactState"`
	R2Reproducibility      string `json:"r2Reproducibility"`
	R3SignedPackageBinding string `json:"r3SignedPackageBinding"`
	ReleaseApproval        string `json:"releaseApproval"`
}

type MigrationCompatibility struct {
	MinimumSchema         string `json:"minimumSchema"`
	MaximumSchema         string `json:"maximumSchema"`
	RollbackCompatibility string `json:"rollbackCompatibility"`
}

type HostSupportEntry struct {
	Host                string             `json:"host"`
	SupportLevel        SupportLevel       `json:"supportLevel"`
	ConformanceRevision string             `json:"conformanceRevision"`
	Capabilities        []CapabilityStatus `json:"capabilities"`
}

func (manifest Manifest) Validate() error {
	if manifest.Schema != Schema {
		return invalid("schema must be %q", Schema)
	}
	if !semverPattern.MatchString(manifest.Version) {
		return invalid("version must be canonical Semantic Versioning 2.0.0")
	}
	if !validChannel(manifest.Channel) {
		return invalid("channel is unsupported")
	}
	if err := ValidateCommit(manifest.Commit); err != nil {
		return err
	}
	if err := validateRevision("protocolSchema", manifest.ProtocolSchema); err != nil {
		return err
	}
	if err := validateRevision("pluginSDK", manifest.PluginSDK); err != nil {
		return err
	}
	if err := validatePlatformSupport(manifest.PlatformSupport); err != nil {
		return err
	}
	artifactsByRole, err := validateArtifacts(manifest.Artifacts)
	if err != nil {
		return err
	}
	if err := validateArtifactReference(
		"sbom",
		manifest.SBOM,
		ArtifactRoleSBOM,
		artifactsByRole,
	); err != nil {
		return err
	}
	if err := validateArtifactReference(
		"knownIssues",
		manifest.KnownIssues,
		ArtifactRoleKnownIssues,
		artifactsByRole,
	); err != nil {
		return err
	}
	if err := validateEvidenceScope(manifest.EvidenceScope); err != nil {
		return err
	}
	if manifest.Channel == ChannelStable {
		return invalid("r0 unsigned evidence cannot claim the stable release channel")
	}
	if err := validateMigration(manifest.Migration); err != nil {
		return err
	}
	if err := validateHostSupport(manifest.HostSupport); err != nil {
		return err
	}
	if err := validateCapabilityEvidence(manifest, artifactsByRole); err != nil {
		return err
	}
	if !rfc3339Pattern.MatchString(manifest.PublishedAt) {
		return invalid("publishedAt must be an RFC 3339 timestamp")
	}
	if _, err := time.Parse(time.RFC3339Nano, manifest.PublishedAt); err != nil {
		return invalid("publishedAt must be an RFC 3339 timestamp: %v", err)
	}
	return nil
}

func validateEvidenceScope(scope EvidenceScope) error {
	if scope.Level != "r0" ||
		scope.ArtifactState != "unsigned-pre-sign" ||
		scope.R2Reproducibility != "not-asserted" ||
		scope.R3SignedPackageBinding != "not-asserted" ||
		scope.ReleaseApproval != "not-asserted" {
		return invalid(
			"evidenceScope must declare r0, unsigned-pre-sign, and no R2, R3, or release-approval assertion",
		)
	}
	return nil
}

func ValidateCommit(value string) error {
	if !commitPattern.MatchString(value) {
		return invalid("commit must be a lowercase 40-character Git object ID")
	}
	return nil
}

func validatePlatformSupport(entries []PlatformSupportEntry) error {
	if len(entries) == 0 || len(entries) > 128 {
		return invalid("platformSupport must contain between 1 and 128 entries")
	}
	seen := make(map[string]struct{}, len(entries))
	for index, entry := range entries {
		prefix := fmt.Sprintf("platformSupport[%d]", index)
		if !identifierPattern.MatchString(entry.OS) {
			return invalid("%s.os is not a canonical identifier", prefix)
		}
		if err := validatePlainText(prefix+".range", entry.Range, 256); err != nil {
			return err
		}
		if len(entry.Architectures) == 0 || len(entry.Architectures) > 16 {
			return invalid("%s.architectures must contain between 1 and 16 entries", prefix)
		}
		architectureSet := make(map[string]struct{}, len(entry.Architectures))
		for _, architecture := range entry.Architectures {
			if !identifierPattern.MatchString(architecture) {
				return invalid("%s.architectures contains a non-canonical identifier", prefix)
			}
			if _, duplicate := architectureSet[architecture]; duplicate {
				return invalid("%s.architectures contains %q more than once", prefix, architecture)
			}
			architectureSet[architecture] = struct{}{}
		}
		canonicalArchitectures := append([]string(nil), entry.Architectures...)
		sort.Strings(canonicalArchitectures)
		if !identifierPattern.MatchString(entry.InstallShape) {
			return invalid("%s.installShape is not a canonical identifier", prefix)
		}
		if !validSupportLevel(entry.SupportLevel) {
			return invalid("%s.supportLevel is unsupported", prefix)
		}
		if err := validateRevision(prefix+".conformanceRevision", entry.ConformanceRevision); err != nil {
			return err
		}
		if err := validateCapabilities(prefix+".hostCapabilities", entry.HostCapabilities); err != nil {
			return err
		}
		key := strings.Join([]string{
			entry.OS,
			entry.Range,
			strings.Join(canonicalArchitectures, ","),
			entry.InstallShape,
		}, "\x00")
		if _, duplicate := seen[key]; duplicate {
			return invalid("platformSupport contains a duplicate platform entry")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateCapabilities(label string, capabilities []CapabilityStatus) error {
	if len(capabilities) == 0 || len(capabilities) > 128 {
		return invalid("%s must contain between 1 and 128 entries", label)
	}
	seen := make(map[string]struct{}, len(capabilities))
	for index, capability := range capabilities {
		prefix := fmt.Sprintf("%s[%d]", label, index)
		if !identifierPattern.MatchString(capability.Capability) {
			return invalid("%s.capability is not a canonical identifier", prefix)
		}
		if !validCapabilitySupport(capability.Status) {
			return invalid("%s.status is unsupported", prefix)
		}
		if !validEvidenceStatus(capability.EvidenceStatus) {
			return invalid("%s.evidenceStatus is unsupported", prefix)
		}
		if (capability.EvidenceStatus == EvidenceMissing ||
			capability.EvidenceStatus == EvidenceStale) &&
			capability.Status != CapabilityUnknown &&
			capability.Status != CapabilityUnsupported {
			return invalid(
				"%s.status must be unknown or unsupported when evidence is missing or stale",
				prefix,
			)
		}
		switch capability.EvidenceStatus {
		case EvidenceMissing:
			if capability.EvidenceArtifactRole != "none" {
				return invalid("%s.evidenceArtifactRole must be none when evidence is missing", prefix)
			}
		case EvidenceCurrent, EvidenceStale:
			if !identifierPattern.MatchString(capability.EvidenceArtifactRole) ||
				capability.EvidenceArtifactRole == "none" {
				return invalid("%s.evidenceArtifactRole must identify a declared evidence artifact", prefix)
			}
		}
		if _, duplicate := seen[capability.Capability]; duplicate {
			return invalid("%s contains capability %q more than once", label, capability.Capability)
		}
		seen[capability.Capability] = struct{}{}
	}
	return nil
}

func validateArtifacts(artifacts []Artifact) (map[string]Artifact, error) {
	if len(artifacts) == 0 || len(artifacts) > 1024 {
		return nil, invalid("artifacts must contain between 1 and 1024 entries")
	}
	roles := make(map[string]Artifact, len(artifacts))
	paths := make(map[string]struct{}, len(artifacts))
	for index, artifact := range artifacts {
		prefix := fmt.Sprintf("artifacts[%d]", index)
		if err := ValidateArtifactPath(artifact.Path); err != nil {
			return nil, invalid("%s.path: %v", prefix, err)
		}
		pathIdentity := strings.ToLower(artifact.Path)
		if _, duplicate := paths[pathIdentity]; duplicate {
			return nil, invalid("artifacts contains path %q more than once or as a case alias", artifact.Path)
		}
		paths[pathIdentity] = struct{}{}
		if !identifierPattern.MatchString(artifact.Role) {
			return nil, invalid("%s.role is not a canonical identifier", prefix)
		}
		if _, duplicate := roles[artifact.Role]; duplicate {
			return nil, invalid("artifacts contains role %q more than once", artifact.Role)
		}
		roles[artifact.Role] = artifact
		if !mediaTypePattern.MatchString(artifact.MediaType) {
			return nil, invalid("%s.mediaType must be a lowercase media type without parameters", prefix)
		}
		parsed, parameters, err := mime.ParseMediaType(artifact.MediaType)
		if err != nil || parsed != artifact.MediaType || len(parameters) != 0 {
			return nil, invalid("%s.mediaType is invalid", prefix)
		}
		if artifact.Size <= 0 || artifact.Size > MaxArtifactBytes {
			return nil, invalid("%s.size must be between 1 and %d bytes", prefix, MaxArtifactBytes)
		}
		if !sha256Pattern.MatchString(artifact.SHA256) {
			return nil, invalid("%s.sha256 must be a lowercase SHA-256 digest", prefix)
		}
	}
	required := []struct {
		role      string
		mediaType string
	}{
		{ArtifactRoleAppTreeLedger, "application/json"},
		{ArtifactRoleDesktopBuildManifest, "application/json"},
		{ArtifactRoleSBOM, "application/spdx+json"},
		{ArtifactRoleKnownIssues, "application/json"},
	}
	if _, exists := roles[ArtifactRoleDMG]; exists {
		return nil, invalid(
			"r0 unsigned evidence cannot contain the signed-package role %q",
			ArtifactRoleDMG,
		)
	}
	for _, requirement := range required {
		artifact, exists := roles[requirement.role]
		if !exists {
			return nil, invalid("artifacts must contain role %q", requirement.role)
		}
		if artifact.MediaType != requirement.mediaType {
			return nil, invalid(
				"artifact role %q must use mediaType %q",
				requirement.role,
				requirement.mediaType,
			)
		}
	}
	return roles, nil
}

func ValidateArtifactPath(value string) error {
	if value == "" || !utf8.ValidString(value) || len(value) > 1024 {
		return errors.New("path is empty, invalid UTF-8, or exceeds the byte limit")
	}
	if strings.ContainsAny(value, "\\:\x00") || path.IsAbs(value) || path.Clean(value) != value {
		return errors.New("path must be a clean relative slash-separated path")
	}
	for _, character := range value {
		asciiLetter := character >= 'A' && character <= 'Z' ||
			character >= 'a' && character <= 'z'
		asciiDigit := character >= '0' && character <= '9'
		if !asciiLetter && !asciiDigit &&
			character != '/' && character != '.' &&
			character != '_' && character != '-' {
			return errors.New("path contains a character outside the portable ASCII profile")
		}
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return errors.New("path contains an empty or traversal segment")
		}
	}
	return nil
}

func validateArtifactReference(
	label string,
	reference ArtifactReference,
	expectedRole string,
	artifactsByRole map[string]Artifact,
) error {
	if reference.Role != expectedRole {
		return invalid("%s.role must be %q", label, expectedRole)
	}
	if _, exists := artifactsByRole[reference.Role]; !exists {
		return invalid("%s.role does not identify a declared artifact", label)
	}
	return nil
}

func validateMigration(migration MigrationCompatibility) error {
	if err := validateRevision("migration.minimumSchema", migration.MinimumSchema); err != nil {
		return err
	}
	if err := validateRevision("migration.maximumSchema", migration.MaximumSchema); err != nil {
		return err
	}
	if !identifierPattern.MatchString(migration.RollbackCompatibility) {
		return invalid("migration.rollbackCompatibility is not a canonical identifier")
	}
	if decimalPattern.MatchString(migration.MinimumSchema) && decimalPattern.MatchString(migration.MaximumSchema) {
		minimum, _ := new(big.Int).SetString(migration.MinimumSchema, 10)
		maximum, _ := new(big.Int).SetString(migration.MaximumSchema, 10)
		if minimum.Cmp(maximum) > 0 {
			return invalid("migration.minimumSchema cannot exceed migration.maximumSchema")
		}
	}
	return nil
}

func validateHostSupport(entries []HostSupportEntry) error {
	if len(entries) != 2 {
		return invalid("hostSupport must explicitly declare desktop and server")
	}
	seen := make(map[string]struct{}, len(entries))
	for index, entry := range entries {
		prefix := fmt.Sprintf("hostSupport[%d]", index)
		if entry.Host != "desktop" && entry.Host != "server" {
			return invalid("%s.host must be desktop or server", prefix)
		}
		if _, duplicate := seen[entry.Host]; duplicate {
			return invalid("hostSupport contains host %q more than once", entry.Host)
		}
		seen[entry.Host] = struct{}{}
		if !validSupportLevel(entry.SupportLevel) {
			return invalid("%s.supportLevel is unsupported", prefix)
		}
		if err := validateRevision(prefix+".conformanceRevision", entry.ConformanceRevision); err != nil {
			return err
		}
		if err := validateCapabilities(prefix+".capabilities", entry.Capabilities); err != nil {
			return err
		}
	}
	if _, exists := seen["desktop"]; !exists {
		return invalid("hostSupport must explicitly declare desktop")
	}
	if _, exists := seen["server"]; !exists {
		return invalid("hostSupport must explicitly declare server")
	}
	return nil
}

func validateRevision(label, value string) error {
	if !revisionPattern.MatchString(value) {
		return invalid("%s is not a canonical revision", label)
	}
	return nil
}

func validatePlainText(label, value string, maximum int) error {
	if value == "" || !utf8.ValidString(value) || len(value) > maximum || strings.TrimSpace(value) != value {
		return invalid("%s is empty, invalid UTF-8, padded, or exceeds the byte limit", label)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return invalid("%s contains a control character", label)
		}
	}
	return nil
}

func validChannel(channel Channel) bool {
	return channel == ChannelStable || channel == ChannelPreview || channel == ChannelNightly
}

func validSupportLevel(level SupportLevel) bool {
	return level == SupportPreview || level == SupportUnsupported
}

func validCapabilitySupport(status CapabilitySupport) bool {
	switch status {
	case CapabilitySupported, CapabilityDegraded, CapabilityUnsupported, CapabilityUnknown:
		return true
	default:
		return false
	}
}

func validEvidenceStatus(status EvidenceStatus) bool {
	return status == EvidenceCurrent || status == EvidenceStale || status == EvidenceMissing
}

func invalid(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidManifest, fmt.Sprintf(format, arguments...))
}

func validateCapabilityEvidence(
	manifest Manifest,
	artifactsByRole map[string]Artifact,
) error {
	type evidenceBinding struct {
		capability          string
		status              CapabilitySupport
		evidenceStatus      EvidenceStatus
		conformanceRevision string
	}
	bindings := make(map[string]evidenceBinding)
	validate := func(
		label, conformanceRevision string,
		capabilities []CapabilityStatus,
	) error {
		for index, capability := range capabilities {
			if capability.EvidenceStatus == EvidenceMissing {
				continue
			}
			artifact, exists := artifactsByRole[capability.EvidenceArtifactRole]
			if !exists {
				return invalid(
					"%s[%d].evidenceArtifactRole does not identify a declared artifact",
					label,
					index,
				)
			}
			if isCoreArtifactRole(artifact.Role) || artifact.MediaType != "application/json" {
				return invalid(
					"%s[%d].evidenceArtifactRole must identify a dedicated application/json evidence artifact",
					label,
					index,
				)
			}
			binding := evidenceBinding{
				capability:          capability.Capability,
				status:              capability.Status,
				evidenceStatus:      capability.EvidenceStatus,
				conformanceRevision: conformanceRevision,
			}
			if previous, shared := bindings[artifact.Role]; shared {
				inconsistent := previous.capability != binding.capability ||
					previous.evidenceStatus != binding.evidenceStatus
				if binding.evidenceStatus == EvidenceCurrent {
					inconsistent = inconsistent ||
						previous.status != binding.status ||
						previous.conformanceRevision != binding.conformanceRevision
				}
				if inconsistent {
					return invalid(
						"%s[%d].evidenceArtifactRole shares one evidence document across incompatible declarations",
						label,
						index,
					)
				}
			} else {
				bindings[artifact.Role] = binding
			}
		}
		return nil
	}
	for index, platform := range manifest.PlatformSupport {
		if err := validate(
			fmt.Sprintf("platformSupport[%d].hostCapabilities", index),
			platform.ConformanceRevision,
			platform.HostCapabilities,
		); err != nil {
			return err
		}
	}
	for index, host := range manifest.HostSupport {
		if err := validate(
			fmt.Sprintf("hostSupport[%d].capabilities", index),
			host.ConformanceRevision,
			host.Capabilities,
		); err != nil {
			return err
		}
	}
	return nil
}

func isCoreArtifactRole(role string) bool {
	switch role {
	case ArtifactRoleAppTreeLedger,
		ArtifactRoleDMG,
		ArtifactRoleDesktopBuildManifest,
		ArtifactRoleSBOM,
		ArtifactRoleKnownIssues:
		return true
	default:
		return false
	}
}
