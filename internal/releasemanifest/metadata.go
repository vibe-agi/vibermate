package releasemanifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	pathpkg "path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DesktopBuildSchemaV2     = "vibermate.desktop-build/v2"
	KnownIssuesSchemaV1      = "vibermate.known-issues/v1"
	AppTreeLedgerSchemaV1    = "vibermate.app-tree-ledger/v1"
	CapabilityEvidenceSchema = "vibermate.capability-evidence/v1"
	UnsignedPayloadRoot      = "unsigned-payload"

	maxDesktopBuildManifestBytes = 128 << 10
	maxKnownIssuesBytes          = 1 << 20
	maxCapabilityEvidenceBytes   = 256 << 10
	maxAppTreeLedgerBytes        = 16 << 20
	maxSPDXDocumentBytes         = 32 << 20
	maxAppTreeEntries            = 65536
	maxAppTreeFileBytes          = 2 << 30
	maxAppTreeTotalBytes         = 16 << 30
)

var (
	spdxElementPattern  = regexp.MustCompile(`^SPDXRef-[A-Za-z0-9.-]+$`)
	utcTimestampPattern = regexp.MustCompile(
		`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$`,
	)
)

var desktopBuildConfigurationNames = []string{
	"go.mod",
	"go.sum",
	"rust-toolchain.toml",
	"ui/desktop/package.json",
	"ui/desktop/pnpm-lock.yaml",
	"ui/desktop/src-tauri/Cargo.toml",
	"ui/desktop/src-tauri/Cargo.lock",
	"ui/desktop/src-tauri/tauri.conf.json",
}

var desktopSidecarNames = []string{"vibermate", "vibermated"}

type desktopBuildDocument struct {
	Schema              string                 `json:"schema"`
	Source              desktopBuildSource     `json:"source"`
	Profiles            desktopBuildProfiles   `json:"profiles"`
	Toolchains          desktopBuildToolchains `json:"toolchains"`
	ConfigurationSHA256 map[string]string      `json:"configurationSHA256"`
	SidecarSHA256       map[string]string      `json:"sidecarSHA256"`
}

type desktopBuildSource struct {
	VCS        string `json:"vcs"`
	Revision   string `json:"revision"`
	CommitTime string `json:"commitTime"`
	Dirty      *bool  `json:"dirty"`
}

type desktopBuildProfiles struct {
	Desktop  string `json:"desktop"`
	Sidecars string `json:"sidecars"`
	Target   string `json:"target"`
}

type desktopBuildToolchains struct {
	Go    string `json:"go"`
	Node  string `json:"node"`
	Rustc string `json:"rustc"`
	Cargo string `json:"cargo"`
	PNPM  string `json:"pnpm"`
	Tauri string `json:"tauri"`
}

type knownIssuesDocument struct {
	Schema  string       `json:"schema"`
	Version string       `json:"version"`
	Commit  string       `json:"commit"`
	Issues  []knownIssue `json:"issues"`
}

type knownIssue struct {
	ID       string `json:"id"`
	Summary  string `json:"summary"`
	Severity string `json:"severity"`
}

type appTreeLedgerDocument struct {
	Schema                     string               `json:"schema"`
	Commit                     string               `json:"commit"`
	Root                       string               `json:"root"`
	DesktopBuildManifestSHA256 string               `json:"desktopBuildManifestSHA256"`
	Entries                    []appTreeLedgerEntry `json:"entries"`
}

type appTreeLedgerEntry struct {
	Mode    *uint32 `json:"mode"`
	Path    string  `json:"path"`
	Type    string  `json:"type"`
	SHA256  *string `json:"sha256,omitempty"`
	Size    *int64  `json:"size,omitempty"`
	members map[string]struct{}
}

func (entry *appTreeLedgerEntry) UnmarshalJSON(payload []byte) error {
	type wireEntry appTreeLedgerEntry
	var decoded wireEntry
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(payload, &members); err != nil || members == nil {
		return errors.New("application tree ledger entry must be an object")
	}
	*entry = appTreeLedgerEntry(decoded)
	entry.members = make(map[string]struct{}, len(members))
	for name := range members {
		entry.members[name] = struct{}{}
	}
	return nil
}

