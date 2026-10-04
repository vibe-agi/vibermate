package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
)

var errNativeSourceUnbound = errors.New("native scenario observations are not bound to clean source")

const nativeSyntheticTag = "vibermate_acceptance_synthetic"
const nativeSyntheticDefinitionDigest = "9ec74da54cc7f865d22dc1d020abf5a6d9a7df1cd7a1db07d509c6394329065f"

type nativeArtifactEvidence struct {
	Scope                      string `json:"scope"`
	SyntheticProfileIdentifier string `json:"syntheticProfileIdentifier"`
	ProfileDefinitionDigest    string `json:"profileDefinitionDigest"`
	GoBuildTags                string `json:"goBuildTags"`
	GoVersion                  string `json:"goVersion"`
	ExecutableSHA256           string `json:"executableSha256"`
}

func nativeProfileRequired(name string) bool {
	switch name {
	case "default_call_text", "default_call_image", "large_result_page", "review_allow_once", "sse_reconnect":
		return true
	}
	return false
}

func nativeEvidenceFromBuild(info *debug.BuildInfo, available bool) (nativeArtifactEvidence, error) {
	e := nativeArtifactEvidence{Scope: "synthetic_private_runtime"}
	if info == nil || info.GoVersion == "" {
		return e, errors.New("native build metadata unavailable")
	}
	e.GoVersion = info.GoVersion
	for _, setting := range info.Settings {
		if setting.Key == "-tags" {
			e.GoBuildTags = canonicalBuildTags(setting.Value)
		}
	}
	if hasBuildTag(e.GoBuildTags, nativeSyntheticTag) != available {
		return e, errors.New("native profile tag/adapter mismatch")
	}
	if available {
		e.SyntheticProfileIdentifier = "synthetic_read/v1"
		e.ProfileDefinitionDigest = nativeSyntheticDefinitionDigest
	}
	return e, nil
}

func nativeExecutedArtifactEvidence() (nativeArtifactEvidence, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nativeArtifactEvidence{}, errors.New("native build info unavailable")
	}
	_, available := nativeAcceptanceContentProfiles()
	e, err := nativeEvidenceFromBuild(info, available)
	if err != nil {
		return e, err
	}
	path, err := os.Executable()
	if err != nil {
		return e, err
	}
	file, err := os.Open(path)
	if err != nil {
		return e, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() <= 0 || stat.Size() > 512<<20 {
		return e, errors.New("native executable artifact unavailable")
	}
	digest := sha256.New()
	if count, err := io.Copy(digest, io.LimitReader(file, stat.Size()+1)); err != nil || count != stat.Size() {
		return e, errors.New("native executable artifact changed")
	}
	e.ExecutableSHA256 = hex.EncodeToString(digest.Sum(nil))
	return e, nil
}

// The caller obtains actual from the running binary's build info and on-disk
// artifact. This comparison is not a signature or authentication of copied JSON.
func validateNativeArtifactCases(report *nativeMCPReport, actual nativeArtifactEvidence, evidenceErr error) bool {
	invalid := false
	_, available := nativeAcceptanceContentProfiles()
	for i := range report.Cases {
		if report.Cases[i].Outcome != "passed" {
			continue
		}
		reason := ""
		if nativeProfileRequired(report.Cases[i].Name) && !available {
			reason = "synthetic_content_profiles_required"
		} else if evidenceErr != nil || report.nativeArtifactEvidence != actual {
			reason = "artifact_provenance_unavailable"
		}
		if reason != "" {
			report.Cases[i].Outcome = "incomplete"
			report.Cases[i].Reason = reason
			invalid = true
		}
	}
	return invalid
}

func nativeSourceProvenance(ctx context.Context, directory string) (string, map[string]string, error) {
	git := func(arguments ...string) (string, error) {
		command := exec.CommandContext(ctx, "git", arguments...)
		command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + directory, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
		output := newBoundedBuffer(64 << 10)
		command.Stdout = output
		command.Stderr = output
		err := command.Run()
		data, overflow := output.snapshot()
		if err != nil || overflow {
			return "", errors.New("native source provenance unavailable")
		}
		return strings.TrimSpace(string(data)), nil
	}
	root, err := git("rev-parse", "--show-toplevel")
	if err != nil || !filepath.IsAbs(root) {
		return "", nil, errors.New("native source checkout unavailable")
	}
	sha, err := git("rev-parse", "HEAD")
	decoded, decodeErr := hex.DecodeString(sha)
	if err != nil || decodeErr != nil || len(decoded) != 20 {
		return "", nil, errors.New("native source SHA unavailable")
	}
	fixtures := map[string]string{}
	for _, name := range []string{"pinned_clients.json", "codex-0.145-initialize.synthetic.json", "codex-0.159-initialize.synthetic.json", "codex-0.160-callbacks.synthetic.json", "recorded-webp.base64"} {
		path := filepath.Join(root, "internal", "nativemcp", "testdata", name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return sha, fixtures, errors.New("native fixture provenance unavailable")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return sha, fixtures, errors.New("native fixture provenance unavailable")
		}
		digest := sha256.Sum256(data)
		fixtures[name] = hex.EncodeToString(digest[:])
	}
	// The real-client stream frames and canonical TLS composition are owned
	// here rather than by the older module-only JSON examples.
	for _, name := range []string{"cmd/vibermate-acceptance/native_mcp_fixture.go", "cmd/vibermate-acceptance/native_mcp_audit.go", "cmd/vibermate-acceptance/native_mcp_profiles_synthetic.go", "cmd/vibermate-acceptance/native_mcp_profiles_disabled.go", "internal/nativemcp/acceptance_synthetic_profiles.go"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return sha, fixtures, errors.New("native executable fixture provenance unavailable")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return sha, fixtures, errors.New("native executable fixture provenance unavailable")
		}
		digest := sha256.Sum256(data)
		fixtures[name] = hex.EncodeToString(digest[:])
	}
	dirty, err := git("status", "--porcelain", "--untracked-files=normal")
	if err != nil || dirty != "" {
		return sha, fixtures, errors.New("native source checkout is not clean")
	}
	build, ok := debug.ReadBuildInfo()
	if !ok {
		return sha, fixtures, errors.New("native build provenance unavailable")
	}
	revision, modified := "", ""
	for _, setting := range build.Settings {
		if setting.Key == "vcs.revision" {
			revision = setting.Value
		}
		if setting.Key == "vcs.modified" {
			modified = setting.Value
		}
	}
	if revision != sha || modified != "false" {
		return sha, fixtures, errors.New("native build does not match clean source")
	}
	return sha, fixtures, nil
}

func nativeBuildMatchesSource(sha string) bool {
	decoded, err := hex.DecodeString(sha)
	if err != nil || len(decoded) != 20 || strings.ToLower(sha) != sha {
		return false
	}
	build, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	revision, modified := "", ""
	for _, setting := range build.Settings {
		if setting.Key == "vcs.revision" {
			revision = setting.Value
		}
		if setting.Key == "vcs.modified" {
			modified = setting.Value
		}
	}
	return revision == sha && modified == "false"
}
