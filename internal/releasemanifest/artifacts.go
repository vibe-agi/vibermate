package releasemanifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

var ErrArtifactMismatch = errors.New("release artifact mismatch")

func VerifyArtifacts(rootPath string, manifest Manifest) error {
	return verifyArtifacts(rootPath, manifest, nil)
}

// VerifyArtifactsWithSpec verifies an exact artifact root that also contains
// the caller-validated input specification at specRelativePath.
func VerifyArtifactsWithSpec(
	rootPath string,
	manifest Manifest,
	specRelativePath string,
) error {
	return verifyArtifacts(rootPath, manifest, []string{specRelativePath})
}

func verifyArtifacts(
	rootPath string,
	manifest Manifest,
	additionalFiles []string,
) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	for _, relativePath := range additionalFiles {
		if err := ValidateArtifactPath(relativePath); err != nil {
			return fmt.Errorf(
				"%w: input spec path is invalid: %v",
				ErrArtifactMismatch,
				err,
			)
		}
	}
	if !filepath.IsAbs(rootPath) || filepath.Clean(rootPath) != rootPath {
		return fmt.Errorf("%w: artifact root must be a clean absolute path", ErrArtifactMismatch)
	}
	rootInfo, err := inspectArtifactRootPath(rootPath)
	if err != nil {
		return fmt.Errorf("%w: inspect artifact root: %v", ErrArtifactMismatch, err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return fmt.Errorf("%w: open artifact root: %v", ErrArtifactMismatch, err)
	}
	defer root.Close()
	openedRootInfo, err := root.Stat(".")
	if err != nil || !openedRootInfo.IsDir() || !os.SameFile(rootInfo, openedRootInfo) {
		return fmt.Errorf(
			"%w: artifact root changed while it was opened",
			ErrArtifactMismatch,
		)
	}
	inventoryPlan, err := buildArtifactInventoryPlan(manifest, additionalFiles)
	if err != nil {
		return fmt.Errorf("%w: artifact root inventory: %v", ErrArtifactMismatch, err)
	}
	initialInventory, err := captureArtifactInventory(root, inventoryPlan)
	if err != nil {
		return fmt.Errorf("%w: artifact root inventory: %v", ErrArtifactMismatch, err)
	}

	artifactsByRole := make(map[string]Artifact, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		artifactsByRole[artifact.Role] = artifact
	}
	expectations := capabilityEvidenceExpectations(manifest)

	verifiedMetadata := make(map[string][]byte)
	for _, artifact := range manifest.Artifacts {
		metadata, err := verifyArtifact(
			root,
			artifact,
			manifest,
			artifactsByRole,
			expectations,
		)
		if err != nil {
			return err
		}
		if metadata != nil {
			verifiedMetadata[artifact.Role] = metadata
		}
	}
	if err := verifyUnsignedPayload(
		root,
		manifest,
		artifactsByRole,
		verifiedMetadata[ArtifactRoleAppTreeLedger],
		verifiedMetadata[ArtifactRoleDesktopBuildManifest],
	); err != nil {
		return err
	}
	finalInventory, err := captureArtifactInventory(root, inventoryPlan)
	if err != nil {
		return fmt.Errorf("%w: artifact root inventory: %v", ErrArtifactMismatch, err)
	}
	if !sameArtifactInventory(initialInventory, finalInventory) {
		return fmt.Errorf(
			"%w: artifact root identity or inventory changed during verification",
			ErrArtifactMismatch,
		)
	}
	finalRootInfo, err := inspectArtifactRootPath(rootPath)
	if err != nil || !os.SameFile(openedRootInfo, finalRootInfo) {
		return fmt.Errorf(
			"%w: artifact root path changed during verification",
			ErrArtifactMismatch,
		)
	}
	return nil
}

type artifactInventoryEntryKind uint8

const (
	artifactInventoryFile artifactInventoryEntryKind = iota + 1
	artifactInventoryDirectory
	artifactInventoryOpaqueDirectory
)

type artifactInventoryPlan map[string]map[string]artifactInventoryEntryKind

type artifactInventorySnapshot struct {
	directories map[string]artifactInventoryDirectorySnapshot
}

type artifactInventoryDirectorySnapshot struct {
	info         os.FileInfo
	pathSnapshot []os.FileInfo
	entries      []payloadDirectoryEntry
	entryInfos   map[string]os.FileInfo
}