type spdxDocument struct {
	SPDXID            string           `json:"SPDXID"`
	CreationInfo      spdxCreationInfo `json:"creationInfo"`
	DataLicense       string           `json:"dataLicense"`
	Name              string           `json:"name"`
	SPDXVersion       string           `json:"spdxVersion"`
	DocumentNamespace string           `json:"documentNamespace"`
	Comment           string           `json:"comment"`
	Packages          []spdxPackage    `json:"packages"`
}

type spdxCreationInfo struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
}

// spdxPackage is the deliberately small, closed SPDX 2.3 package profile used
// by source-traceability evidence (R0). Every field below is a standard SPDX
// JSON field. Keeping the profile small lets this verifier establish validity
// without pretending that
// partially checking the full SPDX schema proves conformance.
type spdxPackage struct {
	SPDXID           string `json:"SPDXID"`
	Name             string `json:"name"`
	VersionInfo      string `json:"versionInfo"`
	DownloadLocation string `json:"downloadLocation"`
	FilesAnalyzed    *bool  `json:"filesAnalyzed"`
	LicenseConcluded string `json:"licenseConcluded"`
	LicenseDeclared  string `json:"licenseDeclared"`
	CopyrightText    string `json:"copyrightText"`
}

type capabilityEvidenceDocument struct {
	Schema              string            `json:"schema"`
	Commit              string            `json:"commit"`
	ConformanceRevision string            `json:"conformanceRevision"`
	Capability          string            `json:"capability"`
	Status              CapabilitySupport `json:"status"`
	ObservedAt          string            `json:"observedAt"`
}

type capabilityEvidenceExpectation struct {
	capability          string
	status              CapabilitySupport
	evidenceStatus      EvidenceStatus
	conformanceRevision string
}

func metadataByteLimit(artifact Artifact) (int64, bool) {
	switch artifact.Role {
	case ArtifactRoleDesktopBuildManifest:
		return maxDesktopBuildManifestBytes, true
	case ArtifactRoleKnownIssues:
		return maxKnownIssuesBytes, true
	case ArtifactRoleAppTreeLedger:
		return maxAppTreeLedgerBytes, true
	case ArtifactRoleSBOM:
		return maxSPDXDocumentBytes, true
	default:
		if artifact.MediaType == "application/json" {
			return maxCapabilityEvidenceBytes, true
		}
		return 0, false
	}
}

func validateArtifactMetadata(
	payload []byte,
	artifact Artifact,
	manifest Manifest,
	artifactsByRole map[string]Artifact,
	expectations map[string][]capabilityEvidenceExpectation,
) error {
	switch artifact.Role {
	case ArtifactRoleDesktopBuildManifest:
		return validateDesktopBuildDocument(payload, manifest)
	case ArtifactRoleKnownIssues:
		return validateKnownIssuesDocument(payload, manifest)
	case ArtifactRoleAppTreeLedger:
		return validateAppTreeLedgerDocument(payload, manifest, artifactsByRole)
	case ArtifactRoleSBOM:
		return validateSPDXDocument(payload, manifest)
	default:
		if artifact.MediaType != "application/json" {
			return nil
		}
		return validateCapabilityEvidenceDocument(
			payload,
			manifest,
			expectations[artifact.Role],
		)
	}
}

func validateDesktopBuildDocument(payload []byte, manifest Manifest) error {
	var document desktopBuildDocument
	if err := decodeClosedArtifactJSON(payload, &document); err != nil {
		return fmt.Errorf("desktop build manifest: %w", err)
	}
	if document.Schema != DesktopBuildSchemaV2 {
		return fmt.Errorf("desktop build manifest schema must be %q", DesktopBuildSchemaV2)
	}
	if document.Source.VCS != "git" || document.Source.Revision != manifest.Commit {
		return errors.New("desktop build manifest source does not bind the release commit")
	}
	if document.Source.Dirty == nil || *document.Source.Dirty {
		return errors.New("desktop build manifest source must be explicitly clean")
	}
	if err := validateUTCTimestamp("desktop build manifest source.commitTime", document.Source.CommitTime); err != nil {
		return err
	}
	if document.Profiles.Desktop != "release" ||
		document.Profiles.Sidecars != "distribution" ||
		document.Profiles.Target != "universal-apple-darwin" {
		return errors.New("desktop build manifest profiles are not the distribution profile")
	}
	toolchains := map[string]string{
		"go":    document.Toolchains.Go,
		"node":  document.Toolchains.Node,
		"rustc": document.Toolchains.Rustc,
		"cargo": document.Toolchains.Cargo,
		"pnpm":  document.Toolchains.PNPM,
		"tauri": document.Toolchains.Tauri,
	}
	for name, value := range toolchains {
		if err := validateMetadataText("desktop build manifest toolchain "+name, value, 4096, true); err != nil {
			return err
		}
	}
	if err := validateExactDigestMap(
		"desktop build manifest configurationSHA256",
		document.ConfigurationSHA256,
		desktopBuildConfigurationNames,
	); err != nil {
		return err
	}
	return validateExactDigestMap(
		"desktop build manifest sidecarSHA256",
		document.SidecarSHA256,
		desktopSidecarNames,
	)
}

