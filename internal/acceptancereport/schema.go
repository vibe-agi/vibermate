// Package acceptancereport defines and verifies private packaged acceptance
// reports. A report is evidence, not configuration: callers must supply the
// exact mode, source revision, and fixed client they expect.
package acceptancereport

import (
	"time"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

const (
	SchemaV7                     = "vibermate.m0-assembly-acceptance/v7"
	DesktopBuildManifestSchemaV3 = "vibermate.desktop-build/v3"
	DesktopBuildManifestSchema   = DesktopBuildManifestSchemaV3
	ExpectedGoVersion            = "go1.25.12"
	ExpectedFlutterVersion       = "3.41.5"
	ExpectedFlutterRevision      = "2c9eb20739dfec95e2c74bd3dfa4601b0a8a36aa"
	ExpectedDartVersion          = "3.11.3"
	ExpectedXcodeVersion         = "Xcode 16.2\nBuild version 16C5032a"
	ExpectedPlatform             = "darwin"
	ExpectedArchitecture         = "arm64"
	ExpectedBuildTarget          = "aarch64-apple-darwin"
)

// Status is the closed report and check status vocabulary.
type Status string

const (
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
	StatusBlocked Status = "blocked"
)

// Mode distinguishes an offline deterministic run from its credentialed
// continuation. The verifier never infers this authority from the report.
type Mode string

const (
	ModeDeterministic Mode = "deterministic"
	ModeCredentialed  Mode = "credentialed"
)

func (mode Mode) Valid() bool {
	return mode == ModeDeterministic || mode == ModeCredentialed
}

type Check struct {
	ID     string `json:"id"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
}

type Client struct {
	ID      string                  `json:"id"`
	Version string                  `json:"version"`
	Adapter *clientadapter.Evidence `json:"adapter,omitempty"`
}

type SourceProvenance struct {
	VCS        string `json:"vcs"`
	Revision   string `json:"revision"`
	CommitTime string `json:"commitTime"`
	Dirty      bool   `json:"dirty"`
}

type ArtifactProvenance struct {
	Role   string `json:"role"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type ToolchainProvenance struct {
	Go      string `json:"go"`
	Flutter string `json:"flutter"`
	Dart    string `json:"dart"`
	Xcode   string `json:"xcode"`
}

type DesktopBuildToolchains struct {
	Go      string `json:"go"`
	Flutter string `json:"flutter"`
	Dart    string `json:"dart"`
	Xcode   string `json:"xcode"`
}

type BuildProvenance struct {
	ManifestSchema      string                 `json:"manifestSchema"`
	DesktopProfile      string                 `json:"desktopProfile"`
	SidecarProfile      string                 `json:"sidecarProfile"`
	Target              string                 `json:"target"`
	Toolkit             string                 `json:"toolkit"`
	Toolchains          DesktopBuildToolchains `json:"toolchains"`
	ConfigurationSHA256 map[string]string      `json:"configurationSHA256"`
	GoBuildVersions     map[string]string      `json:"goBuildVersions"`
	GoBuildTags         map[string]string      `json:"goBuildTags"`
}

type Configuration struct {
	DeterministicOnly bool   `json:"deterministicOnly"`
	ClientID          string `json:"clientId"`
	ClientVersion     string `json:"clientVersion"`
	EnvironmentID     string `json:"environmentId"`
	Timeout           string `json:"timeout"`
}

type Provenance struct {
	Source        SourceProvenance     `json:"source"`
	Artifacts     []ArtifactProvenance `json:"artifacts"`
	Toolchains    ToolchainProvenance  `json:"toolchains"`
	Build         BuildProvenance      `json:"build"`
	Configuration Configuration        `json:"configuration"`
}

type Report struct {
	Schema       string      `json:"schema"`
	StartedAt    time.Time   `json:"startedAt"`
	FinishedAt   time.Time   `json:"finishedAt"`
	Platform     string      `json:"platform"`
	Architecture string      `json:"architecture"`
	Client       Client      `json:"client"`
	Provenance   *Provenance `json:"provenance,omitempty"`
	Status       Status      `json:"status"`
	Checks       []Check     `json:"checks"`
}

// ArtifactCoordinates are trusted verifier inputs for one current v6 run.
// They never come from the report: the workflow supplies the source checkout,
// selected App, acceptance executable, and fixed client independently so a
// report cannot choose which bytes are used to verify its own digests.
type ArtifactCoordinates struct {
	SourceRoot           string
	DesktopApp           string
	AcceptanceExecutable string
	ClientEntrypoint     string
}

// Expectations are intentionally explicit. VerifyFile never obtains these
// coordinates from the worktree, the environment, or the report itself.
type Expectations struct {
	Mode          Mode
	Schema        string
	Revision      string
	ClientID      string
	ClientVersion string
	Artifacts     ArtifactCoordinates
}