func buildArtifactInventoryPlan(
	manifest Manifest,
	additionalFiles []string,
) (artifactInventoryPlan, error) {
	plan := artifactInventoryPlan{
		".": make(map[string]artifactInventoryEntryKind),
	}
	if err := addArtifactInventoryEntry(
		plan,
		".",
		UnsignedPayloadRoot,
		artifactInventoryOpaqueDirectory,
	); err != nil {
		return nil, err
	}
	for _, artifact := range manifest.Artifacts {
		if err := addArtifactInventoryFile(plan, artifact.Path); err != nil {
			return nil, fmt.Errorf("declared artifact %q: %w", artifact.Path, err)
		}
	}
	for _, relativePath := range additionalFiles {
		if err := addArtifactInventoryFile(plan, relativePath); err != nil {
			return nil, fmt.Errorf("input spec %q: %w", relativePath, err)
		}
	}
	return plan, nil
}

func addArtifactInventoryFile(
	plan artifactInventoryPlan,
	relativePath string,
) error {
	segments := strings.Split(relativePath, "/")
	parent := "."
	for index, segment := range segments {
		kind := artifactInventoryDirectory
		if index == len(segments)-1 {
			kind = artifactInventoryFile
		}
		if err := addArtifactInventoryEntry(plan, parent, segment, kind); err != nil {
			return err
		}
		if kind == artifactInventoryDirectory {
			parent = artifactInventoryChildPath(parent, segment)
			if _, exists := plan[parent]; !exists {
				plan[parent] = make(map[string]artifactInventoryEntryKind)
			}
		}
	}
	return nil
}

func addArtifactInventoryEntry(
	plan artifactInventoryPlan,
	parent, name string,
	kind artifactInventoryEntryKind,
) error {
	children, exists := plan[parent]
	if !exists {
		return fmt.Errorf("parent directory %q is not planned", parent)
	}
	if existing, duplicate := children[name]; duplicate {
		if existing != kind {
			return fmt.Errorf("path component %q has incompatible uses", name)
		}
		if kind != artifactInventoryDirectory {
			return fmt.Errorf("path component %q is declared more than once", name)
		}
		return nil
	}
	children[name] = kind
	return nil
}

func captureArtifactInventory(
	root *os.Root,
	plan artifactInventoryPlan,
) (artifactInventorySnapshot, error) {
	snapshot := artifactInventorySnapshot{
		directories: make(
			map[string]artifactInventoryDirectorySnapshot,
			len(plan),
		),
	}
	var captureDirectory func(string) error
	captureDirectory = func(relativePath string) error {
		expected, exists := plan[relativePath]
		if !exists {
			return fmt.Errorf("directory %q has no inventory plan", relativePath)
		}
		initial, err := captureArtifactInventoryDirectory(
			root,
			relativePath,
			expected,
		)
		if err != nil {
			return err
		}
		snapshot.directories[relativePath] = initial

		directories := make([]string, 0)
		for name, kind := range expected {
			if kind == artifactInventoryDirectory {
				directories = append(directories, name)
			}
		}
		sort.Strings(directories)
		for _, name := range directories {
			if err := captureDirectory(
				artifactInventoryChildPath(relativePath, name),
			); err != nil {
				return err
			}
		}

		final, err := captureArtifactInventoryDirectory(
			root,
			relativePath,
			expected,
		)
		if err != nil {
			return err
		}
		if !sameArtifactInventoryDirectory(initial, final) {
			return fmt.Errorf(
				"directory %q identity or inventory changed during traversal",
				relativePath,
			)
		}
		return nil
	}
	if err := captureDirectory("."); err != nil {
		return artifactInventorySnapshot{}, err
	}
	return snapshot, nil
}

