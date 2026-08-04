package acceptancereport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxDesktopBuildManifestBytes = int64(128 << 10)
	sourceVerificationTimeout    = 10 * time.Second
)

type desktopBuildProfiles struct {
	Desktop  string `json:"desktop"`
	Sidecars string `json:"sidecars"`
	Target   string `json:"target"`
}

type desktopBuildManifest struct {
	Schema              string                 `json:"schema"`
	Source              SourceProvenance       `json:"source"`
	Profiles            desktopBuildProfiles   `json:"profiles"`
	Toolchains          DesktopBuildToolchains `json:"toolchains"`
	ConfigurationSHA256 map[string]string      `json:"configurationSHA256"`
	SidecarSHA256       map[string]string      `json:"sidecarSHA256"`
}

// DigestArtifact returns stable evidence for one direct regular file. It
// rejects links and a file that changes while it is being read.
func DigestArtifact(role, path string) (ArtifactProvenance, error) {
	if role == "" {
		return ArtifactProvenance{}, errors.New("artifact role is empty")
	}
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ArtifactProvenance{}, fmt.Errorf("%s artifact path is invalid", role)
	}
	before, err := os.Lstat(path)
	if err != nil {
		return ArtifactProvenance{}, fmt.Errorf("inspect %s artifact: %w", role, err)
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return ArtifactProvenance{}, fmt.Errorf(
			"%s artifact is not a direct regular file",
			role,
		)
	}
	if before.Size() <= 0 || before.Size() > maxArtifactSize {
		return ArtifactProvenance{}, fmt.Errorf("%s artifact size is invalid", role)
	}
	opened, err := openReadNoFollow(path)
	if err != nil {
		return ArtifactProvenance{}, fmt.Errorf("open %s artifact: %w", role, err)
	}
	defer opened.Close()
	afterOpen, err := opened.Stat()
	if err != nil {
		return ArtifactProvenance{}, err
	}
	if !afterOpen.Mode().IsRegular() || !os.SameFile(before, afterOpen) {
		return ArtifactProvenance{}, fmt.Errorf("%s artifact changed while opening", role)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, opened); err != nil {
		return ArtifactProvenance{}, fmt.Errorf("digest %s artifact: %w", role, err)
	}
	afterRead, err := opened.Stat()
	if err != nil {
		return ArtifactProvenance{}, err
	}
	if !os.SameFile(afterOpen, afterRead) ||
		afterOpen.Size() != afterRead.Size() ||
		!afterOpen.ModTime().Equal(afterRead.ModTime()) {
		return ArtifactProvenance{}, fmt.Errorf("%s artifact changed while reading", role)
	}
	return ArtifactProvenance{
		Role:   role,
		Path:   path,
		SHA256: hex.EncodeToString(hash.Sum(nil)),
		Bytes:  afterRead.Size(),
	}, nil
}

// DigestBundle returns the deterministic tree digest used by both the report
// producer and verifier. File content, relative path, mode, size, directory
// mode, and link target all contribute to the digest.
func DigestBundle(root string) (ArtifactProvenance, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return ArtifactProvenance{}, errors.New("App bundle path is invalid")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return ArtifactProvenance{}, err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return ArtifactProvenance{}, errors.New("App bundle is not a direct directory")
	}
	hash := sha256.New()
	var total int64
	err = filepath.WalkDir(root, func(
		path string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.Type().IsRegular():
			fileEvidence, err := DigestArtifact("bundle-member", path)
			if err != nil {
				return err
			}
			total += fileEvidence.Bytes
			_, err = fmt.Fprintf(
				hash,
				"file\x00%s\x00%04o\x00%d\x00%s\x00",
				filepath.ToSlash(relative),
				info.Mode().Perm(),
				fileEvidence.Bytes,
				fileEvidence.SHA256,
			)
			return err
		case entry.IsDir():
			_, err = fmt.Fprintf(
				hash,
				"dir\x00%s\x00%04o\x00",
				filepath.ToSlash(relative),
				info.Mode().Perm(),
			)
			return err
		case entry.Type()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(
				hash,
				"link\x00%s\x00%s\x00",
				filepath.ToSlash(relative),
				target,
			)
			return err
		default:
			return fmt.Errorf(
				"unsupported App bundle member type: %s",
				filepath.ToSlash(relative),
			)
		}
	})
	if err != nil {
		return ArtifactProvenance{}, err
	}
	if total <= 0 || total > maxArtifactSize {
		return ArtifactProvenance{}, errors.New("App bundle size is invalid")
	}
	return ArtifactProvenance{
		Role:   "desktop-app-bundle",
		Path:   root,
		SHA256: hex.EncodeToString(hash.Sum(nil)),
		Bytes:  total,
	}, nil
}

