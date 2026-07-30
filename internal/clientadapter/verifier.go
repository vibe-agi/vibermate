// Package clientadapter verifies narrowly scoped launch adaptations. Generic
// proxy variables need no client identity; adapter-specific trust variables
// require a verified executable and supported version.
package clientadapter

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

const (
	maxExecutableBytes = 1 << 30
)

type Status string

const (
	StatusGeneric  Status = "generic"
	StatusVerified Status = "verified"
)

type LaunchRecipe string

const (
	LaunchGeneric      LaunchRecipe = "generic_proxy"
	LaunchNodeEnvProxy LaunchRecipe = "node_env_proxy"
)

func (recipe LaunchRecipe) Valid() bool {
	return recipe == LaunchGeneric || recipe == LaunchNodeEnvProxy
}

func (recipe LaunchRecipe) RequiresRoot() bool {
	return recipe == LaunchNodeEnvProxy
}

type Evidence struct {
	ID               string       `json:"id"`
	Version          string       `json:"version"`
	ExecutableSHA256 string       `json:"executableSha256"`
	LaunchRecipe     LaunchRecipe `json:"launchRecipe"`
}

type Detection struct {
	Status          Status    `json:"status"`
	CanonicalPath   string    `json:"canonicalPath"`
	ExecutableLabel string    `json:"executableLabel"`
	Evidence        *Evidence `json:"evidence,omitempty"`
}

func (detection Detection) clone() Detection {
	cloned := detection
	if detection.Evidence != nil {
		evidence := *detection.Evidence
		cloned.Evidence = &evidence
	}
	return cloned
}

type Request struct {
	Command        []string
	CWD            string
	ExecutablePath string
}

type Verifier interface {
	Verify(context.Context, Request) (Detection, error)
}

// Release is one read-only adapter identity. A Host selects an explicit
// catalog; package loading never registers releases globally.
type Release struct {
	ID               string
	Version          string
	InvocationLabel  string
	ExecutableSHA256 string
	LaunchRecipe     LaunchRecipe
}

type M0Verifier struct {
	releases []Release
}

func NewM0Verifier(releases []Release) (*M0Verifier, error) {
	if len(releases) == 0 {
		return nil, errors.New("at least one fixed client release is required")
	}
	catalog := slices.Clone(releases)
	slices.SortFunc(catalog, func(left, right Release) int {
		leftKey := left.InvocationLabel + "\x00" + left.ExecutableSHA256
		rightKey := right.InvocationLabel + "\x00" + right.ExecutableSHA256
		return strings.Compare(leftKey, rightKey)
	})
	for index, release := range catalog {
		if err := validateRelease(release); err != nil {
			return nil, err
		}
		if index > 0 &&
			catalog[index-1].InvocationLabel == release.InvocationLabel &&
			catalog[index-1].ExecutableSHA256 == release.ExecutableSHA256 {
			return nil, errors.New("fixed client release catalog contains a duplicate identity")
		}
	}
	return &M0Verifier{releases: catalog}, nil
}

func (verifier *M0Verifier) Verify(
	ctx context.Context,
	request Request,
) (Detection, error) {
	if verifier == nil || ctx == nil {
		return Detection{}, errors.New("ClientAdapter verifier and context are required")
	}
	canonical, label, err := canonicalExecutable(request)
	if err != nil {
		return Detection{}, err
	}
	detection := Detection{
		Status:          StatusGeneric,
		CanonicalPath:   canonical,
		ExecutableLabel: label,
	}
	candidates := verifier.releasesForLabel(label)
	if len(candidates) == 0 {
		return detection, nil
	}
	digest, err := executableDigest(ctx, canonical)
	if err != nil {
		return Detection{}, err
	}
	var matched *Release
	for index := range candidates {
		if candidates[index].ExecutableSHA256 == digest {
			release := candidates[index]
			matched = &release
			break
		}
	}
	if matched == nil {
		return detection, nil
	}
	detection.Status = StatusVerified
	detection.Evidence = &Evidence{
		ID:               matched.ID,
		Version:          matched.Version,
		ExecutableSHA256: digest,
		LaunchRecipe:     matched.LaunchRecipe,
	}
	return detection.clone(), nil
}

