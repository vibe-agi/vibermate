package acceptancereport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
)

const (
	MaxReportBytes  = int64(2 << 20)
	maxArtifactSize = int64(8 << 30)
)

var (
	requiredArtifactRoles = []string{
		"acceptance",
		"app-framework",
		"client-entrypoint",
		"daemon",
		"desktop-app-bundle",
		"desktop-app-executable",
		"desktop-build-manifest",
		"flutter-macos-framework",
		"launcher",
	}
	requiredConfigurationDigests = []string{
		"go.mod",
		"go.sum",
		"ui/flutter_app/.metadata",
		"ui/flutter_app/pubspec.yaml",
		"ui/flutter_app/pubspec.lock",
		"ui/flutter_app/tool/flutter-sdk.env",
		"ui/flutter_app/macos/Runner.xcodeproj/project.pbxproj",
		"ui/flutter_app/macos/Runner/Configs/AppInfo.xcconfig",
		"ui/flutter_app/macos/Runner/Configs/Release.xcconfig",
		"ui/flutter_app/macos/Runner/Info.plist",
		"ui/flutter_app/macos/Runner/Release.entitlements",
	}
	requiredGoBinaries     = []string{"acceptance", "daemon", "launcher"}
	requiredClaudeChecksV7 = []string{
		"build-provenance",
		"fixed-client-identity",
		"exclusive-generation-preflight",
		"packaged-desktop-shell",
		"packaged-main-navigation-cold-restore",
		"private-data-directory",
		"daemon-first-start",
		"environment-publish",
		"capture-environment-assignment",
		"daemon-sigint",
		"environment-recovery",
		"capture-assignment-recovery",
		"deterministic-shutdown",
	}
	requiredCodexChecksV7              = requiredClaudeChecksV7
	requiredClaudeCredentialedChecksV7 = credentialedCheckIDs(
		requiredClaudeChecksV7,
		false,
	)
	requiredCodexCredentialedChecksV7 = credentialedCheckIDs(
		requiredCodexChecksV7,
		true,
	)
)

type fixedClientExpectation struct {
	evidence             clientadapter.Evidence
	checksV7             []string
	credentialedChecksV7 []string
}

func credentialedCheckIDs(deterministic []string, _ bool) []string {
	checks := append([]string(nil), deterministic[:len(deterministic)-1]...)
	checks = append(checks,
		"deterministic-phase-shutdown",
		"credentialed-private-data-directory",
		"provider-account",
		"credentialed-environment-publish",
		"credentialed-capture-environment-assignment",
		"credentialed-managed-request",
		"credentialed-managed-evidence",
		"credentialed-recovery",
		"credentialed-recovered-request",
	)
	return append(checks, "final-shutdown")
}

// VerifyFile verifies one private current report through the complete public
// package entrypoint. A missing, unreadable, or retired-schema report is an
// error, never a skip.
func VerifyFile(path string, expected Expectations) error {
	fixedClient, err := validateExpectations(expected)
	if err != nil {
		return fmt.Errorf("invalid acceptance expectations: %w", err)
	}
	payload, err := readPrivateReport(path)
	if err != nil {
		return fmt.Errorf("read acceptance report: %w", err)
	}
	report, err := decodeReport(payload)
	if err != nil {
		return fmt.Errorf("decode acceptance report: %w", err)
	}
	if err := verifyReport(report, expected, fixedClient); err != nil {
		return fmt.Errorf("verify acceptance report: %w", err)
	}
	return nil
}

// RequiredCheckIDs returns a copy of the current check contract for one mode
// and supported fixed client. It is primarily useful to producers and fixtures
// that emit SchemaV7 reports.
func RequiredCheckIDs(
	mode Mode,
	clientID, clientVersion string,
) ([]string, error) {
	expected, err := fixedClient(clientID, clientVersion)
	if err != nil {
		return nil, err
	}
	switch mode {
	case ModeDeterministic:
		return append([]string(nil), expected.checksV7...), nil
	case ModeCredentialed:
		return append([]string(nil), expected.credentialedChecksV7...), nil
	default:
		return nil, errors.New("acceptance mode is unsupported")
	}
}