func captureArtifactInventoryDirectory(
	root *os.Root,
	relativePath string,
	expected map[string]artifactInventoryEntryKind,
) (artifactInventoryDirectorySnapshot, error) {
	osPath := filepath.FromSlash(relativePath)
	pathSnapshot, err := inspectArtifactPath(root, osPath)
	if err != nil {
		return artifactInventoryDirectorySnapshot{}, fmt.Errorf(
			"inspect directory %q: %w",
			relativePath,
			err,
		)
	}
	pathInfo := pathSnapshot[len(pathSnapshot)-1]
	if !pathInfo.IsDir() {
		return artifactInventoryDirectorySnapshot{}, fmt.Errorf(
			"path %q is not a directory",
			relativePath,
		)
	}
	directory, err := root.Open(osPath)
	if err != nil {
		return artifactInventoryDirectorySnapshot{}, fmt.Errorf(
			"open directory %q: %w",
			relativePath,
			err,
		)
	}
	defer directory.Close()
	openedInfo, err := directory.Stat()
	if err != nil || !openedInfo.IsDir() || !os.SameFile(pathInfo, openedInfo) {
		return artifactInventoryDirectorySnapshot{}, fmt.Errorf(
			"directory %q changed while it was opened",
			relativePath,
		)
	}
	entries, err := readArtifactInventoryDirectory(directory, len(expected))
	if err != nil {
		return artifactInventoryDirectorySnapshot{}, fmt.Errorf(
			"read directory %q: %w",
			relativePath,
			err,
		)
	}
	if err := compareArtifactInventoryDirectoryEntries(expected, entries); err != nil {
		return artifactInventoryDirectorySnapshot{}, fmt.Errorf(
			"directory %q entry set: %w",
			relativePath,
			err,
		)
	}
	entryInfos, err := captureArtifactInventoryEntryInfos(
		root,
		relativePath,
		expected,
	)
	if err != nil {
		return artifactInventoryDirectorySnapshot{}, err
	}
	readInfo, err := directory.Stat()
	if err != nil || !readInfo.IsDir() || !os.SameFile(openedInfo, readInfo) ||
		openedInfo.Mode() != readInfo.Mode() {
		return artifactInventoryDirectorySnapshot{}, fmt.Errorf(
			"directory %q changed while its inventory was read",
			relativePath,
		)
	}
	finalPathSnapshot, err := inspectArtifactPath(root, osPath)
	if err != nil || !sameArtifactPath(pathSnapshot, finalPathSnapshot) ||
		!os.SameFile(openedInfo, finalPathSnapshot[len(finalPathSnapshot)-1]) {
		return artifactInventoryDirectorySnapshot{}, fmt.Errorf(
			"directory %q path changed while its inventory was read",
			relativePath,
		)
	}
	return artifactInventoryDirectorySnapshot{
		info:         openedInfo,
		pathSnapshot: pathSnapshot,
		entries:      entries,
		entryInfos:   entryInfos,
	}, nil
}

func captureArtifactInventoryEntryInfos(
	root *os.Root,
	parent string,
	expected map[string]artifactInventoryEntryKind,
) (map[string]os.FileInfo, error) {
	infos := make(map[string]os.FileInfo, len(expected))
	for name, kind := range expected {
		relativePath := artifactInventoryChildPath(parent, name)
		info, err := root.Lstat(filepath.FromSlash(relativePath))
		if err != nil {
			return nil, fmt.Errorf("inspect inventory entry %q: %w", relativePath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("inventory entry %q is a symbolic link", relativePath)
		}
		switch kind {
		case artifactInventoryFile:
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf(
					"declared artifact %q is not a regular file",
					relativePath,
				)
			}
		case artifactInventoryDirectory, artifactInventoryOpaqueDirectory:
			if !info.IsDir() {
				return nil, fmt.Errorf(
					"inventory entry %q is not a directory",
					relativePath,
				)
			}
		default:
			return nil, fmt.Errorf(
				"inventory entry %q has an unsupported expected type",
				relativePath,
			)
		}
		infos[name] = info
	}
	return infos, nil
}

func readArtifactInventoryDirectory(
	directory *os.File,
	limit int,
) ([]payloadDirectoryEntry, error) {
	entries := make([]payloadDirectoryEntry, 0, limit)
	seen := make(map[string]struct{}, limit)
	for {
		batch, err := directory.ReadDir(256)
		for _, entry := range batch {
			if len(entries) >= limit {
				return nil, fmt.Errorf(
					"directory exceeds the expected %d-entry inventory",
					limit,
				)
			}
			if _, duplicate := seen[entry.Name()]; duplicate {
				return nil, fmt.Errorf(
					"directory contains name %q more than once",
					entry.Name(),
				)
			}
			seen[entry.Name()] = struct{}{}
			entries = append(entries, payloadDirectoryEntry{
				name:     entry.Name(),
				typeMode: entry.Type(),
			})
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].name < entries[right].name
	})
	return entries, nil
}