func verifyCurrentArtifacts(report Report, expected Expectations) error {
	coordinates, err := normalizeArtifactCoordinates(expected.Artifacts)
	if err != nil {
		return err
	}
	provenance := report.Provenance
	if provenance == nil {
		return errors.New("report provenance is missing")
	}
	paths, err := expectedArtifactPaths(coordinates)
	if err != nil {
		return err
	}
	byRole := make(map[string]ArtifactProvenance, len(provenance.Artifacts))
	for _, artifact := range provenance.Artifacts {
		byRole[artifact.Role] = artifact
	}
	for role, path := range paths {
		if byRole[role].Path != path {
			return fmt.Errorf("report artifact differs from trusted coordinate: %s", role)
		}
	}

	bundleBefore, err := DigestBundle(coordinates.DesktopApp)
	if err != nil {
		return fmt.Errorf("digest App bundle before member verification: %w", err)
	}
	actual := make(map[string]ArtifactProvenance, len(paths))
	for role, path := range paths {
		if role == "desktop-app-bundle" {
			continue
		}
		evidence, err := DigestArtifact(role, path)
		if err != nil {
			return err
		}
		actual[role] = evidence
	}
	manifest, err := readDesktopBuildManifest(
		paths["desktop-build-manifest"],
	)
	if err != nil {
		return fmt.Errorf("read verified Desktop build manifest: %w", err)
	}
	bundleAfter, err := DigestBundle(coordinates.DesktopApp)
	if err != nil {
		return fmt.Errorf("digest App bundle after member verification: %w", err)
	}
	if bundleBefore.SHA256 != bundleAfter.SHA256 ||
		bundleBefore.Bytes != bundleAfter.Bytes {
		return errors.New("App bundle changed while verifying its members")
	}
	actual["desktop-app-bundle"] = bundleAfter
	for role, evidence := range actual {
		reported := byRole[role]
		if reported.SHA256 != evidence.SHA256 || reported.Bytes != evidence.Bytes {
			return fmt.Errorf("report artifact content differs from disk: %s", role)
		}
	}

	if err := verifyDesktopBuildManifest(
		manifest,
		provenance,
		actual,
		expected.Revision,
	); err != nil {
		return err
	}
	if err := verifySourceCheckout(
		coordinates.SourceRoot,
		provenance.Source,
		expected.Revision,
	); err != nil {
		return err
	}
	if err := verifyConfigurationFiles(
		coordinates.SourceRoot,
		manifest.ConfigurationSHA256,
	); err != nil {
		return err
	}
	return nil
}