func validateKnownIssuesDocument(payload []byte, manifest Manifest) error {
	var document knownIssuesDocument
	if err := decodeClosedArtifactJSON(payload, &document); err != nil {
		return fmt.Errorf("known issues: %w", err)
	}
	if document.Schema != KnownIssuesSchemaV1 ||
		document.Version != manifest.Version ||
		document.Commit != manifest.Commit {
		return errors.New("known issues do not bind the release version and commit")
	}
	if document.Issues == nil || len(document.Issues) > 1024 {
		return errors.New("known issues must contain an array of at most 1024 entries")
	}
	seen := make(map[string]struct{}, len(document.Issues))
	for index, issue := range document.Issues {
		if !identifierPattern.MatchString(issue.ID) {
			return fmt.Errorf("known issues[%d].id is not a canonical identifier", index)
		}
		if _, duplicate := seen[issue.ID]; duplicate {
			return fmt.Errorf("known issues contains id %q more than once", issue.ID)
		}
		seen[issue.ID] = struct{}{}
		if err := validateMetadataText(
			fmt.Sprintf("known issues[%d].summary", index),
			issue.Summary,
			1024,
			false,
		); err != nil {
			return err
		}
		switch issue.Severity {
		case "low", "medium", "high", "critical":
		default:
			return fmt.Errorf("known issues[%d].severity is unsupported", index)
		}
	}
	return nil
}

func validateAppTreeLedgerDocument(
	payload []byte,
	manifest Manifest,
	artifactsByRole map[string]Artifact,
) error {
	var document appTreeLedgerDocument
	if err := decodeClosedArtifactJSON(payload, &document); err != nil {
		return fmt.Errorf("application tree ledger: %w", err)
	}
	desktopBuild, exists := artifactsByRole[ArtifactRoleDesktopBuildManifest]
	if !exists ||
		document.Schema != AppTreeLedgerSchemaV1 ||
		document.Commit != manifest.Commit ||
		document.Root != UnsignedPayloadRoot ||
		document.DesktopBuildManifestSHA256 != desktopBuild.SHA256 {
		return errors.New("application tree ledger does not bind the fixed unsigned payload root, release commit, and Desktop build manifest")
	}
	if document.Entries == nil || len(document.Entries) == 0 || len(document.Entries) > maxAppTreeEntries {
		return fmt.Errorf(
			"application tree ledger must contain between 1 and %d entries",
			maxAppTreeEntries,
		)
	}
	typesByPath := make(map[string]string, len(document.Entries))
	pathIdentities := make(map[string]string, len(document.Entries))
	fileCount := 0
	var totalFileBytes int64
	for index, entry := range document.Entries {
		label := fmt.Sprintf("application tree ledger entries[%d]", index)
		if entry.Path == "." {
			if entry.Type != "directory" {
				return fmt.Errorf("%s root entry must be a directory", label)
			}
		} else if err := ValidateArtifactPath(entry.Path); err != nil {
			return fmt.Errorf("%s.path: %w", label, err)
		}
		if _, duplicate := typesByPath[entry.Path]; duplicate {
			return fmt.Errorf("application tree ledger contains path %q more than once", entry.Path)
		}
		pathIdentity := strings.ToLower(entry.Path)
		if previous, duplicate := pathIdentities[pathIdentity]; duplicate {
			return fmt.Errorf(
				"application tree ledger paths %q and %q are case aliases",
				previous,
				entry.Path,
			)
		}
		pathIdentities[pathIdentity] = entry.Path
		typesByPath[entry.Path] = entry.Type
		if entry.Mode == nil || *entry.Mode > 0o777 {
			return fmt.Errorf("%s.mode is invalid", label)
		}
		switch entry.Type {
		case "directory":
			if !hasExactMembers(entry.members, "mode", "path", "type") {
				return fmt.Errorf("%s directory has an unexpected shape", label)
			}
			if entry.SHA256 != nil || entry.Size != nil {
				return fmt.Errorf("%s directory has file fields", label)
			}
		case "file":
			if !hasExactMembers(entry.members, "mode", "path", "sha256", "size", "type") {
				return fmt.Errorf("%s file has an unexpected shape", label)
			}
			if entry.SHA256 == nil || !sha256Pattern.MatchString(*entry.SHA256) ||
				entry.Size == nil || *entry.Size < 0 || *entry.Size > maxAppTreeFileBytes {
				return fmt.Errorf("%s file digest or size is invalid", label)
			}
			if totalFileBytes > maxAppTreeTotalBytes-*entry.Size {
				return fmt.Errorf(
					"application tree ledger file bytes exceed the %d-byte limit",
					maxAppTreeTotalBytes,
				)
			}
			totalFileBytes += *entry.Size
			fileCount++
		default:
			return fmt.Errorf(
				"%s.type must be directory or file; source-traceability payloads (R0) cannot contain symbolic links or special files",
				label,
			)
		}
	}
	if typesByPath["."] != "directory" || fileCount == 0 {
		return errors.New("application tree ledger must contain its root and at least one file")
	}
	for entryPath := range typesByPath {
		if entryPath == "." {
			continue
		}
		parent := pathpkg.Dir(entryPath)
		if typesByPath[parent] != "directory" {
			return fmt.Errorf("application tree ledger path %q has no declared parent directory", entryPath)
		}
	}
	return validateUnsignedPayloadLedgerShape(document.Entries, typesByPath)
}

