package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/releasemanifest"
)

type options struct {
	spec             string
	artifactRoot     string
	sourceRoot       string
	expectedRevision string
	output           string
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "vibermate-release-evidence: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string, stdout, stderr io.Writer) error {
	options, err := parseOptions(arguments, stderr)
	if err != nil {
		return err
	}
	manifest, specInfo, err := readSpec(options.spec)
	if err != nil {
		return err
	}
	if err := releasemanifest.ValidateCommit(options.expectedRevision); err != nil {
		return fmt.Errorf("invalid --expected-revision: %w", err)
	}
	if manifest.Commit != options.expectedRevision {
		return errors.New("spec commit does not match --expected-revision")
	}
	specRelativePath, err := filepath.Rel(options.artifactRoot, options.spec)
	if err != nil {
		return fmt.Errorf("resolve spec relative to artifact root: %w", err)
	}
	specRelativePath = filepath.ToSlash(specRelativePath)
	specInsideArtifactRoot := specRelativePath != "." &&
		specRelativePath != ".." &&
		!strings.HasPrefix(specRelativePath, "../")
	verifyArtifacts := func() error {
		if specInsideArtifactRoot {
			return releasemanifest.VerifyArtifactsWithSpec(
				options.artifactRoot,
				manifest,
				specRelativePath,
			)
		}
		return releasemanifest.VerifyArtifacts(options.artifactRoot, manifest)
	}
	if err := verifySource(options.sourceRoot, options.expectedRevision); err != nil {
		return err
	}
	if err := rejectOutputOverlap(options, manifest, specInfo); err != nil {
		return err
	}
	if err := verifyArtifacts(); err != nil {
		return err
	}
	payload, err := releasemanifest.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := verifySource(options.sourceRoot, options.expectedRevision); err != nil {
		return fmt.Errorf("source changed during evidence generation: %w", err)
	}
	if err := verifyArtifacts(); err != nil {
		return fmt.Errorf("artifacts changed during evidence generation: %w", err)
	}
	if err := verifySource(options.sourceRoot, options.expectedRevision); err != nil {
		return fmt.Errorf("source changed during final artifact verification: %w", err)
	}
	digest, err := writeEvidence(options.output, payload)
	if err != nil {
		return err
	}
	if stdout != nil {
		fmt.Fprintf(stdout, "wrote %s\nsha256 %s\n", options.output, digest)
	}
	return nil
}

func parseOptions(arguments []string, stderr io.Writer) (options, error) {
	var parsed options
	flags := flag.NewFlagSet("vibermate-release-evidence", flag.ContinueOnError)
	if stderr == nil {
		stderr = io.Discard
	}
	flags.SetOutput(stderr)
	flags.StringVar(&parsed.spec, "spec", "", "absolute path to the release manifest specification")
	flags.StringVar(&parsed.artifactRoot, "artifact-root", "", "absolute path containing declared artifacts")
	flags.StringVar(&parsed.sourceRoot, "source-root", "", "absolute path to the clean Git source root")
	flags.StringVar(&parsed.expectedRevision, "expected-revision", "", "expected lowercase 40-character Git revision")
	flags.StringVar(&parsed.output, "output", "", "absolute path for the immutable release manifest")
	if err := flags.Parse(arguments); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, errors.New("positional arguments are not accepted")
	}
	paths := []struct {
		name  string
		value string
	}{
		{"--spec", parsed.spec},
		{"--artifact-root", parsed.artifactRoot},
		{"--source-root", parsed.sourceRoot},
		{"--output", parsed.output},
	}
	for _, candidate := range paths {
		if err := validateAbsolutePath(candidate.name, candidate.value); err != nil {
			return options{}, err
		}
	}
	if parsed.expectedRevision == "" {
		return options{}, errors.New("--expected-revision is required")
	}
	return parsed, nil
}

