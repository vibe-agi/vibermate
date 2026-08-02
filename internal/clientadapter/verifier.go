// Package clientadapter verifies narrowly scoped launch adaptations. Generic
// proxy variables need no client identity; adapter-specific trust and
// compatibility behavior require a complete, fixed release match.
package clientadapter

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

const (
	defaultMaxArtifactBytes = int64(512 << 20)
	maxCatalogEntries       = 256
	maxReleaseArtifacts     = 16
	maxArtifactPathBytes    = 4096
	releaseDigestDomain     = "vibermate:client-release:v1"
)

type Status string

const (
	StatusGeneric  Status = "generic"
	StatusVerified Status = "verified"
	StatusFailed   Status = "failed"
)

type CatalogRevision uint64

func (revision CatalogRevision) Valid() bool {
	return revision > 0 && uint64(revision) <= uint64(^uint64(0)>>1)
}

type AdapterRevision uint64

func (revision AdapterRevision) Valid() bool {
	return revision > 0 && uint64(revision) <= uint64(^uint64(0)>>1)
}

type LaunchRecipe string

const (
	LaunchGeneric      LaunchRecipe = "generic_proxy"
	LaunchNodeEnvProxy LaunchRecipe = "node_env_proxy"
	LaunchSSLCertFile  LaunchRecipe = "ssl_cert_file"
)

func (recipe LaunchRecipe) Valid() bool {
	switch recipe {
	case LaunchGeneric, LaunchNodeEnvProxy, LaunchSSLCertFile:
		return true
	default:
		return false
	}
}

func (recipe LaunchRecipe) RequiresRoot() bool {
	return recipe == LaunchNodeEnvProxy || recipe == LaunchSSLCertFile
}

type InstallShape string

const (
	InstallNativeSingleBinary    InstallShape = "native_single_binary"
	InstallNPMWrapperNativeChild InstallShape = "npm_wrapper_native_child"
)

func (shape InstallShape) Valid() bool {
	return shape == InstallNativeSingleBinary ||
		shape == InstallNPMWrapperNativeChild
}

type ArtifactRole string

const (
	ArtifactEntrypoint              ArtifactRole = "entrypoint"
	ArtifactMainPackageMetadata     ArtifactRole = "main_package_metadata"
	ArtifactPlatformPackageMetadata ArtifactRole = "platform_package_metadata"
	ArtifactNativeChild             ArtifactRole = "native_child"
)

func (role ArtifactRole) Valid() bool {
	switch role {
	case ArtifactEntrypoint,
		ArtifactMainPackageMetadata,
		ArtifactPlatformPackageMetadata,
		ArtifactNativeChild:
		return true
	default:
		return false
	}
}

// Feature is a closed bit set of compatibility behavior proven for an exact
// release. Generic clients never receive these features.
type Feature uint64

const (
	FeatureResponsesWebSocketHTTPFallback Feature = 1 << iota
)

const knownFeatures = FeatureResponsesWebSocketHTTPFallback

func (features Feature) valid() bool {
	return features&^knownFeatures == 0
}

type Artifact struct {
	Role         ArtifactRole
	RelativePath string
	SHA256       string
	MaxBytes     int64
}

// Release is one immutable catalog input. Artifact paths are relative to the
// canonical entrypoint directory; ArtifactRoot bounds every resolved path.
type Release struct {
	ID                      string
	Revision                AdapterRevision
	Version                 string
	OperatingSystem         string
	Architecture            string
	InstallShape            InstallShape
	InvocationLabel         string
	CanonicalEntrypointName string
	ArtifactRoot            string
	Artifacts               []Artifact
	LaunchRecipe            LaunchRecipe
	Features                Feature
}

// Catalog is an immutable, explicitly constructed release set. Its revision
// identifies the exact evidence set consulted for verified and generic
// detections.
type Catalog struct {
	revision CatalogRevision
	releases []Release
	signers  []Signer
}

func NewCatalog(
	revision CatalogRevision,
	releases []Release,
) (Catalog, error) {
	return NewCatalogWithSigners(revision, releases, nil)
}