func compareArtifactInventoryDirectoryEntries(
	expected map[string]artifactInventoryEntryKind,
	actual []payloadDirectoryEntry,
) error {
	if len(expected) != len(actual) {
		return fmt.Errorf("has %d entries, expected %d", len(actual), len(expected))
	}
	expectedNames := make([]string, 0, len(expected))
	for name := range expected {
		expectedNames = append(expectedNames, name)
	}
	sort.Strings(expectedNames)
	for index, name := range expectedNames {
		if actual[index].name != name {
			return fmt.Errorf(
				"contains %q where %q was expected",
				actual[index].name,
				name,
			)
		}
		if actual[index].typeMode&os.ModeSymlink != 0 {
			return fmt.Errorf("contains symbolic link %q", actual[index].name)
		}
	}
	return nil
}

func sameArtifactInventory(
	left, right artifactInventorySnapshot,
) bool {
	if len(left.directories) != len(right.directories) {
		return false
	}
	for relativePath, leftDirectory := range left.directories {
		rightDirectory, exists := right.directories[relativePath]
		if !exists || !sameArtifactInventoryDirectory(leftDirectory, rightDirectory) {
			return false
		}
	}
	return true
}

func sameArtifactInventoryDirectory(
	left, right artifactInventoryDirectorySnapshot,
) bool {
	if !sameArtifactInventoryInfo(left.info, right.info) ||
		!sameArtifactPath(left.pathSnapshot, right.pathSnapshot) ||
		!samePayloadDirectoryEntries(left.entries, right.entries) ||
		len(left.entryInfos) != len(right.entryInfos) {
		return false
	}
	for name, leftInfo := range left.entryInfos {
		rightInfo, exists := right.entryInfos[name]
		if !exists || !sameArtifactInventoryInfo(leftInfo, rightInfo) {
			return false
		}
	}
	return true
}

func sameArtifactInventoryInfo(left, right os.FileInfo) bool {
	return os.SameFile(left, right) &&
		left.Mode() == right.Mode() &&
		left.Size() == right.Size() &&
		left.ModTime().Equal(right.ModTime())
}

func artifactInventoryChildPath(parent, name string) string {
	if parent == "." {
		return name
	}
	return parent + "/" + name
}