func validateAbsolutePath(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if !utf8.ValidString(value) || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return fmt.Errorf("%s must be a clean absolute UTF-8 path", name)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("%s cannot contain control characters", name)
		}
	}
	return nil
}

func readSpec(path string) (releasemanifest.Manifest, os.FileInfo, error) {
	pathInfo, err := inspectCanonicalPath(path, false)
	if err != nil {
		return releasemanifest.Manifest{}, nil, fmt.Errorf("inspect --spec: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return releasemanifest.Manifest{}, nil, fmt.Errorf("open --spec: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return releasemanifest.Manifest{}, nil, fmt.Errorf("inspect open --spec: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
		return releasemanifest.Manifest{}, nil, errors.New("--spec changed while it was opened")
	}
	manifest, err := releasemanifest.Decode(file)
	if err != nil {
		return releasemanifest.Manifest{}, nil, fmt.Errorf("decode --spec: %w", err)
	}
	return manifest, pathInfo, nil
}

func verifySource(rootPath, expectedRevision string) error {
	rootInfo, err := inspectCanonicalPath(rootPath, true)
	if err != nil {
		return fmt.Errorf("inspect --source-root: %w", err)
	}
	topLevel, err := gitOutput(rootPath, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("resolve Git source root: %w", err)
	}
	topLevelPath := strings.TrimSpace(string(topLevel))
	topLevelInfo, err := os.Stat(topLevelPath)
	if err != nil || !os.SameFile(rootInfo, topLevelInfo) {
		return errors.New("--source-root must identify the Git worktree root")
	}
	head, err := gitOutput(rootPath, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve Git HEAD: %w", err)
	}
	if strings.TrimSpace(string(head)) != expectedRevision {
		return errors.New("Git HEAD does not match --expected-revision")
	}
	if err := requireNoRepositoryExcludePatterns(rootPath); err != nil {
		return err
	}
	if err := requireUnmaskedGitState(rootPath); err != nil {
		return err
	}
	status, err := gitOutput(
		rootPath,
		"status",
		"--porcelain=v1",
		"--untracked-files=all",
		"--ignore-submodules=none",
	)
	if err != nil {
		return fmt.Errorf("inspect Git source state: %w", err)
	}
	if len(status) != 0 {
		return errors.New("Git source worktree is dirty")
	}
	return nil
}

func requireNoRepositoryExcludePatterns(rootPath string) error {
	reportedPath, err := gitOutput(
		rootPath,
		"rev-parse",
		"--path-format=absolute",
		"--git-path",
		"info/exclude",
	)
	if err != nil {
		return fmt.Errorf("resolve repository-local Git exclude file: %w", err)
	}
	excludePath := strings.TrimSpace(string(reportedPath))
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(rootPath, excludePath)
	}
	initialInfo, err := os.Lstat(excludePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect repository-local Git exclude file: %w", err)
	}
	if initialInfo.Mode()&os.ModeSymlink != 0 ||
		!initialInfo.Mode().IsRegular() ||
		initialInfo.Size() > 64*1024 {
		return errors.New("repository-local Git exclude file is not an admitted regular file")
	}
	handle, err := os.Open(excludePath)
	if err != nil {
		return fmt.Errorf("open repository-local Git exclude file: %w", err)
	}
	openedInfo, err := handle.Stat()
	if err != nil {
		handle.Close()
		return fmt.Errorf("inspect open repository-local Git exclude file: %w", err)
	}
	if !os.SameFile(initialInfo, openedInfo) {
		handle.Close()
		return errors.New("repository-local Git exclude file changed before reading")
	}
	payload, readErr := io.ReadAll(io.LimitReader(handle, 64*1024+1))
	finalOpenedInfo, statErr := handle.Stat()
	closeErr := handle.Close()
	if readErr != nil || statErr != nil || closeErr != nil {
		return errors.New("could not read repository-local Git exclude file")
	}
	finalInfo, err := os.Lstat(excludePath)
	if err != nil ||
		!os.SameFile(initialInfo, finalInfo) ||
		!os.SameFile(openedInfo, finalOpenedInfo) ||
		int64(len(payload)) != initialInfo.Size() ||
		int64(len(payload)) != finalOpenedInfo.Size() ||
		initialInfo.Mode() != finalInfo.Mode() ||
		!initialInfo.ModTime().Equal(finalInfo.ModTime()) {
		return errors.New("repository-local Git exclude file changed while reading")
	}
	if !utf8.Valid(payload) || bytes.IndexByte(payload, 0) >= 0 {
		return errors.New("repository-local Git exclude file is not valid UTF-8 text")
	}
	for _, line := range strings.Split(string(payload), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line != "" && !strings.HasPrefix(line, "#") {
			return errors.New("repository-local Git exclude file contains an active pattern")
		}
	}
	return nil
}

func requireUnmaskedGitState(rootPath string) error {
	flags, err := gitOutput(rootPath, "ls-files", "-v", "-z")
	if err != nil {
		return fmt.Errorf("inspect Git index flags: %w", err)
	}
	tracked := 0
	for _, record := range bytes.Split(flags, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		tracked++
		// A clean ordinary index entry is prefixed "H ". Lowercase prefixes
		// hide assume-unchanged paths and "S " hides skip-worktree paths.
		if len(record) < 3 || record[0] != 'H' || record[1] != ' ' {
			return errors.New("Git index contains a masked or non-ordinary tracked entry")
		}
	}
	if tracked == 0 {
		return errors.New("Git source has no ordinary tracked files")
	}
	// --really-refresh ignores assume-unchanged stat shortcuts. Together with
	// the closed core.filemode/checkStat configuration below, this forces Git to
	// re-observe tracked bytes and executable modes before the exact diffs.
	if _, err := gitOutput(rootPath, "update-index", "-q", "--really-refresh"); err != nil {
		return errors.New("Git source worktree is dirty")
	}
	if _, err := gitOutput(
		rootPath,
		"diff-files",
		"--quiet",
		"--ignore-submodules=none",
		"--",
	); err != nil {
		return errors.New("Git source worktree is dirty")
	}
	if _, err := gitOutput(
		rootPath,
		"diff-index",
		"--cached",
		"--quiet",
		"HEAD",
		"--",
	); err != nil {
		return errors.New("Git source index differs from HEAD")
	}
	return nil
}

func gitOutput(rootPath string, arguments ...string) ([]byte, error) {
	commandArguments := []string{
		"-c", "core.checkStat=default",
		"-c", "core.excludesFile=" + os.DevNull,
		"-c", "core.filemode=true",
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.ignorestat=false",
		"-C", rootPath,
	}
	commandArguments = append(commandArguments, arguments...)
	gitPath := "/usr/bin/git"
	if runtime.GOOS == "windows" {
		var err error
		gitPath, err = exec.LookPath("git")
		if err != nil {
			return nil, err
		}
	}
	command := exec.Command(gitPath, commandArguments...)
	command.Env = cleanGitEnvironment()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", err, message)
	}
	return stdout.Bytes(), nil
}