// NewCatalogWithSigners builds a catalog that can also recognize a publisher
// for a build it does not carry. Passing no signers gives exactly the previous
// behaviour, so a caller that wants only exact releases keeps it.
func NewCatalogWithSigners(
	revision CatalogRevision,
	releases []Release,
	signers []Signer,
) (Catalog, error) {
	if !revision.Valid() ||
		len(releases) == 0 ||
		len(releases) > maxCatalogEntries {
		return Catalog{}, errors.New("client release catalog is invalid")
	}
	cloned := make([]Release, len(releases))
	for index, release := range releases {
		cloned[index] = cloneRelease(release)
		slices.SortFunc(
			cloned[index].Artifacts,
			func(left, right Artifact) int {
				leftKey := string(left.Role) + "\x00" + left.RelativePath
				rightKey := string(right.Role) + "\x00" + right.RelativePath
				return strings.Compare(leftKey, rightKey)
			},
		)
		if err := validateRelease(cloned[index]); err != nil {
			return Catalog{}, err
		}
	}
	slices.SortFunc(cloned, func(left, right Release) int {
		return strings.Compare(releaseSortKey(left), releaseSortKey(right))
	})
	for index := 1; index < len(cloned); index++ {
		if releaseSortKey(cloned[index-1]) == releaseSortKey(cloned[index]) {
			return Catalog{}, errors.New(
				"client release catalog contains a duplicate identity",
			)
		}
	}
	if len(signers) > maxCatalogEntries {
		return Catalog{}, errors.New("client signer catalog is too large")
	}
	clonedSigners := make([]Signer, len(signers))
	for index, signer := range signers {
		clonedSigners[index] = cloneSigner(signer)
		if err := validateSigner(clonedSigners[index]); err != nil {
			return Catalog{}, err
		}
	}
	slices.SortFunc(clonedSigners, func(left, right Signer) int {
		return strings.Compare(signerSortKey(left), signerSortKey(right))
	})
	for index := 1; index < len(clonedSigners); index++ {
		if signerSortKey(clonedSigners[index-1]) ==
			signerSortKey(clonedSigners[index]) {
			return Catalog{}, errors.New(
				"client signer catalog contains a duplicate identity",
			)
		}
	}
	return Catalog{
		revision: revision,
		releases: cloned,
		signers:  clonedSigners,
	}, nil
}

func signerSortKey(signer Signer) string {
	return strings.Join([]string{
		signer.ID,
		signer.OperatingSystem,
		signer.Architecture,
		signer.InvocationLabel,
	}, "\x00")
}

func (catalog Catalog) Revision() CatalogRevision {
	return catalog.revision
}

func (catalog Catalog) Valid() bool {
	return catalog.revision.Valid() &&
		len(catalog.releases) > 0 &&
		len(catalog.releases) <= maxCatalogEntries
}

type Evidence struct {
	ID              string          `json:"id"`
	Revision        AdapterRevision `json:"revision"`
	Version         string          `json:"version"`
	CatalogRevision CatalogRevision `json:"catalogRevision"`
	InstallShape    InstallShape    `json:"installShape"`
	ReleaseSHA256   string          `json:"releaseSha256"`
	LaunchRecipe    LaunchRecipe    `json:"launchRecipe"`
	Features        Feature         `json:"features"`
}

func (evidence Evidence) Validate() error {
	if strings.TrimSpace(evidence.ID) != evidence.ID ||
		evidence.ID == "" ||
		!evidence.Revision.Valid() ||
		strings.TrimSpace(evidence.Version) != evidence.Version ||
		evidence.Version == "" ||
		!evidence.CatalogRevision.Valid() ||
		!evidence.InstallShape.Valid() ||
		!evidence.LaunchRecipe.Valid() ||
		evidence.LaunchRecipe == LaunchGeneric ||
		!evidence.Features.valid() ||
		!validDigest(evidence.ReleaseSHA256) {
		return errors.New("client adapter evidence is invalid")
	}
	return nil
}

func (evidence Evidence) Supports(feature Feature) bool {
	return feature != 0 &&
		feature&^knownFeatures == 0 &&
		evidence.Features&feature == feature
}