func validateExpectations(expected Expectations) (fixedClientExpectation, error) {
	if !expected.Mode.Valid() {
		return fixedClientExpectation{}, errors.New(
			"expected mode must be deterministic or credentialed",
		)
	}
	if expected.Schema != SchemaV7 {
		return fixedClientExpectation{}, errors.New(
			"expected schema must be the current report schema",
		)
	}
	if !validRevision(expected.Revision) {
		return fixedClientExpectation{}, errors.New(
			"expected revision must be a full lowercase Git commit identity",
		)
	}
	if _, err := normalizeArtifactCoordinates(expected.Artifacts); err != nil {
		return fixedClientExpectation{}, fmt.Errorf(
			"current artifact coordinates: %w",
			err,
		)
	}
	return fixedClient(expected.ClientID, expected.ClientVersion)
}

func fixedClient(id, version string) (fixedClientExpectation, error) {
	var checksV7 []string
	switch {
	case id == "claude-code" && version == "2.1.220":
		checksV7 = requiredClaudeChecksV7
	case id == "codex-cli" && version == "0.145.0":
		checksV7 = requiredCodexChecksV7
	default:
		return fixedClientExpectation{}, errors.New(
			"expected client must be claude-code 2.1.220 or codex-cli 0.145.0",
		)
	}
	evidence, ok := clientadapter.BuiltInCatalog().ExpectedEvidence(id, version)
	if !ok {
		return fixedClientExpectation{}, errors.New(
			"expected client is absent from the built-in release catalog",
		)
	}
	return fixedClientExpectation{
		evidence: evidence,
		checksV7: checksV7,
		credentialedChecksV7: func() []string {
			if id == "codex-cli" {
				return requiredCodexCredentialedChecksV7
			}
			return requiredClaudeCredentialedChecksV7
		}(),
	}, nil
}

func readPrivateReport(path string) ([]byte, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("report path must be absolute and clean")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("report must be a regular file, not a link")
	}
	if runtime.GOOS != "windows" && before.Mode().Perm() != 0o600 {
		return nil, errors.New("report file must have mode 0600")
	}
	if before.Size() <= 0 || before.Size() > MaxReportBytes {
		return nil, errors.New("report file size is outside the allowed bound")
	}
	opened, err := openReadNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	afterOpen, err := opened.Stat()
	if err != nil {
		return nil, err
	}
	if err := validateOpenedPrivateReport(opened); err != nil {
		return nil, err
	}
	if !os.SameFile(before, afterOpen) {
		return nil, errors.New("report changed while opening")
	}
	payload, err := io.ReadAll(io.LimitReader(opened, MaxReportBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > MaxReportBytes {
		return nil, errors.New("report exceeds the size limit")
	}
	afterRead, err := opened.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(afterOpen, afterRead) ||
		afterOpen.Size() != afterRead.Size() ||
		!afterOpen.ModTime().Equal(afterRead.ModTime()) ||
		int64(len(payload)) != afterRead.Size() {
		return nil, errors.New("report changed while reading")
	}
	return payload, nil
}

func decodeReport(payload []byte) (Report, error) {
	if err := rejectDuplicateJSONNames(payload); err != nil {
		return Report{}, err
	}
	var report Report
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return Report{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Report{}, errors.New("report has trailing JSON data")
	}
	return report, nil
}

func rejectDuplicateJSONNames(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("report has trailing JSON data")
		}
		return err
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		names := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("report object member name is invalid")
			}
			if _, duplicate := names[name]; duplicate {
				return fmt.Errorf("report contains duplicate JSON field %q", name)
			}
			names[name] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("report object is malformed")
		}
		return nil
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("report array is malformed")
		}
		return nil
	default:
		return errors.New("report JSON delimiter is malformed")
	}
}