func cleanGitEnvironment() []string {
	path := "/usr/bin:/bin:/usr/sbin:/sbin"
	if runtime.GOOS == "windows" {
		path = os.Getenv("PATH")
	}
	return []string{
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
		"GIT_TERMINAL_PROMPT=0",
		"LANG=C",
		"LC_ALL=C",
		"PATH=" + path,
	}
}

func rejectOutputOverlap(
	options options,
	manifest releasemanifest.Manifest,
	specInfo os.FileInfo,
) error {
	checksumPath := options.output + ".sha256"
	for _, root := range []struct {
		label string
		path  string
	}{
		{"artifact root", options.artifactRoot},
		{"source root", options.sourceRoot},
	} {
		insideByIdentity, err := outputParentInsideRoot(root.path, options.output)
		if err != nil {
			return fmt.Errorf("inspect output overlap with %s: %w", root.label, err)
		}
		if pathAtOrBelow(root.path, options.output) ||
			pathAtOrBelow(root.path, checksumPath) || insideByIdentity {
			return fmt.Errorf("output cannot be inside the %s", root.label)
		}
	}
	if options.output == options.spec || checksumPath == options.spec {
		return errors.New("output cannot overwrite the input spec")
	}
	for _, artifact := range manifest.Artifacts {
		artifactPath := filepath.Join(options.artifactRoot, filepath.FromSlash(artifact.Path))
		if options.output == artifactPath || checksumPath == artifactPath {
			return fmt.Errorf("output cannot reference artifact %q", artifact.Path)
		}
	}
	for _, outputPath := range []string{options.output, checksumPath} {
		info, err := os.Lstat(outputPath)
		if err == nil {
			if os.SameFile(info, specInfo) {
				return errors.New("output cannot overwrite the input spec")
			}
			return fmt.Errorf("output path already exists: %s", outputPath)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect output path %s: %w", outputPath, err)
		}
	}
	parent := filepath.Dir(options.output)
	_, err := inspectCanonicalPath(parent, true)
	if err != nil {
		return fmt.Errorf("inspect output directory: %w", err)
	}
	return nil
}