func validateUnsignedPayloadLedgerShape(
	entries []appTreeLedgerEntry,
	typesByPath map[string]string,
) error {
	required := map[string]string{
		"vibermate-desktop":             "file",
		"vibermate":                     "file",
		"vibermated":                    "file",
		"vibermate-build-manifest.json": "file",
		"LICENSE":                       "file",
		"dist":                          "directory",
	}
	for requiredPath, requiredType := range required {
		if typesByPath[requiredPath] != requiredType {
			return fmt.Errorf(
				"application tree ledger must contain %q as a %s",
				requiredPath,
				requiredType,
			)
		}
	}
	for declaredPath := range typesByPath {
		if declaredPath == "." || pathpkg.Dir(declaredPath) != "." {
			continue
		}
		if _, allowed := required[declaredPath]; !allowed {
			return fmt.Errorf(
				"application tree ledger contains unexpected top-level payload path %q",
				declaredPath,
			)
		}
	}
	for _, executablePath := range []string{
		"vibermate-desktop",
		"vibermate",
		"vibermated",
	} {
		for _, entry := range entries {
			if entry.Path == executablePath && (*entry.Mode)&0o111 == 0 {
				return fmt.Errorf(
					"application tree ledger executable %q has no execute bit",
					executablePath,
				)
			}
		}
	}
	distHasFile := false
	reservedNames := map[string]string{
		"vibermate-desktop":             "vibermate-desktop",
		"vibermate":                     "vibermate",
		"vibermated":                    "vibermated",
		"vibermate-build-manifest.json": "vibermate-build-manifest.json",
		"LICENSE":                       "LICENSE",
	}
	for _, entry := range entries {
		if entry.Type == "file" && strings.HasPrefix(entry.Path, "dist/") {
			distHasFile = true
		}
		if rootPath, reserved := reservedNames[pathpkg.Base(entry.Path)]; reserved && entry.Path != rootPath {
			return fmt.Errorf(
				"application tree ledger contains reserved payload name %q outside its fixed location",
				pathpkg.Base(entry.Path),
			)
		}
	}
	if !distHasFile {
		return errors.New("application tree ledger dist directory must contain at least one file")
	}
	return nil
}