func verifySourceCheckout(
	sourceRoot string,
	source SourceProvenance,
	expectedRevision string,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), sourceVerificationTimeout)
	defer cancel()

	topLevel, err := gitOutput(ctx, sourceRoot, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("identify source checkout: %w", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(sourceRoot)
	if err != nil {
		return fmt.Errorf("resolve source checkout: %w", err)
	}
	canonicalTop, err := filepath.EvalSymlinks(topLevel)
	if err != nil {
		return fmt.Errorf("resolve Git top-level checkout: %w", err)
	}
	if canonicalTop != canonicalRoot {
		return errors.New("source root is not the Git top-level checkout")
	}

	head, err := gitOutput(ctx, sourceRoot, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return fmt.Errorf("read source checkout revision: %w", err)
	}
	if head != expectedRevision || head != source.Revision {
		return errors.New("source checkout revision differs from current evidence")
	}

	status, err := gitOutput(
		ctx,
		sourceRoot,
		"status",
		"--porcelain=v1",
		"--untracked-files=all",
	)
	if err != nil {
		return fmt.Errorf("inspect source checkout status: %w", err)
	}
	if status != "" {
		return errors.New("source checkout is dirty while verifying current evidence")
	}

	commitTimeValue, err := gitOutput(
		ctx,
		sourceRoot,
		"show",
		"-s",
		"--format=%cI",
		"HEAD",
	)
	if err != nil {
		return fmt.Errorf("read source checkout commit time: %w", err)
	}
	commitTime, err := time.Parse(time.RFC3339, commitTimeValue)
	if err != nil {
		return errors.New("source checkout commit time is invalid")
	}
	reportedTime, err := time.Parse(time.RFC3339, source.CommitTime)
	if err != nil || !commitTime.Equal(reportedTime) {
		return errors.New("source checkout commit time differs from current evidence")
	}
	return nil
}

func gitOutput(
	ctx context.Context,
	sourceRoot string,
	arguments ...string,
) (string, error) {
	commandArguments := append([]string{"-C", sourceRoot}, arguments...)
	command := exec.CommandContext(ctx, "git", commandArguments...)
	command.Env = append(
		os.Environ(),
		"GIT_OPTIONAL_LOCKS=0",
		"LC_ALL=C",
	)
	output, err := command.Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", errors.New("Git source verification timed out")
		}
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			detail := strings.TrimSpace(string(exitError.Stderr))
			if detail != "" {
				return "", fmt.Errorf("git failed: %s", detail)
			}
		}
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func normalizeArtifactCoordinates(
	coordinates ArtifactCoordinates,
) (ArtifactCoordinates, error) {
	sourceRoot, err := directDirectory(coordinates.SourceRoot, "source root")
	if err != nil {
		return ArtifactCoordinates{}, err
	}
	desktopApp, err := directDirectory(coordinates.DesktopApp, "Desktop App")
	if err != nil {
		return ArtifactCoordinates{}, err
	}
	if filepath.Ext(desktopApp) != ".app" {
		return ArtifactCoordinates{}, errors.New("Desktop App coordinate is not an App bundle")
	}
	acceptance, err := canonicalExecutable(
		coordinates.AcceptanceExecutable,
		"acceptance executable",
	)
	if err != nil {
		return ArtifactCoordinates{}, err
	}
	client, err := canonicalExecutable(
		coordinates.ClientEntrypoint,
		"client entrypoint",
	)
	if err != nil {
		return ArtifactCoordinates{}, err
	}
	return ArtifactCoordinates{
		SourceRoot:           sourceRoot,
		DesktopApp:           desktopApp,
		AcceptanceExecutable: acceptance,
		ClientEntrypoint:     client,
	}, nil
}

func directDirectory(path, label string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("%s path is invalid", label)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", label, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is not a direct directory", label)
	}
	return path, nil
}

func canonicalExecutable(path, label string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("%s path is invalid", label)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	info, err := os.Lstat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", label, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 ||
		info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is not an executable regular file", label)
	}
	return canonical, nil
}

func expectedArtifactPaths(
	coordinates ArtifactCoordinates,
) (map[string]string, error) {
	macOSDirectory := filepath.Join(coordinates.DesktopApp, "Contents", "MacOS")
	resourcesDirectory := filepath.Join(
		coordinates.DesktopApp,
		"Contents",
		"Resources",
	)
	paths := map[string]string{
		"acceptance":             coordinates.AcceptanceExecutable,
		"client-entrypoint":      coordinates.ClientEntrypoint,
		"daemon":                 filepath.Join(macOSDirectory, "vibermated"),
		"desktop-app-bundle":     coordinates.DesktopApp,
		"desktop-app-executable": filepath.Join(macOSDirectory, "vibermate-desktop"),
		"desktop-build-manifest": filepath.Join(resourcesDirectory, "vibermate-build-manifest.json"),
		"launcher":               filepath.Join(macOSDirectory, "vibermate"),
	}
	for _, role := range []string{"daemon", "desktop-app-executable", "launcher"} {
		path, err := directExecutable(paths[role], role)
		if err != nil {
			return nil, err
		}
		if path != paths[role] {
			return nil, fmt.Errorf("%s path changed while validating", role)
		}
	}
	return paths, nil
}