// Recognition says whether the catalog knows this program at all, apart from
// whether this particular copy of it verified.
//
// A catalogued client at a version this build has no evidence for is launched
// without a trust root and cannot complete a decrypted handshake. A program
// nobody catalogued was never going to have one. The two look identical in the
// launch recipe and are entirely different to explain.
type Recognition string

const (
	// RecognitionUnknown: no catalogued release is invoked by this name.
	RecognitionUnknown Recognition = "unknown"
	// RecognitionUnverified: a catalogued release is invoked by this name, and
	// this copy matched none of them.
	RecognitionUnverified Recognition = "unverified"
	// RecognitionRecognized: no catalogued release matched this copy, but the
	// platform confirmed a catalogued publisher signed it and it has not been
	// modified. It says who made this, not which build it is.
	RecognitionRecognized Recognition = "recognized"
	// RecognitionVerified: this copy matched a catalogued release exactly.
	RecognitionVerified Recognition = "verified"
)

func (recognition Recognition) Valid() bool {
	switch recognition {
	case RecognitionUnknown,
		RecognitionUnverified,
		RecognitionRecognized,
		RecognitionVerified:
		return true
	default:
		return false
	}
}

type Detection struct {
	Status          Status          `json:"status"`
	Recognition     Recognition     `json:"recognition"`
	CatalogRevision CatalogRevision `json:"catalogRevision"`
	CanonicalPath   string          `json:"canonicalPath"`
	ExecutableLabel string          `json:"executableLabel"`
	Evidence        *Evidence       `json:"evidence,omitempty"`
	// Signer is set only for a recognized detection, and Evidence is set only
	// for a verified one. They are different claims and never both hold.
	Signer *SignerEvidence `json:"signer,omitempty"`
}