func validateSPDXDocument(payload []byte, manifest Manifest) error {
	var document spdxDocument
	if err := decodeClosedArtifactJSON(payload, &document); err != nil {
		return fmt.Errorf("SPDX document: %w", err)
	}
	if document.SPDXVersion != "SPDX-2.3" ||
		document.DataLicense != "CC0-1.0" ||
		document.SPDXID != "SPDXRef-DOCUMENT" {
		return errors.New("SPDX document header is not the supported SPDX 2.3 profile")
	}
	if document.Comment != expectedSPDXDocumentComment(manifest) {
		return errors.New("SPDX DocumentComment does not bind the release version, commit, and payload ledger")
	}
	if err := validateMetadataText("SPDX document name", document.Name, 256, false); err != nil {
		return err
	}
	if err := validateSPDXNamespace(document.DocumentNamespace); err != nil {
		return err
	}
	if err := validateUTCTimestamp("SPDX creationInfo.created", document.CreationInfo.Created); err != nil {
		return err
	}
	if len(document.CreationInfo.Creators) == 0 || len(document.CreationInfo.Creators) > 64 {
		return errors.New("SPDX creationInfo.creators must contain between 1 and 64 entries")
	}
	for index, creator := range document.CreationInfo.Creators {
		if err := validateMetadataText(
			fmt.Sprintf("SPDX creationInfo.creators[%d]", index),
			creator,
			256,
			false,
		); err != nil {
			return err
		}
		if !strings.HasPrefix(creator, "Person: ") &&
			!strings.HasPrefix(creator, "Organization: ") &&
			!strings.HasPrefix(creator, "Tool: ") {
			return fmt.Errorf("SPDX creationInfo.creators[%d] has an invalid creator kind", index)
		}
	}
	if len(document.Packages) == 0 || len(document.Packages) > 65536 {
		return errors.New("SPDX document must contain between 1 and 65536 packages")
	}
	seen := map[string]struct{}{"SPDXRef-DOCUMENT": {}}
	releasePackageCount := 0
	for index, pkg := range document.Packages {
		label := fmt.Sprintf("SPDX packages[%d]", index)
		if !spdxElementPattern.MatchString(pkg.SPDXID) || pkg.SPDXID == "SPDXRef-DOCUMENT" {
			return fmt.Errorf("%s.SPDXID is invalid", label)
		}
		if _, duplicate := seen[pkg.SPDXID]; duplicate {
			return fmt.Errorf("SPDX document contains SPDXID %q more than once", pkg.SPDXID)
		}
		seen[pkg.SPDXID] = struct{}{}
		if err := validateMetadataText(label+".name", pkg.Name, 256, false); err != nil {
			return err
		}
		if err := validateMetadataText(label+".versionInfo", pkg.VersionInfo, 256, false); err != nil {
			return err
		}
		if err := validateSPDXDownloadLocation(label+".downloadLocation", pkg.DownloadLocation); err != nil {
			return err
		}
		if pkg.FilesAnalyzed == nil || *pkg.FilesAnalyzed {
			return fmt.Errorf("%s.filesAnalyzed must explicitly be false in the minimal profile", label)
		}
		if !validSPDXNoAssertion(pkg.LicenseConcluded) ||
			!validSPDXNoAssertion(pkg.LicenseDeclared) ||
			!validSPDXNoAssertion(pkg.CopyrightText) {
			return fmt.Errorf("%s license or copyright assertion is invalid", label)
		}
		if pkg.Name == "vibermate" {
			releasePackageCount++
			if pkg.VersionInfo != manifest.Version {
				return errors.New("SPDX VibeMate package does not bind the release version")
			}
		}
	}
	if releasePackageCount != 1 {
		return errors.New("SPDX document must contain exactly one VibeMate release package")
	}
	return nil
}

func validateCapabilityEvidenceDocument(
	payload []byte,
	manifest Manifest,
	expectations []capabilityEvidenceExpectation,
) error {
	if len(expectations) == 0 {
		return errors.New("application/json metadata artifact is not referenced as capability evidence")
	}
	var document capabilityEvidenceDocument
	if err := decodeClosedArtifactJSON(payload, &document); err != nil {
		return fmt.Errorf("capability evidence: %w", err)
	}
	if document.Schema != CapabilityEvidenceSchema {
		return fmt.Errorf("capability evidence schema must be %q", CapabilityEvidenceSchema)
	}
	if err := ValidateCommit(document.Commit); err != nil {
		return fmt.Errorf("capability evidence commit: %w", err)
	}
	if err := validateRevision("capability evidence conformanceRevision", document.ConformanceRevision); err != nil {
		return err
	}
	if !identifierPattern.MatchString(document.Capability) {
		return errors.New("capability evidence capability is not a canonical identifier")
	}
	if !validCapabilitySupport(document.Status) {
		return errors.New("capability evidence status is unsupported")
	}
	if err := validateUTCTimestamp("capability evidence observedAt", document.ObservedAt); err != nil {
		return err
	}
	for _, expectation := range expectations {
		if document.Capability != expectation.capability {
			return errors.New("capability evidence does not bind the declared capability")
		}
		if expectation.evidenceStatus != EvidenceCurrent {
			continue
		}
		if document.Commit != manifest.Commit ||
			document.ConformanceRevision != expectation.conformanceRevision ||
			document.Status != expectation.status {
			return errors.New("current capability evidence does not bind the release commit, conformance revision, and status")
		}
	}
	return nil
}