func (verifier *M0Verifier) releasesForLabel(label string) []Release {
	var matches []Release
	for _, release := range verifier.releases {
		matched := release.InvocationLabel == label
		if runtime.GOOS == "windows" {
			matched = strings.EqualFold(release.InvocationLabel, label)
		}
		if matched {
			matches = append(matches, release)
		}
	}
	return matches
}

func canonicalExecutable(request Request) (string, string, error) {
	if len(request.Command) == 0 ||
		request.Command[0] == "" ||
		request.CWD == "" ||
		!filepath.IsAbs(request.CWD) ||
		filepath.Clean(request.CWD) != request.CWD ||
		request.ExecutablePath == "" ||
		!filepath.IsAbs(request.ExecutablePath) {
		return "", "", errors.New("complete canonical launch request is required")
	}
	invocationLabel := filepath.Base(request.ExecutablePath)
	commandLabel := filepath.Base(request.Command[0])
	matches := commandLabel == invocationLabel
	if runtime.GOOS == "windows" {
		matches = strings.EqualFold(commandLabel, invocationLabel)
	}
	if !matches {
		return "", "", errors.New("command and executable labels differ")
	}
	canonical, err := filepath.EvalSymlinks(request.ExecutablePath)
	if err != nil {
		return "", "", fmt.Errorf("resolve executable path: %w", err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", "", fmt.Errorf("make executable path absolute: %w", err)
	}
	canonical = filepath.Clean(canonical)
	info, err := os.Stat(canonical)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", errors.New("executable path must resolve to a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", "", errors.New("executable path is not executable")
	}
	if filepath.IsAbs(request.Command[0]) ||
		strings.ContainsRune(request.Command[0], filepath.Separator) {
		commandPath := request.Command[0]
		if !filepath.IsAbs(commandPath) {
			commandPath = filepath.Join(request.CWD, commandPath)
		}
		commandPath, err = filepath.EvalSymlinks(commandPath)
		if err != nil {
			return "", "", fmt.Errorf("resolve explicit command path: %w", err)
		}
		commandPath, err = filepath.Abs(commandPath)
		if err != nil {
			return "", "", err
		}
		if filepath.Clean(commandPath) != canonical {
			return "", "", errors.New("explicit command path differs from executable path")
		}
	}
	return canonical, invocationLabel, nil
}

func executableDigest(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() <= 0 || info.Size() > maxExecutableBytes {
		return "", errors.New("executable size is outside the verification limit")
	}
	hash := sha256.New()
	reader := bufio.NewReaderSize(file, 64<<10)
	buffer := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count, readErr := reader.Read(buffer)
		if count > 0 {
			total += int64(count)
			if total > maxExecutableBytes {
				return "", errors.New("executable exceeds the verification limit")
			}
			if _, err := hash.Write(buffer[:count]); err != nil {
				return "", err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validateRelease(release Release) error {
	if strings.TrimSpace(release.ID) != release.ID ||
		release.ID == "" ||
		strings.TrimSpace(release.Version) != release.Version ||
		release.Version == "" ||
		strings.TrimSpace(release.InvocationLabel) != release.InvocationLabel ||
		release.InvocationLabel == "" ||
		filepath.Base(release.InvocationLabel) != release.InvocationLabel ||
		!release.LaunchRecipe.Valid() {
		return errors.New("fixed client release metadata is invalid")
	}
	decoded, err := hex.DecodeString(release.ExecutableSHA256)
	if err != nil ||
		len(decoded) != sha256.Size ||
		strings.ToLower(release.ExecutableSHA256) != release.ExecutableSHA256 {
		return errors.New("fixed client release digest is invalid")
	}
	return nil
}

// ClaudeCode221220DarwinARM64 returns the fixed release identity used by the
// M0 acceptance matrix. The digest identifies the canonical executable bytes;
// version output is never executed as part of verification.
func ClaudeCode221220DarwinARM64() Release {
	return Release{
		ID:               "claude-code",
		Version:          "2.1.220",
		InvocationLabel:  "claude",
		ExecutableSHA256: "8addc857f3fe64d5a0368af9ee50321b50afb4a6918ba3ef018ab84f5dbbe081",
		LaunchRecipe:     LaunchNodeEnvProxy,
	}
}