func pathAtOrBelow(rootPath, candidatePath string) bool {
	relative, err := filepath.Rel(rootPath, candidatePath)
	if err != nil {
		return false
	}
	return relative == "." ||
		(relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func outputParentInsideRoot(rootPath, outputPath string) (bool, error) {
	rootInfo, err := os.Stat(rootPath)
	if err != nil {
		return false, err
	}
	current := filepath.Dir(outputPath)
	for {
		info, err := os.Stat(current)
		if err != nil {
			return false, err
		}
		if os.SameFile(rootInfo, info) {
			return true, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false, nil
		}
		current = parent
	}
}

func writeEvidence(outputPath string, payload []byte) (string, error) {
	digestBytes := sha256.Sum256(payload)
	digest := hex.EncodeToString(digestBytes[:])
	parentPath := filepath.Dir(outputPath)
	parentInfo, err := inspectCanonicalPath(parentPath, true)
	if err != nil {
		return "", fmt.Errorf("inspect output directory: %w", err)
	}
	root, err := os.OpenRoot(parentPath)
	if err != nil {
		return "", fmt.Errorf("open output directory: %w", err)
	}
	defer root.Close()
	openedParentInfo, err := root.Stat(".")
	if err != nil || !openedParentInfo.IsDir() || !os.SameFile(parentInfo, openedParentInfo) {
		return "", errors.New("output directory changed while it was opened")
	}
	manifestName := filepath.Base(outputPath)
	checksumName := manifestName + ".sha256"
	if filepath.Join(parentPath, manifestName) != outputPath {
		return "", errors.New("output path is not a direct child of its directory")
	}
	for _, name := range []string{manifestName, checksumName} {
		if _, statErr := root.Lstat(name); statErr == nil {
			return "", fmt.Errorf("output path already exists: %s", filepath.Join(parentPath, name))
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("inspect output path %s: %w", filepath.Join(parentPath, name), statErr)
		}
	}
	checksum := []byte(fmt.Sprintf("%s  %s\n", digest, manifestName))

	manifestTemp, err := stagePrivateFile(root, payload)
	if err != nil {
		return "", err
	}
	defer root.Remove(manifestTemp)
	checksumTemp, err := stagePrivateFile(root, checksum)
	if err != nil {
		return "", err
	}
	defer root.Remove(checksumTemp)

	// Publish the checksum before the manifest. The manifest is the commit
	// marker, so a crash can leave an obvious orphan checksum but cannot expose
	// a manifest whose checksum was never made durable.
	if err := root.Link(checksumTemp, checksumName); err != nil {
		return "", fmt.Errorf("publish checksum atomically: %w", err)
	}
	if err := syncOutputDirectory(root); err != nil {
		_ = root.Remove(checksumName)
		_ = syncOutputDirectory(root)
		return "", err
	}
	if err := root.Link(manifestTemp, manifestName); err != nil {
		_ = root.Remove(checksumName)
		_ = syncOutputDirectory(root)
		return "", fmt.Errorf("publish release manifest atomically: %w", err)
	}
	if err := syncOutputDirectory(root); err != nil {
		manifestRemoveErr := root.Remove(manifestName)
		checksumRemoveErr := root.Remove(checksumName)
		_ = syncOutputDirectory(root)
		if manifestRemoveErr != nil || checksumRemoveErr != nil {
			return "", fmt.Errorf(
				"sync published evidence: %v (rollback manifest: %v; checksum: %v)",
				err,
				manifestRemoveErr,
				checksumRemoveErr,
			)
		}
		return "", fmt.Errorf("sync published evidence: %w", err)
	}
	finalParentInfo, err := inspectCanonicalPath(parentPath, true)
	if err != nil || !os.SameFile(openedParentInfo, finalParentInfo) {
		manifestRemoveErr := root.Remove(manifestName)
		checksumRemoveErr := root.Remove(checksumName)
		_ = syncOutputDirectory(root)
		return "", fmt.Errorf(
			"output directory path changed during publication (rollback manifest: %v; checksum: %v)",
			manifestRemoveErr,
			checksumRemoveErr,
		)
	}
	return digest, nil
}

func syncOutputDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open output directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync output directory: %w", err)
	}
	return nil
}