func directExecutable(path, label string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("%s path is invalid", label)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", label, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 ||
		info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is not a direct executable regular file", label)
	}
	return path, nil
}

func readDesktopBuildManifest(path string) (desktopBuildManifest, error) {
	payload, err := readBoundedRegularFile(path, maxDesktopBuildManifestBytes)
	if err != nil {
		return desktopBuildManifest{}, err
	}
	if err := rejectDuplicateJSONNames(payload); err != nil {
		return desktopBuildManifest{}, err
	}
	var manifest desktopBuildManifest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return desktopBuildManifest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return desktopBuildManifest{}, errors.New("Desktop build manifest has trailing data")
	}
	return manifest, nil
}

func readBoundedRegularFile(path string, limit int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 ||
		before.Size() <= 0 || before.Size() > limit {
		return nil, errors.New("file is not a bounded direct regular file")
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
	if !os.SameFile(before, afterOpen) {
		return nil, errors.New("file changed while opening")
	}
	payload, err := io.ReadAll(io.LimitReader(opened, limit+1))
	if err != nil {
		return nil, err
	}
	afterRead, err := opened.Stat()
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > limit ||
		!os.SameFile(afterOpen, afterRead) ||
		afterOpen.Size() != afterRead.Size() ||
		!afterOpen.ModTime().Equal(afterRead.ModTime()) ||
		int64(len(payload)) != afterRead.Size() {
		return nil, errors.New("file changed while reading")
	}
	return payload, nil
}

func verifyDesktopBuildManifest(
	manifest desktopBuildManifest,
	provenance *Provenance,
	artifacts map[string]ArtifactProvenance,
	expectedRevision string,
) error {
	if manifest.Schema != DesktopBuildManifestSchemaV2 {
		return errors.New("current v6 evidence requires Desktop build manifest v2")
	}
	if manifest.Source != provenance.Source ||
		manifest.Source.Revision != expectedRevision ||
		manifest.Source.Dirty {
		return errors.New("Desktop build manifest source differs from the report")
	}
	if manifest.Profiles.Desktop != provenance.Build.DesktopProfile ||
		manifest.Profiles.Sidecars != provenance.Build.SidecarProfile ||
		manifest.Profiles.Target != provenance.Build.Target {
		return errors.New("Desktop build manifest profiles differ from the report")
	}
	if manifest.Toolchains != provenance.Build.Toolchains {
		return errors.New("Desktop build manifest toolchains differ from the report")
	}
	if !maps.Equal(
		manifest.ConfigurationSHA256,
		provenance.Build.ConfigurationSHA256,
	) {
		return errors.New("Desktop build manifest configuration differs from the report")
	}
	if len(manifest.SidecarSHA256) != 2 ||
		manifest.SidecarSHA256["vibermated"] != artifacts["daemon"].SHA256 ||
		manifest.SidecarSHA256["vibermate"] != artifacts["launcher"].SHA256 {
		return errors.New("Desktop build manifest does not bind the verified sidecars")
	}
	return nil
}

func verifyConfigurationFiles(
	sourceRoot string,
	expected map[string]string,
) error {
	if len(expected) != len(requiredConfigurationDigests) {
		return errors.New("Desktop configuration digest set is incomplete")
	}
	for _, name := range requiredConfigurationDigests {
		path := filepath.Join(sourceRoot, filepath.FromSlash(name))
		evidence, err := DigestArtifact("configuration "+name, path)
		if err != nil {
			return err
		}
		if evidence.SHA256 != expected[name] {
			return fmt.Errorf("Desktop configuration differs from disk: %s", name)
		}
	}
	return nil
}