func verifyReport(
	report Report,
	expected Expectations,
	fixed fixedClientExpectation,
) error {
	if report.Schema != expected.Schema {
		return errors.New("report schema differs from the expected schema")
	}
	requiredChecks, err := requiredChecksForSchema(
		expected.Schema,
		expected.Mode,
		fixed,
	)
	if err != nil {
		return errors.New("report schema is unsupported")
	}
	if report.Platform != ExpectedPlatform ||
		report.Architecture != ExpectedArchitecture {
		return errors.New("report platform is not macOS arm64")
	}
	if report.StartedAt.IsZero() || report.FinishedAt.IsZero() ||
		report.FinishedAt.Before(report.StartedAt) {
		return errors.New("report timestamps are invalid")
	}
	if report.Status != StatusPassed {
		return errors.New("report status is not passed")
	}
	if report.Client.ID != expected.ClientID ||
		report.Client.Version != expected.ClientVersion {
		return errors.New("report client differs from the expected fixed client")
	}
	if report.Client.Adapter == nil {
		return errors.New("report has no fixed-client adapter evidence")
	}
	if err := report.Client.Adapter.Validate(); err != nil {
		return fmt.Errorf("client adapter evidence: %w", err)
	}
	if *report.Client.Adapter != fixed.evidence {
		return errors.New("client adapter evidence differs from the fixed release catalog")
	}
	if report.Provenance == nil {
		return errors.New("report provenance is missing")
	}
	if err := verifySource(report.Provenance.Source, expected.Revision); err != nil {
		return err
	}
	if err := verifyConfiguration(
		report.Provenance.Configuration,
		expected,
	); err != nil {
		return err
	}
	if err := verifyChecks(report.Checks, requiredChecks); err != nil {
		return err
	}
	if err := verifyArtifacts(report.Provenance.Artifacts); err != nil {
		return err
	}
	if err := verifyToolchains(report.Provenance.Toolchains); err != nil {
		return err
	}
	if err := verifyBuild(report.Provenance.Build); err != nil {
		return err
	}
	if report.Provenance.Build.ManifestSchema != DesktopBuildManifestSchemaV3 {
		return errors.New("current report does not use the current Desktop build manifest")
	}
	if err := verifyToolchainContinuity(
		report.Provenance.Toolchains,
		report.Provenance.Build.Toolchains,
	); err != nil {
		return err
	}
	if err := verifyCurrentArtifacts(report, expected); err != nil {
		return fmt.Errorf("verify current artifacts: %w", err)
	}
	return nil
}

func requiredChecksForSchema(
	schema string,
	mode Mode,
	fixed fixedClientExpectation,
) ([]string, error) {
	if schema != SchemaV7 {
		return nil, errors.New("report schema is unsupported")
	}
	if mode == ModeCredentialed {
		return append([]string(nil), fixed.credentialedChecksV7...), nil
	}
	if mode == ModeDeterministic {
		return append([]string(nil), fixed.checksV7...), nil
	}
	return nil, errors.New("report mode is unsupported")
}

func verifySource(source SourceProvenance, expectedRevision string) error {
	if source.VCS != "git" || !validRevision(source.Revision) {
		return errors.New("report source identity is not a full Git revision")
	}
	if source.Dirty {
		return errors.New("report source worktree was dirty")
	}
	if source.Revision != expectedRevision {
		return errors.New("report source revision is stale")
	}
	commitTime, err := time.Parse(time.RFC3339, source.CommitTime)
	if err != nil || commitTime.IsZero() {
		return errors.New("report source commit time is invalid")
	}
	return nil
}

func verifyConfiguration(
	configuration Configuration,
	expected Expectations,
) error {
	deterministicOnly := expected.Mode == ModeDeterministic
	if configuration.DeterministicOnly != deterministicOnly {
		return errors.New("report configuration mode differs from the expected mode")
	}
	if configuration.ClientID != expected.ClientID ||
		configuration.ClientVersion != expected.ClientVersion {
		return errors.New("report configuration has client drift")
	}
	if _, err := environment.NewEnvironmentID(configuration.EnvironmentID); err != nil {
		return errors.New("report configuration Environment ID is invalid")
	}
	timeout, err := time.ParseDuration(configuration.Timeout)
	if err != nil || timeout <= 0 {
		return errors.New("report configuration timeout is invalid")
	}
	return nil
}