func capabilityEvidenceExpectations(
	manifest Manifest,
) map[string][]capabilityEvidenceExpectation {
	expectations := make(map[string][]capabilityEvidenceExpectation)
	collect := func(revision string, capabilities []CapabilityStatus) {
		for _, capability := range capabilities {
			if capability.EvidenceStatus == EvidenceMissing {
				continue
			}
			expectations[capability.EvidenceArtifactRole] = append(
				expectations[capability.EvidenceArtifactRole],
				capabilityEvidenceExpectation{
					capability:          capability.Capability,
					status:              capability.Status,
					evidenceStatus:      capability.EvidenceStatus,
					conformanceRevision: revision,
				},
			)
		}
	}
	for _, platform := range manifest.PlatformSupport {
		collect(platform.ConformanceRevision, platform.HostCapabilities)
	}
	for _, host := range manifest.HostSupport {
		collect(host.ConformanceRevision, host.Capabilities)
	}
	return expectations
}

func decodeClosedArtifactJSON(payload []byte, destination any) error {
	if len(payload) == 0 {
		return errors.New("document is empty")
	}
	if !utf8.Valid(payload) {
		return errors.New("document is not valid UTF-8")
	}
	if err := rejectDuplicateMembers(payload); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode document: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("document contains trailing JSON")
	}
	return nil
}

func validateExactDigestMap(label string, actual map[string]string, expected []string) error {
	if actual == nil || len(actual) != len(expected) {
		return fmt.Errorf("%s has an unexpected key set", label)
	}
	for _, name := range expected {
		digest, exists := actual[name]
		if !exists || !sha256Pattern.MatchString(digest) {
			return fmt.Errorf("%s contains an invalid digest for %q", label, name)
		}
	}
	return nil
}

func hasExactMembers(actual map[string]struct{}, expected ...string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for _, name := range expected {
		if _, exists := actual[name]; !exists {
			return false
		}
	}
	return true
}

func validateMetadataText(
	label, value string,
	maximum int,
	allowNewline bool,
) error {
	if value == "" || !utf8.ValidString(value) || len(value) > maximum ||
		strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is empty, padded, invalid UTF-8, or exceeds the byte limit", label)
	}
	for _, character := range value {
		if character == '\n' && allowNewline {
			continue
		}
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("%s contains a control character", label)
		}
	}
	return nil
}

func validateUTCTimestamp(label, value string) error {
	if !utcTimestampPattern.MatchString(value) {
		return fmt.Errorf("%s must be a UTC RFC 3339 timestamp with whole seconds", label)
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("%s is invalid: %w", label, err)
	}
	return nil
}

func expectedSPDXDocumentComment(manifest Manifest) string {
	ledgerSHA256 := ""
	for _, artifact := range manifest.Artifacts {
		if artifact.Role == ArtifactRoleAppTreeLedger {
			ledgerSHA256 = artifact.SHA256
			break
		}
	}
	return fmt.Sprintf(
		"vibermate.release version=%s commit=%s payloadLedgerSHA256=%s",
		manifest.Version,
		manifest.Commit,
		ledgerSHA256,
	)
}

func validateSPDXNamespace(value string) error {
	if err := validateMetadataText("SPDX documentNamespace", value, 2048, false); err != nil {
		return err
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Fragment != "" || strings.Contains(value, "#") {
		return errors.New("SPDX documentNamespace must be an absolute URI without a fragment")
	}
	return nil
}

func validateSPDXDownloadLocation(label, value string) error {
	if value == "NONE" || value == "NOASSERTION" {
		return nil
	}
	if err := validateMetadataText(label, value, 2048, false); err != nil {
		return err
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Fragment != "" {
		return fmt.Errorf("%s must be NONE, NOASSERTION, or an absolute URI", label)
	}
	return nil
}

func validSPDXNoAssertion(value string) bool {
	return value == "NONE" || value == "NOASSERTION"
}