func verifyArtifact(
	root *os.Root,
	artifact Artifact,
	manifest Manifest,
	artifactsByRole map[string]Artifact,
	expectations map[string][]capabilityEvidenceExpectation,
) ([]byte, error) {
	metadataLimit, isMetadata := metadataByteLimit(artifact)
	if isMetadata && artifact.Size > metadataLimit {
		return nil, artifactMismatch(
			artifact,
			"metadata exceeds the %d-byte limit",
			metadataLimit,
		)
	}
	relativePath := filepath.FromSlash(artifact.Path)
	pathSnapshot, err := inspectArtifactPath(root, relativePath)
	if err != nil {
		return nil, artifactMismatch(artifact, "%v", err)
	}
	pathInfo := pathSnapshot[len(pathSnapshot)-1]
	if pathInfo == nil || !pathInfo.Mode().IsRegular() {
		return nil, artifactMismatch(artifact, "artifact is not a regular file")
	}
	if pathInfo.Size() != artifact.Size {
		return nil, artifactMismatch(
			artifact,
			"size is %d bytes, expected %d",
			pathInfo.Size(),
			artifact.Size,
		)
	}

	file, err := root.Open(relativePath)
	if err != nil {
		return nil, artifactMismatch(artifact, "open file: %v", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, artifactMismatch(artifact, "inspect open file: %v", err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
		return nil, artifactMismatch(artifact, "artifact changed while it was opened")
	}

	hash := sha256.New()
	var metadata bytes.Buffer
	writer := io.Writer(hash)
	if isMetadata {
		metadata.Grow(int(artifact.Size))
		writer = io.MultiWriter(hash, &metadata)
	}
	read, err := io.Copy(writer, io.LimitReader(file, artifact.Size+1))
	if err != nil {
		return nil, artifactMismatch(artifact, "read file: %v", err)
	}
	if read != artifact.Size {
		return nil, artifactMismatch(artifact, "size changed while the artifact was read")
	}
	readInfo, err := file.Stat()
	if err != nil || readInfo.Size() != artifact.Size || !os.SameFile(openedInfo, readInfo) {
		return nil, artifactMismatch(artifact, "artifact changed while it was read")
	}
	finalPathSnapshot, err := inspectArtifactPath(root, relativePath)
	if err != nil || !sameArtifactPath(pathSnapshot, finalPathSnapshot) ||
		!os.SameFile(openedInfo, finalPathSnapshot[len(finalPathSnapshot)-1]) {
		return nil, artifactMismatch(artifact, "artifact path changed while it was read")
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if digest != artifact.SHA256 {
		return nil, artifactMismatch(artifact, "SHA-256 digest does not match")
	}
	if isMetadata {
		if err := validateArtifactMetadata(
			metadata.Bytes(),
			artifact,
			manifest,
			artifactsByRole,
			expectations,
		); err != nil {
			return nil, artifactMismatch(artifact, "metadata is invalid: %v", err)
		}
		return append([]byte(nil), metadata.Bytes()...), nil
	}
	return nil, nil
}

type unsignedPayloadVerifier struct {
	root                 *os.Root
	entries              map[string]appTreeLedgerEntry
	children             map[string][]string
	desktopBuild         desktopBuildDocument
	desktopPayload       []byte
	desktopBuildArtifact Artifact
	seen                 map[string]struct{}
}

type payloadDirectoryEntry struct {
	name     string
	typeMode os.FileMode
}

type payloadDirectoryTraversal struct {
	file         *os.File
	info         os.FileInfo
	pathSnapshot []os.FileInfo
	entries      []payloadDirectoryEntry
}

func verifyUnsignedPayload(
	root *os.Root,
	manifest Manifest,
	artifactsByRole map[string]Artifact,
	ledgerPayload []byte,
	desktopPayload []byte,
) error {
	ledgerArtifact, ledgerExists := artifactsByRole[ArtifactRoleAppTreeLedger]
	desktopArtifact, desktopExists := artifactsByRole[ArtifactRoleDesktopBuildManifest]
	if !ledgerExists || !desktopExists || ledgerPayload == nil || desktopPayload == nil {
		return fmt.Errorf(
			"%w: verified application tree ledger or Desktop build manifest is unavailable",
			ErrArtifactMismatch,
		)
	}
	var ledger appTreeLedgerDocument
	if err := decodeClosedArtifactJSON(ledgerPayload, &ledger); err != nil {
		return artifactMismatch(
			ledgerArtifact,
			"cannot decode verified application tree ledger: %v",
			err,
		)
	}
	var desktopBuild desktopBuildDocument
	if err := decodeClosedArtifactJSON(desktopPayload, &desktopBuild); err != nil {
		return artifactMismatch(
			ledgerArtifact,
			"cannot decode verified Desktop build manifest: %v",
			err,
		)
	}
	for _, artifact := range manifest.Artifacts {
		if artifact.Path == ledger.Root ||
			strings.HasPrefix(artifact.Path, ledger.Root+"/") {
			return artifactMismatch(
				ledgerArtifact,
				"declared artifact %q is inside the unsigned payload it is meant to bind",
				artifact.Path,
			)
		}
	}

	entries := make(map[string]appTreeLedgerEntry, len(ledger.Entries))
	children := make(map[string][]string)
	for _, entry := range ledger.Entries {
		entries[entry.Path] = entry
		if entry.Path != "." {
			parent := path.Dir(entry.Path)
			children[parent] = append(children[parent], entry.Path)
		}
	}
	for parent := range children {
		sort.Strings(children[parent])
	}
	manifestEntry := entries["vibermate-build-manifest.json"]
	if manifestEntry.SHA256 == nil || manifestEntry.Size == nil ||
		*manifestEntry.SHA256 != desktopArtifact.SHA256 ||
		*manifestEntry.Size != desktopArtifact.Size {
		return artifactMismatch(
			ledgerArtifact,
			"embedded Desktop build manifest entry does not bind the declared Desktop build manifest artifact",
		)
	}
	for _, sidecar := range desktopSidecarNames {
		entry := entries[sidecar]
		if entry.SHA256 == nil || *entry.SHA256 != desktopBuild.SidecarSHA256[sidecar] {
			return artifactMismatch(
				ledgerArtifact,
				"sidecar %q entry does not bind desktop build sidecarSHA256",
				sidecar,
			)
		}
	}

	rootPath := filepath.FromSlash(ledger.Root)
	rootPathSnapshot, err := inspectArtifactPath(root, rootPath)
	if err != nil {
		return artifactMismatch(
			ledgerArtifact,
			"inspect unsigned payload root: %v",
			err,
		)
	}
	rootPathInfo := rootPathSnapshot[len(rootPathSnapshot)-1]
	if !rootPathInfo.IsDir() {
		return artifactMismatch(
			ledgerArtifact,
			"unsigned payload root is not a directory",
		)
	}
	payloadRoot, err := root.OpenRoot(rootPath)
	if err != nil {
		return artifactMismatch(
			ledgerArtifact,
			"open unsigned payload root: %v",
			err,
		)
	}
	defer payloadRoot.Close()
	openedRootInfo, err := payloadRoot.Stat(".")
	if err != nil || !openedRootInfo.IsDir() || !os.SameFile(rootPathInfo, openedRootInfo) {
		return artifactMismatch(
			ledgerArtifact,
			"unsigned payload root changed while it was opened",
		)
	}

	verifier := unsignedPayloadVerifier{
		root:                 payloadRoot,
		entries:              entries,
		children:             children,
		desktopBuild:         desktopBuild,
		desktopPayload:       desktopPayload,
		desktopBuildArtifact: desktopArtifact,
		seen:                 make(map[string]struct{}, len(entries)),
	}
	if err := verifier.verifyDirectory("."); err != nil {
		return artifactMismatch(
			ledgerArtifact,
			"unsigned payload tree does not match the ledger: %v",
			err,
		)
	}
	if len(verifier.seen) != len(entries) {
		return artifactMismatch(
			ledgerArtifact,
			"unsigned payload tree is missing one or more ledger entries",
		)
	}
	finalOpenedRootInfo, err := payloadRoot.Stat(".")
	if err != nil || !os.SameFile(openedRootInfo, finalOpenedRootInfo) ||
		payloadLedgerMode(finalOpenedRootInfo.Mode()) != *entries["."].Mode {
		return artifactMismatch(
			ledgerArtifact,
			"unsigned payload root changed during verification",
		)
	}
	finalRootPathSnapshot, err := inspectArtifactPath(root, rootPath)
	if err != nil || !sameArtifactPath(rootPathSnapshot, finalRootPathSnapshot) ||
		!os.SameFile(openedRootInfo, finalRootPathSnapshot[len(finalRootPathSnapshot)-1]) {
		return artifactMismatch(
			ledgerArtifact,
			"unsigned payload root path changed during verification",
		)
	}
	return nil
}

func (verifier *unsignedPayloadVerifier) verifyDirectory(relativePath string) error {
	entry, declared := verifier.entries[relativePath]
	if !declared || entry.Type != "directory" || entry.Mode == nil {
		return fmt.Errorf("directory %q is not declared as a directory", relativePath)
	}
	if _, duplicate := verifier.seen[relativePath]; duplicate {
		return fmt.Errorf("directory %q was visited more than once", relativePath)
	}
	verifier.seen[relativePath] = struct{}{}

	traversal, err := verifier.beginDirectory(relativePath)
	if err != nil {
		return err
	}
	defer traversal.file.Close()
	if payloadLedgerMode(traversal.info.Mode()) != *entry.Mode {
		return fmt.Errorf("directory %q mode does not match", relativePath)
	}
	expectedChildren := verifier.children[relativePath]
	if err := comparePayloadDirectoryEntries(expectedChildren, traversal.entries); err != nil {
		return fmt.Errorf("directory %q entry set: %w", relativePath, err)
	}
	for _, child := range expectedChildren {
		declaredChild := verifier.entries[child]
		switch declaredChild.Type {
		case "directory":
			if err := verifier.verifyDirectory(child); err != nil {
				return err
			}
		case "file":
			if err := verifier.verifyFile(child, declaredChild); err != nil {
				return err
			}
		default:
			return fmt.Errorf("path %q has unsupported type %q", child, declaredChild.Type)
		}
	}
	return verifier.finishDirectory(relativePath, traversal)
}

func (verifier *unsignedPayloadVerifier) beginDirectory(
	relativePath string,
) (payloadDirectoryTraversal, error) {
	osPath := filepath.FromSlash(relativePath)
	pathSnapshot, err := inspectArtifactPath(verifier.root, osPath)
	if err != nil {
		return payloadDirectoryTraversal{}, fmt.Errorf(
			"inspect directory %q: %w",
			relativePath,
			err,
		)
	}
	pathInfo := pathSnapshot[len(pathSnapshot)-1]
	if !pathInfo.IsDir() {
		return payloadDirectoryTraversal{}, fmt.Errorf(
			"path %q is not a directory",
			relativePath,
		)
	}
	directory, err := verifier.root.Open(osPath)
	if err != nil {
		return payloadDirectoryTraversal{}, fmt.Errorf(
			"open directory %q: %w",
			relativePath,
			err,
		)
	}
	openedInfo, err := directory.Stat()
	if err != nil || !openedInfo.IsDir() || !os.SameFile(pathInfo, openedInfo) {
		directory.Close()
		return payloadDirectoryTraversal{}, fmt.Errorf(
			"directory %q changed while it was opened",
			relativePath,
		)
	}
	entries, err := readPayloadDirectory(directory, maxAppTreeEntries)
	if err != nil {
		directory.Close()
		return payloadDirectoryTraversal{}, fmt.Errorf(
			"read directory %q: %w",
			relativePath,
			err,
		)
	}
	return payloadDirectoryTraversal{
		file:         directory,
		info:         openedInfo,
		pathSnapshot: pathSnapshot,
		entries:      entries,
	}, nil
}

func (verifier *unsignedPayloadVerifier) finishDirectory(
	relativePath string,
	initial payloadDirectoryTraversal,
) error {
	finalOpenInfo, err := initial.file.Stat()
	if err != nil || !finalOpenInfo.IsDir() || !os.SameFile(initial.info, finalOpenInfo) ||
		payloadLedgerMode(finalOpenInfo.Mode()) != payloadLedgerMode(initial.info.Mode()) {
		return fmt.Errorf("directory %q changed during traversal", relativePath)
	}
	final, err := verifier.beginDirectory(relativePath)
	if err != nil {
		return err
	}
	defer final.file.Close()
	if !sameArtifactPath(initial.pathSnapshot, final.pathSnapshot) ||
		!os.SameFile(initial.info, final.info) ||
		!samePayloadDirectoryEntries(initial.entries, final.entries) {
		return fmt.Errorf("directory %q identity or entry set changed during traversal", relativePath)
	}
	return nil
}

func (verifier *unsignedPayloadVerifier) verifyFile(
	relativePath string,
	entry appTreeLedgerEntry,
) error {
	if entry.Mode == nil || entry.Size == nil || entry.SHA256 == nil {
		return fmt.Errorf("file %q has an incomplete declaration", relativePath)
	}
	if _, duplicate := verifier.seen[relativePath]; duplicate {
		return fmt.Errorf("file %q was visited more than once", relativePath)
	}
	verifier.seen[relativePath] = struct{}{}
	osPath := filepath.FromSlash(relativePath)
	pathSnapshot, err := inspectArtifactPath(verifier.root, osPath)
	if err != nil {
		return fmt.Errorf("inspect file %q: %w", relativePath, err)
	}
	pathInfo := pathSnapshot[len(pathSnapshot)-1]
	if !pathInfo.Mode().IsRegular() {
		return fmt.Errorf("path %q is not a regular file", relativePath)
	}
	if pathInfo.Size() != *entry.Size || payloadLedgerMode(pathInfo.Mode()) != *entry.Mode {
		return fmt.Errorf("file %q size or mode does not match", relativePath)
	}
	file, err := verifier.root.Open(osPath)
	if err != nil {
		return fmt.Errorf("open file %q: %w", relativePath, err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
		return fmt.Errorf("file %q changed while it was opened", relativePath)
	}

	hash := sha256.New()
	var captured bytes.Buffer
	writer := io.Writer(hash)
	if relativePath == "vibermate-build-manifest.json" {
		if *entry.Size > maxDesktopBuildManifestBytes {
			return fmt.Errorf("embedded Desktop build manifest exceeds its byte limit")
		}
		captured.Grow(int(*entry.Size))
		writer = io.MultiWriter(hash, &captured)
	}
	read, err := io.Copy(writer, io.LimitReader(file, *entry.Size+1))
	if err != nil {
		return fmt.Errorf("read file %q: %w", relativePath, err)
	}
	if read != *entry.Size {
		return fmt.Errorf("file %q size changed while it was read", relativePath)
	}
	readInfo, err := file.Stat()
	if err != nil || !readInfo.Mode().IsRegular() ||
		readInfo.Size() != *entry.Size ||
		payloadLedgerMode(readInfo.Mode()) != *entry.Mode ||
		!os.SameFile(openedInfo, readInfo) {
		return fmt.Errorf("file %q identity, size, or mode changed while it was read", relativePath)
	}
	finalPathSnapshot, err := inspectArtifactPath(verifier.root, osPath)
	if err != nil || !sameArtifactPath(pathSnapshot, finalPathSnapshot) ||
		!os.SameFile(openedInfo, finalPathSnapshot[len(finalPathSnapshot)-1]) {
		return fmt.Errorf("file %q path changed while it was read", relativePath)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if digest != *entry.SHA256 {
		return fmt.Errorf("file %q SHA-256 digest does not match", relativePath)
	}
	switch relativePath {
	case "vibermate", "vibermated":
		if digest != verifier.desktopBuild.SidecarSHA256[relativePath] {
			return fmt.Errorf(
				"sidecar %q SHA-256 digest does not match desktop build sidecarSHA256",
				relativePath,
			)
		}
	case "vibermate-build-manifest.json":
		if digest != verifier.desktopBuildArtifact.SHA256 ||
			!bytes.Equal(captured.Bytes(), verifier.desktopPayload) {
			return fmt.Errorf(
				"embedded Desktop build manifest bytes do not match the declared artifact",
			)
		}
	}
	return nil
}

func readPayloadDirectory(
	directory *os.File,
	limit int,
) ([]payloadDirectoryEntry, error) {
	entries := make([]payloadDirectoryEntry, 0)
	seen := make(map[string]struct{})
	for {
		batch, err := directory.ReadDir(256)
		for _, entry := range batch {
			if len(entries) >= limit {
				return nil, fmt.Errorf("directory exceeds the %d-entry limit", limit)
			}
			if _, duplicate := seen[entry.Name()]; duplicate {
				return nil, fmt.Errorf("directory contains name %q more than once", entry.Name())
			}
			seen[entry.Name()] = struct{}{}
			entries = append(entries, payloadDirectoryEntry{
				name:     entry.Name(),
				typeMode: entry.Type(),
			})
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].name < entries[right].name
	})
	return entries, nil
}

func comparePayloadDirectoryEntries(
	expectedPaths []string,
	actual []payloadDirectoryEntry,
) error {
	if len(expectedPaths) != len(actual) {
		return fmt.Errorf("has %d entries, expected %d", len(actual), len(expectedPaths))
	}
	for index, expectedPath := range expectedPaths {
		if path.Base(expectedPath) != actual[index].name {
			return fmt.Errorf(
				"contains %q where %q was expected",
				actual[index].name,
				path.Base(expectedPath),
			)
		}
		if actual[index].typeMode&os.ModeSymlink != 0 {
			return fmt.Errorf("contains symbolic link %q", actual[index].name)
		}
	}
	return nil
}

func samePayloadDirectoryEntries(
	left, right []payloadDirectoryEntry,
) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func payloadLedgerMode(mode os.FileMode) uint32 {
	result := uint32(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		result |= 0o4000
	}
	if mode&os.ModeSetgid != 0 {
		result |= 0o2000
	}
	if mode&os.ModeSticky != 0 {
		result |= 0o1000
	}
	return result
}

func inspectArtifactRootPath(rootPath string) (os.FileInfo, error) {
	volume := filepath.VolumeName(rootPath)
	current := volume + string(filepath.Separator)
	if volume == "" {
		current = string(filepath.Separator)
	}
	info, err := os.Lstat(current)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("artifact root has a symlink or non-directory ancestor")
	}
	relative := strings.TrimPrefix(rootPath, current)
	if relative == "" {
		return info, nil
	}
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, segment)
		info, err = os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, errors.New("artifact root has a symlink or non-directory ancestor")
		}
	}
	return info, nil
}

func inspectArtifactPath(root *os.Root, relativePath string) ([]os.FileInfo, error) {
	segments := strings.Split(relativePath, string(filepath.Separator))
	snapshot := make([]os.FileInfo, 0, len(segments))
	current := ""
	for index, segment := range segments {
		current = filepath.Join(current, segment)
		info, err := root.Lstat(current)
		if err != nil {
			return nil, fmt.Errorf("inspect path component: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("path contains a symbolic link")
		}
		if index < len(segments)-1 && !info.IsDir() {
			return nil, errors.New("path component is not a directory")
		}
		snapshot = append(snapshot, info)
	}
	return snapshot, nil
}

func sameArtifactPath(left, right []os.FileInfo) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !os.SameFile(left[index], right[index]) {
			return false
		}
	}
	return true
}

func artifactMismatch(artifact Artifact, format string, arguments ...any) error {
	return fmt.Errorf(
		"%w: artifact %q (%s): %s",
		ErrArtifactMismatch,
		artifact.Path,
		artifact.Role,
		fmt.Sprintf(format, arguments...),
	)
}