func verifyChecks(checks []Check, required []string) error {
	if len(checks) != len(required) {
		return errors.New("report check set is incomplete or contains extras")
	}
	requiredSet := make(map[string]struct{}, len(required))
	for _, id := range required {
		requiredSet[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(checks))
	for _, check := range checks {
		if _, ok := requiredSet[check.ID]; !ok {
			return fmt.Errorf("report check ID is unexpected: %q", check.ID)
		}
		if _, duplicate := seen[check.ID]; duplicate {
			return fmt.Errorf("report check ID is duplicated: %q", check.ID)
		}
		seen[check.ID] = struct{}{}
		if check.Status != StatusPassed {
			return fmt.Errorf("report check did not pass: %s", check.ID)
		}
		if !boundedTrimmed(check.Detail, 1, 16<<10) {
			return fmt.Errorf("report check detail is invalid: %s", check.ID)
		}
	}
	for _, id := range required {
		if _, ok := seen[id]; !ok {
			return fmt.Errorf("report check is missing: %s", id)
		}
	}
	return nil
}

func verifyArtifacts(artifacts []ArtifactProvenance) error {
	if len(artifacts) != len(requiredArtifactRoles) {
		return errors.New("report artifact set is incomplete or contains extras")
	}
	required := make(map[string]struct{}, len(requiredArtifactRoles))
	for _, role := range requiredArtifactRoles {
		required[role] = struct{}{}
	}
	roles := make(map[string]struct{}, len(artifacts))
	byRole := make(map[string]ArtifactProvenance, len(artifacts))
	paths := make(map[string]struct{}, len(artifacts))
	digests := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		if _, ok := required[artifact.Role]; !ok {
			return fmt.Errorf("report artifact role is unexpected: %q", artifact.Role)
		}
		if _, duplicate := roles[artifact.Role]; duplicate {
			return fmt.Errorf("report artifact role is duplicated: %s", artifact.Role)
		}
		roles[artifact.Role] = struct{}{}
		byRole[artifact.Role] = artifact
		if artifact.Path == "" || len(artifact.Path) > 4096 ||
			!filepath.IsAbs(artifact.Path) ||
			filepath.Clean(artifact.Path) != artifact.Path {
			return fmt.Errorf("report artifact path is invalid: %s", artifact.Role)
		}
		if _, duplicate := paths[artifact.Path]; duplicate {
			return errors.New("report artifact path is duplicated")
		}
		paths[artifact.Path] = struct{}{}
		if !validSHA256(artifact.SHA256) {
			return fmt.Errorf("report artifact digest is invalid: %s", artifact.Role)
		}
		if _, duplicate := digests[artifact.SHA256]; duplicate {
			return errors.New("report artifact digest is duplicated")
		}
		digests[artifact.SHA256] = struct{}{}
		if artifact.Bytes <= 0 || artifact.Bytes > maxArtifactSize {
			return fmt.Errorf("report artifact size is invalid: %s", artifact.Role)
		}
	}
	bundle := byRole["desktop-app-bundle"].Path
	if filepath.Ext(bundle) != ".app" {
		return errors.New("report Desktop App bundle path is invalid")
	}
	requiredMembers := map[string]string{
		"app-framework": filepath.Join(
			bundle,
			"Contents", "Frameworks", "App.framework", "Versions", "A", "App",
		),
		"desktop-app-executable": filepath.Join(
			bundle,
			"Contents",
			"MacOS",
			"vibermate-desktop",
		),
		"daemon":   filepath.Join(bundle, "Contents", "MacOS", "vibermated"),
		"launcher": filepath.Join(bundle, "Contents", "MacOS", "vibermate"),
		"desktop-build-manifest": filepath.Join(
			bundle,
			"Contents",
			"Resources",
			"vibermate-build-manifest.json",
		),
		"flutter-macos-framework": filepath.Join(
			bundle,
			"Contents", "Frameworks", "FlutterMacOS.framework", "Versions", "A", "FlutterMacOS",
		),
	}
	for role, expectedPath := range requiredMembers {
		if byRole[role].Path != expectedPath {
			return fmt.Errorf("report artifact is not the selected App member: %s", role)
		}
	}
	return nil
}