func stagePrivateFile(root *os.Root, payload []byte) (string, error) {
	var file *os.File
	var name string
	for attempt := 0; attempt < 16; attempt++ {
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return "", fmt.Errorf("create staged output name: %w", err)
		}
		name = ".vibermate-release-evidence-" + hex.EncodeToString(random)
		var err error
		file, err = root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("create staged output: %w", err)
		}
	}
	if file == nil {
		return "", errors.New("create staged output: temporary name attempts exhausted")
	}
	keep := false
	defer func() {
		if !keep {
			_ = root.Remove(name)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return "", fmt.Errorf("set staged output permissions: %w", err)
	}
	written, err := file.Write(payload)
	if err != nil {
		file.Close()
		return "", fmt.Errorf("write staged output: %w", err)
	}
	if written != len(payload) {
		file.Close()
		return "", io.ErrShortWrite
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return "", fmt.Errorf("sync staged output: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close staged output: %w", err)
	}
	keep = true
	return name, nil
}

func inspectCanonicalPath(value string, directory bool) (os.FileInfo, error) {
	if !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return nil, errors.New("path must be clean and absolute")
	}
	volume := filepath.VolumeName(value)
	current := volume + string(filepath.Separator)
	if volume == "" {
		current = string(filepath.Separator)
	}
	info, err := os.Lstat(current)
	if err != nil {
		return nil, err
	}
	relative := strings.TrimPrefix(value, current)
	segments := []string{}
	if relative != "" {
		segments = strings.Split(relative, string(filepath.Separator))
	}
	for index, segment := range segments {
		current = filepath.Join(current, segment)
		info, err = os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("path contains a symbolic-link component")
		}
		if index < len(segments)-1 && !info.IsDir() {
			return nil, errors.New("path contains a non-directory ancestor")
		}
	}
	if info.Mode()&os.ModeSymlink != 0 ||
		(directory && !info.IsDir()) ||
		(!directory && !info.Mode().IsRegular()) {
		if directory {
			return nil, errors.New("path must be a non-symlink directory")
		}
		return nil, errors.New("path must be a non-symlink regular file")
	}
	return info, nil
}