func (detection Detection) clone() Detection {
	cloned := detection
	if detection.Evidence != nil {
		evidence := *detection.Evidence
		cloned.Evidence = &evidence
	}
	if detection.Signer != nil {
		signer := *detection.Signer
		cloned.Signer = &signer
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

type ReleaseVerifier struct {
	revision CatalogRevision
	releases []Release
	signers  []Signer
}

func NewReleaseVerifier(
	catalog Catalog,
) (*ReleaseVerifier, error) {
	if !catalog.Valid() {
		return nil, errors.New("complete client release catalog is required")
	}
	normalized, err := NewCatalogWithSigners(
		catalog.revision,
		catalog.releases,
		catalog.signers,
	)
	if err != nil {
		return nil, err
	}
	releases := make([]Release, len(normalized.releases))
	for index, release := range normalized.releases {
		releases[index] = cloneRelease(release)
	}
	signers := make([]Signer, len(normalized.signers))
	for index, signer := range normalized.signers {
		signers[index] = cloneSigner(signer)
	}
	return &ReleaseVerifier{
		revision: normalized.revision,
		releases: releases,
		signers:  signers,
	}, nil
}

func (verifier *ReleaseVerifier) Verify(
	ctx context.Context,
	request Request,
) (Detection, error) {
	if verifier == nil || ctx == nil || !verifier.revision.Valid() {
		return Detection{}, errors.New(
			"ClientAdapter verifier and context are required",
		)
	}
	canonical, label, err := canonicalExecutable(request)
	if err != nil {
		return Detection{}, err
	}
	detection := Detection{
		Status:          StatusGeneric,
		Recognition:     RecognitionUnknown,
		CatalogRevision: verifier.revision,
		CanonicalPath:   canonical,
		ExecutableLabel: label,
	}
	candidates := verifier.releasesForLabel(label)
	if len(candidates) > 0 {
		detection.Recognition = RecognitionUnverified
	}
	var matched []Release
	for _, candidate := range candidates {
		ok, matchErr := verifyRelease(ctx, canonical, candidate)
		if matchErr != nil {
			detection.Status = StatusFailed
			return detection, matchErr
		}
		if ok {
			matched = append(matched, candidate)
		}
	}
	if len(matched) == 0 {
		// No catalogued build matched this copy. Before calling it generic,
		// ask the platform whether a catalogued publisher signed it: a user
		// who updated this morning is running a real client, not an unknown
		// program, and a release catalog frozen at our ship date can never
		// describe every build a user base is on.
		recognized, recognizeErr := verifier.recognizeSigner(ctx, canonical, label)
		if recognizeErr != nil {
			detection.Status = StatusFailed
			return detection, recognizeErr
		}
		if recognized != nil {
			detection.Recognition = RecognitionRecognized
			detection.Signer = recognized
			return detection.clone(), nil
		}
		return detection, nil
	}
	if len(matched) != 1 {
		detection.Status = StatusFailed
		return detection, errors.New(
			"client executable matches multiple catalog releases",
		)
	}
	release := matched[0]
	evidence := Evidence{
		ID:              release.ID,
		Revision:        release.Revision,
		Version:         release.Version,
		CatalogRevision: verifier.revision,
		InstallShape:    release.InstallShape,
		ReleaseSHA256:   releaseEvidenceDigest(release),
		LaunchRecipe:    release.LaunchRecipe,
		Features:        release.Features,
	}
	if err := evidence.Validate(); err != nil {
		detection.Status = StatusFailed
		return detection, err
	}
	detection.Status = StatusVerified
	detection.Recognition = RecognitionVerified
	detection.Evidence = &evidence
	return detection.clone(), nil
}

// recognizeSigner returns evidence when exactly one catalogued publisher
// claims this file. More than one is a catalog mistake rather than a stronger
// result, so it is refused: two publishers cannot both have signed it, and
// picking either would be inventing an answer.
func (verifier *ReleaseVerifier) recognizeSigner(
	ctx context.Context,
	canonical string,
	label string,
) (*SignerEvidence, error) {
	var found []SignerEvidence
	for _, signer := range verifier.signersForLabel(label) {
		signedPath, ok, err := verifySigner(ctx, canonical, signer)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		evidence := SignerEvidence{
			ID:              signer.ID,
			Revision:        signer.Revision,
			CatalogRevision: verifier.revision,
			InstallShape:    signer.InstallShape,
			LaunchRecipe:    signer.LaunchRecipe,
			SignedPath:      signedPath,
		}
		if err := evidence.Validate(); err != nil {
			return nil, err
		}
		found = append(found, evidence)
	}
	if len(found) == 0 {
		return nil, nil
	}
	if len(found) != 1 {
		return nil, errors.New(
			"client executable satisfies multiple catalogued signers",
		)
	}
	return &found[0], nil
}

func (verifier *ReleaseVerifier) releasesForLabel(label string) []Release {
	var matches []Release
	for _, release := range verifier.releases {
		if release.OperatingSystem != runtime.GOOS ||
			release.Architecture != runtime.GOARCH {
			continue
		}
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

func verifyRelease(
	ctx context.Context,
	canonicalEntrypoint string,
	release Release,
) (bool, error) {
	if release.CanonicalEntrypointName != "" &&
		filepath.Base(canonicalEntrypoint) !=
			release.CanonicalEntrypointName {
		return false, nil
	}
	entrypointDirectory := filepath.Dir(canonicalEntrypoint)
	lexicalRoot := filepath.Clean(filepath.Join(
		entrypointDirectory,
		release.ArtifactRoot,
	))
	resolvedRoot, err := filepath.EvalSymlinks(lexicalRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("resolve client release root: %w", err)
	}
	resolvedRoot, err = filepath.Abs(resolvedRoot)
	if err != nil {
		return false, fmt.Errorf("make client release root absolute: %w", err)
	}
	resolvedRoot = filepath.Clean(resolvedRoot)
	rootInfo, err := os.Stat(resolvedRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect client release root: %w", err)
	}
	if !rootInfo.IsDir() {
		return false, nil
	}
	if !pathWithin(resolvedRoot, canonicalEntrypoint) {
		return false, errors.New(
			"canonical client entrypoint escapes its release root",
		)
	}
	seen := make(map[string]struct{}, len(release.Artifacts))
	for _, artifact := range release.Artifacts {
		lexicalPath := canonicalEntrypoint
		if artifact.RelativePath != "" {
			lexicalPath = filepath.Clean(filepath.Join(
				entrypointDirectory,
				artifact.RelativePath,
			))
		}
		if !pathWithin(lexicalRoot, lexicalPath) {
			return false, errors.New(
				"client release artifact escapes its lexical root",
			)
		}
		resolvedPath, err := filepath.EvalSymlinks(lexicalPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			return false, fmt.Errorf(
				"resolve client release artifact: %w",
				err,
			)
		}
		resolvedPath, err = filepath.Abs(resolvedPath)
		if err != nil {
			return false, fmt.Errorf(
				"make client release artifact absolute: %w",
				err,
			)
		}
		resolvedPath = filepath.Clean(resolvedPath)
		if !pathWithin(resolvedRoot, resolvedPath) {
			return false, errors.New(
				"client release artifact resolves outside its release root",
			)
		}
		if _, duplicate := seen[resolvedPath]; duplicate {
			return false, errors.New(
				"client release artifacts resolve to the same file",
			)
		}
		seen[resolvedPath] = struct{}{}
		maxBytes := artifact.MaxBytes
		if maxBytes == 0 {
			maxBytes = defaultMaxArtifactBytes
		}
		actual, eligible, err := regularFileDigest(
			ctx,
			resolvedPath,
			maxBytes,
			artifact.Role == ArtifactEntrypoint ||
				artifact.Role == ArtifactNativeChild,
		)
		if err != nil {
			return false, fmt.Errorf(
				"hash client release artifact: %w",
				err,
			)
		}
		if !eligible || actual != artifact.SHA256 {
			return false, nil
		}
	}
	return true, nil
}

func canonicalExecutable(request Request) (string, string, error) {
	if len(request.Command) == 0 ||
		request.Command[0] == "" ||
		request.CWD == "" ||
		!filepath.IsAbs(request.CWD) ||
		filepath.Clean(request.CWD) != request.CWD ||
		request.ExecutablePath == "" ||
		!filepath.IsAbs(request.ExecutablePath) {
		return "", "", errors.New(
			"complete canonical launch request is required",
		)
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
		return "", "", errors.New(
			"executable path must resolve to a regular file",
		)
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
			return "", "", fmt.Errorf(
				"resolve explicit command path: %w",
				err,
			)
		}
		commandPath, err = filepath.Abs(commandPath)
		if err != nil {
			return "", "", err
		}
		if filepath.Clean(commandPath) != canonical {
			return "", "", errors.New(
				"explicit command path differs from executable path",
			)
		}
	}
	return canonical, invocationLabel, nil
}

func regularFileDigest(
	ctx context.Context,
	path string,
	maxBytes int64,
	requireExecutable bool,
) (string, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() ||
		info.Size() <= 0 ||
		info.Size() > maxBytes {
		return "", false, nil
	}
	if requireExecutable &&
		runtime.GOOS != "windows" &&
		info.Mode().Perm()&0o111 == 0 {
		return "", false, nil
	}
	hash := sha256.New()
	reader := bufio.NewReaderSize(file, 64<<10)
	buffer := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		count, readErr := reader.Read(buffer)
		if count > 0 {
			total += int64(count)
			if total > maxBytes {
				return "", false, nil
			}
			if _, err := hash.Write(buffer[:count]); err != nil {
				return "", false, err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", false, readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), true, nil
}

func pathWithin(root string, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative == "." ||
		(relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func validateRelease(release Release) error {
	if strings.TrimSpace(release.ID) != release.ID ||
		release.ID == "" ||
		!release.Revision.Valid() ||
		strings.TrimSpace(release.Version) != release.Version ||
		release.Version == "" ||
		release.OperatingSystem == "" ||
		release.Architecture == "" ||
		!release.InstallShape.Valid() ||
		strings.TrimSpace(release.InvocationLabel) !=
			release.InvocationLabel ||
		release.InvocationLabel == "" ||
		filepath.Base(release.InvocationLabel) != release.InvocationLabel ||
		(release.CanonicalEntrypointName != "" &&
			filepath.Base(release.CanonicalEntrypointName) !=
				release.CanonicalEntrypointName) ||
		!validArtifactRoot(release.ArtifactRoot) ||
		!release.LaunchRecipe.Valid() ||
		release.LaunchRecipe == LaunchGeneric ||
		!release.Features.valid() ||
		len(release.Artifacts) == 0 ||
		len(release.Artifacts) > maxReleaseArtifacts {
		return errors.New("fixed client release metadata is invalid")
	}
	entrypoints := 0
	roles := make(map[ArtifactRole]int)
	paths := make(map[string]struct{})
	for _, artifact := range release.Artifacts {
		if !artifact.Role.Valid() ||
			len(artifact.RelativePath) > maxArtifactPathBytes ||
			(artifact.RelativePath != "" &&
				(filepath.IsAbs(artifact.RelativePath) ||
					filepath.Clean(artifact.RelativePath) !=
						artifact.RelativePath)) ||
			artifact.MaxBytes < 0 ||
			artifact.MaxBytes > defaultMaxArtifactBytes ||
			!validDigest(artifact.SHA256) {
			return errors.New("fixed client release artifact is invalid")
		}
		if _, duplicate := paths[artifact.RelativePath]; duplicate {
			return errors.New(
				"fixed client release artifact path is duplicated",
			)
		}
		paths[artifact.RelativePath] = struct{}{}
		roles[artifact.Role]++
		if artifact.Role == ArtifactEntrypoint {
			entrypoints++
			if artifact.RelativePath != "" {
				return errors.New(
					"fixed client entrypoint artifact path is invalid",
				)
			}
		} else if artifact.RelativePath == "" {
			return errors.New(
				"fixed client companion artifact path is empty",
			)
		}
	}
	if entrypoints != 1 {
		return errors.New(
			"fixed client release needs one entrypoint artifact",
		)
	}
	switch release.InstallShape {
	case InstallNativeSingleBinary:
		if len(release.Artifacts) != 1 {
			return errors.New(
				"native client release must contain one artifact",
			)
		}
	case InstallNPMWrapperNativeChild:
		if roles[ArtifactMainPackageMetadata] != 1 ||
			roles[ArtifactPlatformPackageMetadata] != 1 ||
			roles[ArtifactNativeChild] != 1 {
			return errors.New(
				"npm client release companion artifacts are incomplete",
			)
		}
	}
	return nil
}

func validArtifactRoot(root string) bool {
	if root == "" ||
		len(root) > maxArtifactPathBytes ||
		filepath.IsAbs(root) ||
		filepath.Clean(root) != root {
		return false
	}
	parts := strings.Split(filepath.ToSlash(root), "/")
	parents := 0
	for _, part := range parts {
		if part == ".." {
			parents++
		}
	}
	return parents <= 2
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil &&
		len(decoded) == sha256.Size &&
		strings.ToLower(value) == value
}

func cloneRelease(release Release) Release {
	cloned := release
	cloned.Artifacts = slices.Clone(release.Artifacts)
	return cloned
}

func releaseSortKey(release Release) string {
	var builder strings.Builder
	builder.WriteString(release.InvocationLabel)
	builder.WriteByte(0)
	builder.WriteString(release.OperatingSystem)
	builder.WriteByte(0)
	builder.WriteString(release.Architecture)
	builder.WriteByte(0)
	builder.WriteString(release.CanonicalEntrypointName)
	builder.WriteByte(0)
	builder.WriteString(release.ID)
	builder.WriteByte(0)
	builder.WriteString(strconv.FormatUint(uint64(release.Revision), 10))
	builder.WriteByte(0)
	builder.WriteString(release.Version)
	builder.WriteByte(0)
	builder.WriteString(string(release.InstallShape))
	builder.WriteByte(0)
	builder.WriteString(release.ArtifactRoot)
	for _, artifact := range release.Artifacts {
		builder.WriteByte(0)
		builder.WriteString(string(artifact.Role))
		builder.WriteByte(0)
		builder.WriteString(artifact.RelativePath)
		builder.WriteByte(0)
		builder.WriteString(artifact.SHA256)
	}
	return builder.String()
}

func releaseEvidenceDigest(release Release) string {
	hash := sha256.New()
	writeDigestField(hash, releaseDigestDomain)
	writeDigestField(hash, release.ID)
	writeDigestUint(hash, uint64(release.Revision))
	writeDigestField(hash, release.Version)
	writeDigestField(hash, release.OperatingSystem)
	writeDigestField(hash, release.Architecture)
	writeDigestField(hash, string(release.InstallShape))
	writeDigestField(hash, release.InvocationLabel)
	writeDigestField(hash, release.CanonicalEntrypointName)
	writeDigestField(hash, release.ArtifactRoot)
	writeDigestField(hash, string(release.LaunchRecipe))
	writeDigestUint(hash, uint64(release.Features))
	for _, artifact := range release.Artifacts {
		writeDigestField(hash, string(artifact.Role))
		writeDigestField(hash, artifact.RelativePath)
		writeDigestField(hash, artifact.SHA256)
		writeDigestUint(hash, uint64(artifact.MaxBytes))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func writeDigestField(writer io.Writer, value string) {
	writeDigestUint(writer, uint64(len(value)))
	_, _ = io.WriteString(writer, value)
}

func writeDigestUint(writer io.Writer, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = writer.Write(encoded[:])
}

// ClaudeCode221220DarwinARM64 returns one fixed compatibility release. Version
// output is never executed as part of verification.
func ClaudeCode221220DarwinARM64() Release {
	return Release{
		ID:              "claude-code",
		Revision:        1,
		Version:         "2.1.220",
		OperatingSystem: "darwin",
		Architecture:    "arm64",
		InstallShape:    InstallNativeSingleBinary,
		InvocationLabel: "claude",
		ArtifactRoot:    ".",
		Artifacts: []Artifact{{
			Role:   ArtifactEntrypoint,
			SHA256: "8addc857f3fe64d5a0368af9ee50321b50afb4a6918ba3ef018ab84f5dbbe081",
		}},
		LaunchRecipe: LaunchNodeEnvProxy,
	}
}

// CodexCLI01450DarwinARM64 returns the compound release whose HTTP fallback,
// Root input, and wire behavior were verified by the fixed client matrix.
func CodexCLI01450DarwinARM64() Release {
	return Release{
		ID:                      "codex-cli",
		Revision:                1,
		Version:                 "0.145.0",
		OperatingSystem:         "darwin",
		Architecture:            "arm64",
		InstallShape:            InstallNPMWrapperNativeChild,
		InvocationLabel:         "codex",
		CanonicalEntrypointName: "codex.js",
		ArtifactRoot:            "..",
		Artifacts: []Artifact{
			{
				Role:   ArtifactEntrypoint,
				SHA256: "134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477",
			},
			{
				Role:         ArtifactMainPackageMetadata,
				RelativePath: "../package.json",
				SHA256:       "ff896fd5e5444cfc645890b21273ad1c6b3e26e4e4ab0934de597a0f8db5aafb",
				MaxBytes:     1 << 20,
			},
			{
				Role:         ArtifactPlatformPackageMetadata,
				RelativePath: "../node_modules/@openai/codex-darwin-arm64/package.json",
				SHA256:       "da204207716d61f06a70d96dd66e9b6c0728a3bdf8f696f31026549d47667a98",
				MaxBytes:     1 << 20,
			},
			{
				Role:         ArtifactNativeChild,
				RelativePath: "../node_modules/@openai/codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex",
				SHA256:       "1da3f4e0e96028b8a771814293c3033dafd1971f943f6c7e79b0897fe705f590",
			},
		},
		LaunchRecipe: LaunchSSLCertFile,
		Features:     FeatureResponsesWebSocketHTTPFallback,
	}
}

// BuiltInCatalog returns the explicit release evidence set. Other executable
// versions remain valid generic clients rather than inheriting these recipes.
//
// NewReleaseVerifier performs the full validation at Host construction, so a
// programming error in these constants is reported instead of becoming an
// empty or partially active catalog.
func BuiltInCatalog() Catalog {
	return Catalog{
		revision: 1,
		releases: []Release{
			ClaudeCode221220DarwinARM64(),
			CodexCLI01450DarwinARM64(),
		},
		signers: []Signer{
			ClaudeCodeSignerDarwin(),
			CodexCLISignerDarwin(),
		},
	}
}