func verifyToolchains(toolchains ToolchainProvenance) error {
	if !validGoToolchain(toolchains.Go) ||
		toolchains.Flutter != expectedFlutterToolchain() ||
		toolchains.Dart != "Dart "+ExpectedDartVersion ||
		toolchains.Xcode != ExpectedXcodeVersion {
		return errors.New("report runtime toolchains are not the frozen versions")
	}
	return nil
}

func verifyBuild(build BuildProvenance) error {
	if build.ManifestSchema != DesktopBuildManifestSchemaV3 ||
		build.DesktopProfile != "release" ||
		(build.SidecarProfile != "development" &&
			build.SidecarProfile != "release") ||
		build.Target != ExpectedBuildTarget ||
		build.Toolkit != "flutter" {
		return errors.New("report Desktop build identity is invalid")
	}
	tools := build.Toolchains
	if !validGoToolchain(tools.Go) ||
		tools.Flutter != expectedFlutterToolchain() ||
		tools.Dart != "Dart "+ExpectedDartVersion ||
		tools.Xcode != ExpectedXcodeVersion {
		return errors.New("report Desktop build toolchains are not frozen")
	}
	if err := verifyDigestMap(
		build.ConfigurationSHA256,
		requiredConfigurationDigests,
		"Desktop configuration",
	); err != nil {
		return err
	}
	if len(build.GoBuildVersions) != len(requiredGoBinaries) {
		return errors.New("report Go build-version set is incomplete or contains extras")
	}
	if len(build.GoBuildTags) != len(requiredGoBinaries) {
		return errors.New("report Go build-tag set is incomplete or contains extras")
	}
	for _, role := range requiredGoBinaries {
		if build.GoBuildVersions[role] != ExpectedGoVersion {
			return fmt.Errorf("report Go build version is invalid: %s", role)
		}
		if _, ok := build.GoBuildTags[role]; !ok {
			return fmt.Errorf("report Go build tags are missing: %s", role)
		}
	}
	if _, ok := build.GoBuildVersions["acceptance"]; !ok {
		return errors.New("report acceptance build version is missing")
	}
	if build.GoBuildTags["acceptance"] != "" {
		return errors.New("report acceptance build tags are unexpected")
	}
	switch build.SidecarProfile {
	case "development":
		if build.GoBuildTags["daemon"] != "" ||
			build.GoBuildTags["launcher"] != "" {
			return errors.New("report development sidecars have release build tags")
		}
	case "release":
		if build.GoBuildTags["daemon"] != "vibermate_native_secrets" ||
			build.GoBuildTags["launcher"] != "vibermate_native_secrets" {
			return errors.New("report release sidecar build tags are invalid")
		}
	}
	return nil
}

func verifyToolchainContinuity(
	runtimeTools ToolchainProvenance,
	buildTools DesktopBuildToolchains,
) error {
	if runtimeTools.Go != buildTools.Go ||
		runtimeTools.Flutter != buildTools.Flutter ||
		runtimeTools.Dart != buildTools.Dart ||
		runtimeTools.Xcode != buildTools.Xcode {
		return errors.New(
			"report build and acceptance toolchain evidence differs",
		)
	}
	return nil
}

func verifyDigestMap(
	digests map[string]string,
	required []string,
	label string,
) error {
	if len(digests) != len(required) {
		return fmt.Errorf("report %s digest set is incomplete or contains extras", label)
	}
	for _, name := range required {
		if !validSHA256(digests[name]) {
			return fmt.Errorf("report %s digest is invalid: %s", label, name)
		}
	}
	return nil
}

func validGoToolchain(value string) bool {
	return value == "go version "+ExpectedGoVersion+" darwin/arm64"
}

func expectedFlutterToolchain() string {
	return "Flutter " + ExpectedFlutterVersion + " (" + ExpectedFlutterRevision + ")"
}

func validRevision(value string) bool {
	const fullGitRevisionBytes = 20
	if len(value) != fullGitRevisionBytes*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == fullGitRevisionBytes
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func boundedTrimmed(value string, minimum, maximum int) bool {
	return len(value) >= minimum && len(value) <= maximum &&
		strings.TrimSpace(value) == value
}
