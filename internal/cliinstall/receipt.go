package cliinstall

import (
	"bytes"
	"crypto/rand"
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
	"sync"
	"time"
	"unicode/utf8"
)

const (
	receiptSchema = "vibermate.cli-link/v1"
	maxCLIBytes   = int64(512 << 20)
	maxReceipt    = int64(32 << 10)
	maxPathBytes  = 4096
	maxVersion    = 128
)

var (
	mutationLock       sync.Mutex
	errIdentityChanged = errors.New("filesystem identity changed")
	errNotOwned        = errors.New("terminal command is not owned by this installation record")
)

// LinkState is the user-actionable relationship between the packaged command,
// terminal entry, and private installation record.
type LinkState string

const (
	StateNotInstalled  LinkState = "not_installed"
	StateCurrent       LinkState = "current"
	StateSourceUpdated LinkState = "source_updated"
	StateSourceMissing LinkState = "source_missing"
	StateTargetMissing LinkState = "target_missing"
	StateUnownedTarget LinkState = "unowned_target"
	StateConflict      LinkState = "conflict"
)

// LinkSpec identifies one macOS terminal entry. SourcePath must be the
// Contents/MacOS/vibermate executable in a stable .app location. The caller or
// privileged helper remains responsible for verifying the app signature and
// deciding that the app location is suitable for a permanent terminal entry.
type LinkSpec struct {
	SourcePath  string
	TargetPath  string
	ReceiptPath string
	Version     string
}

// Receipt is the private ownership record. TargetIdentity binds removal to the
// symbolic-link file ID created by Install while tolerating macOS mount-device
// renumbering; a different link that points to the same source remains unowned.
type Receipt struct {
	Schema         string    `json:"schema"`
	Owner          Owner     `json:"owner"`
	Method         Method    `json:"method"`
	SourcePath     string    `json:"sourcePath"`
	TargetPath     string    `json:"targetPath"`
	SourceSHA256   string    `json:"sourceSha256"`
	SourceSize     int64     `json:"sourceSize"`
	TargetIdentity string    `json:"targetIdentity"`
	Version        string    `json:"version"`
	InstalledAt    time.Time `json:"installedAt"`
	RefreshedAt    time.Time `json:"refreshedAt,omitempty"`
}

// Observation describes the current state without changing the filesystem.
type Observation struct {
	State   LinkState
	Receipt *Receipt
	Detail  string
}

// LinkManager performs the low-frequency ownership transitions. Mutations are
// process-serialized; a privileged helper must also serialize requests at its
// own process boundary and repeat signature and path policy checks.
type LinkManager struct {
	now timeSource

	// The hooks are intentionally private and exist only for deterministic
	// filesystem-race regression tests in this package.
	beforeReceiptPublish  func()
	beforeReceiptExchange func()
	beforeTargetMove      func()
}

type timeSource func() time.Time

// NewLinkManager constructs a manager with an injectable clock.
func NewLinkManager(now func() time.Time) *LinkManager {
	if now == nil {
		now = time.Now
	}
	return &LinkManager{now: now}
}

// Inspect reports whether the exact terminal command and installation record
// are still current. It never repairs or removes an object.
func (manager *LinkManager) Inspect(spec LinkSpec) (Observation, error) {
	if err := validateSpec(spec); err != nil {
		return Observation{}, err
	}
	if err := requireManagedLinkPlatform(); err != nil {
		return Observation{}, err
	}

	receipt, receiptErr := loadReceipt(spec.ReceiptPath)
	target, targetErr := inspectTarget(spec.TargetPath)
	targetMissing := errors.Is(targetErr, os.ErrNotExist)
	if targetErr != nil && !targetMissing {
		return Observation{}, fmt.Errorf("inspect terminal command: %w", targetErr)
	}

	if errors.Is(receiptErr, os.ErrNotExist) {
		if targetMissing {
			return Observation{State: StateNotInstalled}, nil
		}
		return Observation{
			State:  StateUnownedTarget,
			Detail: describeTarget(target.metadata.kind),
		}, nil
	}
	if receiptErr != nil {
		return Observation{}, fmt.Errorf("read private installation record: %w", receiptErr)
	}
	// A missing target can be repaired across App-location changes when the
	// private record still names this exact user-owned terminal entry. The
	// record itself remains the CAS token for removal; a record for another
	// target, owner, or installation method still fails closed as a conflict.
	if targetMissing && receiptOwnsTarget(receipt, spec) {
		return Observation{
			State:   StateTargetMissing,
			Receipt: cloneReceipt(receipt),
			Detail:  "the terminal command recorded by the app is missing",
		}, nil
	}
	if !receiptMatchesSpec(receipt, spec) {
		return Observation{
			State:   StateConflict,
			Receipt: cloneReceipt(receipt),
			Detail:  "the private installation record belongs to a different application location or terminal command",
		}, nil
	}
	if target.metadata.kind != entrySymlink {
		return Observation{
			State:   StateConflict,
			Receipt: cloneReceipt(receipt),
			Detail:  "the terminal command is no longer a symbolic link",
		}, nil
	}
	if target.destination != receipt.SourcePath {
		return Observation{
			State:   StateConflict,
			Receipt: cloneReceipt(receipt),
			Detail:  "the terminal command now points to a different application location",
		}, nil
	}
	if !sameManagedTargetIdentity(receipt.TargetIdentity, target.metadata.identity) {
		return Observation{
			State:   StateConflict,
			Receipt: cloneReceipt(receipt),
			Detail:  "the terminal command was replaced outside the app",
		}, nil
	}

	digest, size, err := digestRegularExecutable(spec.SourcePath)
	if errors.Is(err, os.ErrNotExist) {
		return Observation{
			State:   StateSourceMissing,
			Receipt: cloneReceipt(receipt),
			Detail:  "the app or its packaged terminal command moved",
		}, nil
	}
	if err != nil {
		return Observation{}, fmt.Errorf("inspect packaged terminal command: %w", err)
	}
	if digest != receipt.SourceSHA256 || size != receipt.SourceSize ||
		spec.Version != receipt.Version {
		return Observation{
			State:   StateSourceUpdated,
			Receipt: cloneReceipt(receipt),
			Detail:  "the terminal link is still owned, but the packaged command version changed",
		}, nil
	}
	return Observation{State: StateCurrent, Receipt: cloneReceipt(receipt)}, nil
}

// Install creates a terminal symlink without replacing any existing object.
// Signature verification and the stable-location decision must be repeated by
// the privileged caller immediately before invoking this method.
func (manager *LinkManager) Install(spec LinkSpec) (Receipt, error) {
	if manager == nil || manager.now == nil {
		return Receipt{}, errors.New("terminal command manager is required")
	}
	if err := validateSpec(spec); err != nil {
		return Receipt{}, err
	}
	if err := requireManagedLinkPlatform(); err != nil {
		return Receipt{}, err
	}

	mutationLock.Lock()
	defer mutationLock.Unlock()

	digest, size, err := digestRegularExecutable(spec.SourcePath)
	if err != nil {
		return Receipt{}, fmt.Errorf("inspect packaged terminal command: %w", err)
	}
	targetDirectory, err := openAnchoredDirectory(filepath.Dir(spec.TargetPath), false)
	if err != nil {
		return Receipt{}, fmt.Errorf("open terminal command directory: %w", err)
	}
	defer targetDirectory.close()

	receiptDirectory, err := ensurePrivateDirectory(filepath.Dir(spec.ReceiptPath))
	if err != nil {
		return Receipt{}, fmt.Errorf("prepare private installation-record directory: %w", err)
	}
	defer receiptDirectory.close()

	targetName := filepath.Base(spec.TargetPath)
	receiptName := filepath.Base(spec.ReceiptPath)
	if _, err := targetDirectory.metadata(targetName); err == nil {
		return Receipt{}, errors.New("a terminal command named vibermate already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Receipt{}, fmt.Errorf("inspect terminal command: %w", err)
	}
	if _, err := receiptDirectory.metadata(receiptName); err == nil {
		return Receipt{}, errors.New("a private installation record already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Receipt{}, fmt.Errorf("inspect private installation record: %w", err)
	}

	targetIdentity, err := publishManagedSymlink(
		targetDirectory,
		targetName,
		spec.SourcePath,
	)
	if err != nil {
		return Receipt{}, err
	}
	rollback := func(root error) error {
		rollbackErr := removeExactLink(
			targetDirectory,
			targetName,
			targetIdentity,
			spec.SourcePath,
		)
		if rollbackErr != nil {
			return errors.Join(root, fmt.Errorf(
				"could not undo the newly created terminal command because it changed: %w",
				rollbackErr,
			))
		}
		return root
	}

	confirmedDigest, confirmedSize, err := digestRegularExecutable(spec.SourcePath)
	if err != nil {
		return Receipt{}, rollback(fmt.Errorf(
			"recheck packaged terminal command before recording ownership: %w",
			err,
		))
	}
	if confirmedDigest != digest || confirmedSize != size {
		return Receipt{}, rollback(errors.New(
			"the packaged terminal command changed while it was being installed",
		))
	}
	if err := targetDirectory.sync(); err != nil {
		return Receipt{}, rollback(fmt.Errorf(
			"make terminal command durable: %w",
			err,
		))
	}

	installedAt := manager.now().UTC()
	if installedAt.IsZero() {
		return Receipt{}, rollback(errors.New("installation time is unavailable"))
	}
	receipt := Receipt{
		Schema:         receiptSchema,
		Owner:          OwnerDesktopApp,
		Method:         MethodManagedSymlink,
		SourcePath:     spec.SourcePath,
		TargetPath:     spec.TargetPath,
		SourceSHA256:   digest,
		SourceSize:     size,
		TargetIdentity: targetIdentity,
		Version:        spec.Version,
		InstalledAt:    installedAt,
	}
	if manager.beforeReceiptPublish != nil {
		manager.beforeReceiptPublish()
	}
	if err := writeReceiptExclusive(receiptDirectory, receiptName, receipt); err != nil {
		return Receipt{}, rollback(fmt.Errorf(
			"write private installation record: %w",
			err,
		))
	}
	return receipt, nil
}

// AcknowledgeUpdate refreshes only the private installation record. The link
// continues to point at the stable path inside the app bundle. The caller must
// verify the updated app signature before acknowledging the new digest.
func (manager *LinkManager) AcknowledgeUpdate(
	spec LinkSpec,
	expectedOldDigest string,
) (Receipt, error) {
	if manager == nil || manager.now == nil {
		return Receipt{}, errors.New("terminal command manager is required")
	}
	if err := validateSpec(spec); err != nil {
		return Receipt{}, err
	}
	if !validSHA256(expectedOldDigest) {
		return Receipt{}, errors.New("the expected previous command digest is invalid")
	}
	if err := requireManagedLinkPlatform(); err != nil {
		return Receipt{}, err
	}

	mutationLock.Lock()
	defer mutationLock.Unlock()

	observation, err := manager.Inspect(spec)
	if err != nil {
		return Receipt{}, err
	}
	if observation.State != StateSourceUpdated || observation.Receipt == nil {
		return Receipt{}, fmt.Errorf(
			"the terminal command is not awaiting an app-update refresh: %s",
			observation.State,
		)
	}
	previous := *observation.Receipt
	if previous.SourceSHA256 != expectedOldDigest {
		return Receipt{}, errors.New(
			"the private installation record changed after the update preview",
		)
	}
	digest, size, err := digestRegularExecutable(spec.SourcePath)
	if err != nil {
		return Receipt{}, fmt.Errorf("inspect updated packaged command: %w", err)
	}
	refreshedAt := manager.now().UTC()
	if refreshedAt.Before(previous.InstalledAt) {
		return Receipt{}, errors.New("update time precedes the original installation time")
	}
	updated := previous
	updated.SourceSHA256 = digest
	updated.SourceSize = size
	updated.Version = spec.Version
	updated.RefreshedAt = refreshedAt
	if err := replaceReceipt(
		spec.ReceiptPath,
		previous,
		updated,
		manager.beforeReceiptExchange,
	); err != nil {
		return Receipt{}, fmt.Errorf("refresh private installation record: %w", err)
	}
	return updated, nil
}

// RemoveState is the terminal-entry removal outcome.
type RemoveState string

const (
	RemoveRemoved  RemoveState = "removed"
	RemoveMissing  RemoveState = "already_missing"
	RemoveConflict RemoveState = "conflict"
)

// RemoveResult gives a stable outcome and an optional user-facing explanation.
type RemoveResult struct {
	State  RemoveState
	Detail string
}

// Remove deletes only the exact link still owned by the private installation
// record. A different file or link is never deleted.
func (manager *LinkManager) Remove(spec LinkSpec) (RemoveResult, error) {
	if manager == nil || manager.now == nil {
		return RemoveResult{}, errors.New("terminal command manager is required")
	}
	if err := validateSpec(spec); err != nil {
		return RemoveResult{}, err
	}
	if err := requireManagedLinkPlatform(); err != nil {
		return RemoveResult{}, err
	}

	mutationLock.Lock()
	defer mutationLock.Unlock()

	observation, err := manager.Inspect(spec)
	if err != nil {
		return RemoveResult{}, err
	}
	switch observation.State {
	case StateNotInstalled:
		return RemoveResult{State: RemoveMissing}, nil
	case StateTargetMissing:
		if observation.Receipt == nil {
			return RemoveResult{}, errors.New("private installation record is unavailable")
		}
		quarantine, err := quarantineReceipt(spec.ReceiptPath, *observation.Receipt)
		if errors.Is(err, errNotOwned) {
			return RemoveResult{
				State:  RemoveConflict,
				Detail: "the private installation record changed during removal",
			}, nil
		}
		if err != nil {
			return RemoveResult{}, err
		}
		defer quarantine.close()
		if err := quarantine.remove(); err != nil {
			_ = quarantine.restore()
			return RemoveResult{}, fmt.Errorf("remove stale installation record: %w", err)
		}
		return RemoveResult{State: RemoveMissing}, nil
	case StateCurrent, StateSourceUpdated, StateSourceMissing:
		if observation.Receipt == nil {
			return RemoveResult{}, errors.New("private installation record is unavailable")
		}
		if manager.beforeTargetMove != nil {
			manager.beforeTargetMove()
		}
		return removeOwnedLink(spec, *observation.Receipt)
	default:
		return RemoveResult{
			State:  RemoveConflict,
			Detail: observation.Detail,
		}, nil
	}
}

func removeOwnedLink(spec LinkSpec, receipt Receipt) (RemoveResult, error) {
	targetDirectory, err := openAnchoredDirectory(filepath.Dir(spec.TargetPath), false)
	if err != nil {
		return RemoveResult{}, fmt.Errorf("open terminal command directory: %w", err)
	}
	defer targetDirectory.close()
	target, err := quarantineLink(
		targetDirectory,
		filepath.Base(spec.TargetPath),
		receipt.TargetIdentity,
		receipt.SourcePath,
	)
	if errors.Is(err, errNotOwned) {
		return RemoveResult{
			State:  RemoveConflict,
			Detail: "the terminal command changed during removal",
		}, nil
	}
	if err != nil {
		return RemoveResult{}, err
	}

	record, err := quarantineReceipt(spec.ReceiptPath, receipt)
	if errors.Is(err, errNotOwned) {
		if restoreErr := target.restore(); restoreErr != nil {
			return RemoveResult{}, errors.Join(err, restoreErr)
		}
		return RemoveResult{
			State:  RemoveConflict,
			Detail: "the private installation record changed during removal",
		}, nil
	}
	if err != nil {
		if restoreErr := target.restore(); restoreErr != nil {
			return RemoveResult{}, errors.Join(err, restoreErr)
		}
		return RemoveResult{}, err
	}
	defer record.close()

	if err := target.remove(); err != nil {
		restoreRecordErr := record.restore()
		restoreTargetErr := target.restore()
		return RemoveResult{}, errors.Join(
			fmt.Errorf("remove owned terminal command: %w", err),
			restoreRecordErr,
			restoreTargetErr,
		)
	}
	if err := record.remove(); err != nil {
		// The link is already absent. Restoring the installation record leaves a
		// recoverable target_missing state for a later retry.
		restoreErr := record.restore()
		return RemoveResult{}, errors.Join(
			fmt.Errorf("remove private installation record: %w", err),
			restoreErr,
		)
	}
	return RemoveResult{State: RemoveRemoved}, nil
}

func validateSpec(spec LinkSpec) error {
	if err := validateVersion(spec.Version); err != nil {
		return err
	}
	paths := []struct {
		label string
		value string
	}{
		{label: "packaged command", value: spec.SourcePath},
		{label: "terminal command", value: spec.TargetPath},
		{label: "installation record", value: spec.ReceiptPath},
	}
	for _, path := range paths {
		if err := validateAbsolutePath(path.value); err != nil {
			return fmt.Errorf("%s path: %w", path.label, err)
		}
	}
	if err := validateSourceLayout(spec.SourcePath); err != nil {
		return err
	}
	if filepath.Base(spec.TargetPath) != "vibermate" {
		return errors.New("terminal command must be named vibermate")
	}
	if spec.SourcePath == spec.TargetPath ||
		spec.ReceiptPath == spec.TargetPath ||
		spec.ReceiptPath == spec.SourcePath {
		return errors.New(
			"packaged command, terminal command, and installation record paths must be different",
		)
	}
	return nil
}

func validateAbsolutePath(path string) error {
	if path == "" || len(path) > maxPathBytes || !utf8.ValidString(path) ||
		strings.IndexByte(path, 0) >= 0 {
		return errors.New("must be a bounded text path")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("must be absolute and clean")
	}
	return nil
}

func validateSourceLayout(path string) error {
	if filepath.Base(path) != "vibermate" {
		return errors.New("packaged command must be named vibermate")
	}
	macOSDirectory := filepath.Dir(path)
	contentsDirectory := filepath.Dir(macOSDirectory)
	appDirectory := filepath.Dir(contentsDirectory)
	if filepath.Base(macOSDirectory) != "MacOS" ||
		filepath.Base(contentsDirectory) != "Contents" ||
		!strings.HasSuffix(filepath.Base(appDirectory), ".app") ||
		len(filepath.Base(appDirectory)) <= len(".app") {
		return errors.New(
			"packaged command must be inside a .app at Contents/MacOS/vibermate",
		)
	}
	if knownTransientAppLocation(appDirectory) {
		return errors.New(
			"packaged command is in a temporary app location; move the app to Applications first",
		)
	}
	return nil
}

func knownTransientAppLocation(appDirectory string) bool {
	cleaned := filepath.Clean(appDirectory)
	if cleaned == string(filepath.Separator)+"Volumes" ||
		strings.HasPrefix(
			cleaned,
			string(filepath.Separator)+"Volumes"+string(filepath.Separator),
		) {
		return true
	}
	for _, component := range strings.Split(cleaned, string(filepath.Separator)) {
		switch component {
		case "Downloads", "AppTranslocation", ".Trash", ".Trashes":
			return true
		}
	}
	return false
}

func validateVersion(version string) error {
	if version == "" || len(version) > maxVersion || !utf8.ValidString(version) ||
		strings.TrimSpace(version) != version {
		return errors.New("a bounded app version is required")
	}
	for _, character := range version {
		if character < 0x20 || character == 0x7f {
			return errors.New("app version contains a control character")
		}
	}
	return nil
}

func digestRegularExecutable(path string) (string, int64, error) {
	if err := requireUnaliasedPath(path); err != nil {
		return "", 0, err
	}
	parent, err := openAnchoredDirectory(filepath.Dir(path), false)
	if err != nil {
		return "", 0, err
	}
	defer parent.close()
	file, err := parent.openFile(filepath.Base(path), os.O_RDONLY, 0)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if !before.Mode().IsRegular() {
		return "", 0, errors.New("packaged command must be a regular file, not a symbolic link")
	}
	if before.Size() < 1 || before.Size() > maxCLIBytes {
		return "", 0, errors.New("packaged command size is outside the supported bound")
	}
	if before.Mode().Perm()&0o111 == 0 {
		return "", 0, errors.New("packaged command is not executable")
	}
	if links, linkErr := fileLinkCount(before); linkErr != nil {
		return "", 0, linkErr
	} else if links != 1 {
		return "", 0, errors.New("packaged command must have one filesystem name")
	}

	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maxCLIBytes+1))
	if err != nil {
		return "", 0, fmt.Errorf("read packaged command: %w", err)
	}
	after, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return "", 0, err
	}
	if written != before.Size() || written > maxCLIBytes ||
		!sameOpenFile(before, after) || !os.SameFile(after, pathInfo) ||
		pathInfo.Mode() != after.Mode() || pathInfo.Size() != after.Size() ||
		!pathInfo.ModTime().Equal(after.ModTime()) {
		return "", 0, errors.New("packaged command changed while it was inspected")
	}
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}

func sameOpenFile(left, right os.FileInfo) bool {
	return os.SameFile(left, right) &&
		left.Mode() == right.Mode() &&
		left.Size() == right.Size() &&
		left.ModTime().Equal(right.ModTime())
}

func requireUnaliasedPath(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if resolved != path {
		return errors.New("packaged command path must not traverse symbolic links")
	}
	return nil
}

func inspectTarget(path string) (targetSnapshot, error) {
	directory, err := openAnchoredDirectory(filepath.Dir(path), false)
	if err != nil {
		return targetSnapshot{}, err
	}
	defer directory.close()
	return inspectTargetEntry(directory, filepath.Base(path))
}

func inspectTargetEntry(
	directory *anchoredDirectory,
	name string,
) (targetSnapshot, error) {
	before, err := directory.metadata(name)
	if err != nil {
		return targetSnapshot{}, err
	}
	snapshot := targetSnapshot{metadata: before}
	if before.kind == entrySymlink {
		snapshot.destination, err = directory.readlink(name)
		if err != nil {
			return targetSnapshot{}, err
		}
	}
	after, err := directory.metadata(name)
	if err != nil {
		return targetSnapshot{}, err
	}
	if !sameEntry(before, after) {
		return targetSnapshot{}, errIdentityChanged
	}
	return snapshot, nil
}

func publishManagedSymlink(
	directory *anchoredDirectory,
	name string,
	destination string,
) (string, error) {
	temporaryName, err := createTemporarySymlink(
		directory,
		".vibermate-command-install-",
		destination,
	)
	if err != nil {
		return "", fmt.Errorf("prepare terminal command: %w", err)
	}
	defer func() {
		_ = directory.unlinkIfExists(temporaryName)
	}()
	temporary, err := inspectTargetEntry(directory, temporaryName)
	if err != nil {
		return "", err
	}
	if temporary.metadata.kind != entrySymlink ||
		temporary.metadata.links != 1 ||
		temporary.destination != destination {
		return "", errors.New("prepared terminal command has an unexpected filesystem identity")
	}
	if err := directory.renameNoReplace(temporaryName, name); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", errors.New("a terminal command named vibermate already exists")
		}
		return "", fmt.Errorf("publish terminal command without replacement: %w", err)
	}
	published, err := inspectTargetEntry(directory, name)
	if err != nil {
		return "", fmt.Errorf("confirm published terminal command: %w", err)
	}
	if published.metadata.identity != temporary.metadata.identity ||
		published.destination != destination {
		return "", errors.New("terminal command changed as it was published")
	}
	return published.metadata.identity, nil
}

func createTemporarySymlink(
	directory *anchoredDirectory,
	prefix string,
	destination string,
) (string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		name, err := randomEntryName(prefix)
		if err != nil {
			return "", err
		}
		if err := directory.symlink(destination, name); err == nil {
			return name, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("could not allocate a temporary terminal-command name")
}

func removeExactLink(
	directory *anchoredDirectory,
	name string,
	identity string,
	destination string,
) error {
	quarantine, err := quarantineLink(directory, name, identity, destination)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return quarantine.remove()
}

func quarantineLink(
	directory *anchoredDirectory,
	name string,
	identity string,
	destination string,
) (*entryQuarantine, error) {
	current, err := inspectTargetEntry(directory, name)
	if err != nil {
		return nil, err
	}
	if current.metadata.kind != entrySymlink ||
		!sameManagedTargetIdentity(identity, current.metadata.identity) ||
		current.destination != destination {
		return nil, errNotOwned
	}
	quarantineName, err := moveToUniqueName(
		directory,
		name,
		".vibermate-command-remove-",
	)
	if err != nil {
		return nil, fmt.Errorf("move terminal command aside for exact removal: %w", err)
	}
	quarantine := &entryQuarantine{
		directory: directory,
		original:  name,
		moved:     quarantineName,
	}
	moved, inspectErr := inspectTargetEntry(directory, quarantineName)
	if inspectErr != nil ||
		!sameManagedTargetIdentity(identity, moved.metadata.identity) ||
		moved.destination != destination {
		restoreErr := quarantine.restore()
		return nil, errors.Join(errNotOwned, inspectErr, restoreErr)
	}
	return quarantine, nil
}

func moveToUniqueName(
	directory *anchoredDirectory,
	name string,
	prefix string,
) (string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		candidate, err := randomEntryName(prefix)
		if err != nil {
			return "", err
		}
		if err := directory.renameNoReplace(name, candidate); err == nil {
			return candidate, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("could not allocate a private removal name")
}

type entryQuarantine struct {
	directory *anchoredDirectory
	original  string
	moved     string
	done      bool
}

func (quarantine *entryQuarantine) restore() error {
	if quarantine == nil || quarantine.done {
		return nil
	}
	if err := quarantine.directory.renameNoReplace(
		quarantine.moved,
		quarantine.original,
	); err != nil {
		return fmt.Errorf("restore terminal command after an incomplete removal: %w", err)
	}
	quarantine.done = true
	return quarantine.directory.sync()
}

func (quarantine *entryQuarantine) remove() error {
	if quarantine == nil || quarantine.done {
		return nil
	}
	if err := quarantine.directory.unlink(quarantine.moved); err != nil {
		return err
	}
	quarantine.done = true
	return quarantine.directory.sync()
}

type receiptQuarantine struct {
	directory  *anchoredDirectory
	quarantine *entryQuarantine
}

func quarantineReceipt(path string, expected Receipt) (*receiptQuarantine, error) {
	directory, err := openAnchoredDirectory(filepath.Dir(path), true)
	if err != nil {
		return nil, fmt.Errorf("open private installation-record directory: %w", err)
	}
	name := filepath.Base(path)
	current, err := loadReceiptEntry(directory, name)
	if err != nil {
		directory.close()
		return nil, err
	}
	if !sameReceipt(current, expected) {
		directory.close()
		return nil, errNotOwned
	}
	moved, err := moveToUniqueName(
		directory,
		name,
		".vibermate-installation-record-remove-",
	)
	if err != nil {
		directory.close()
		return nil, err
	}
	quarantine := &entryQuarantine{
		directory: directory,
		original:  name,
		moved:     moved,
	}
	movedReceipt, loadErr := loadReceiptEntry(directory, moved)
	if loadErr != nil || !sameReceipt(movedReceipt, expected) {
		restoreErr := quarantine.restore()
		directory.close()
		return nil, errors.Join(errNotOwned, loadErr, restoreErr)
	}
	return &receiptQuarantine{
		directory:  directory,
		quarantine: quarantine,
	}, nil
}

func (quarantine *receiptQuarantine) restore() error {
	if quarantine == nil {
		return nil
	}
	return quarantine.quarantine.restore()
}

func (quarantine *receiptQuarantine) remove() error {
	if quarantine == nil {
		return nil
	}
	return quarantine.quarantine.remove()
}

func (quarantine *receiptQuarantine) close() {
	if quarantine != nil && quarantine.directory != nil {
		quarantine.directory.close()
	}
}

func loadReceipt(path string) (Receipt, error) {
	directory, err := openAnchoredDirectory(filepath.Dir(path), true)
	if err != nil {
		return Receipt{}, err
	}
	defer directory.close()
	return loadReceiptEntry(directory, filepath.Base(path))
}

func loadReceiptEntry(
	directory *anchoredDirectory,
	name string,
) (Receipt, error) {
	file, err := directory.openFile(name, os.O_RDONLY, 0)
	if err != nil {
		return Receipt{}, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return Receipt{}, err
	}
	if !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > maxReceipt {
		return Receipt{}, errors.New("private installation record must be a bounded regular file")
	}
	if before.Mode().Perm()&0o077 != 0 {
		return Receipt{}, errors.New("private installation record is accessible by another user")
	}
	if owned, ownerErr := fileOwnedByCurrentUser(before); ownerErr != nil {
		return Receipt{}, ownerErr
	} else if !owned {
		return Receipt{}, errors.New("private installation record belongs to another user")
	}
	if links, linkErr := fileLinkCount(before); linkErr != nil {
		return Receipt{}, linkErr
	} else if links != 1 {
		return Receipt{}, errors.New("private installation record has more than one filesystem name")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxReceipt+1))
	if err != nil {
		return Receipt{}, err
	}
	after, err := file.Stat()
	if err != nil {
		return Receipt{}, err
	}
	entry, err := directory.metadata(name)
	if err != nil {
		return Receipt{}, err
	}
	identity, err := fileIdentity(before)
	if err != nil {
		return Receipt{}, err
	}
	if int64(len(data)) != before.Size() || !sameOpenFile(before, after) ||
		entry.identity != identity || entry.kind != entryRegular ||
		entry.size != before.Size() || entry.permissions != before.Mode().Perm() {
		return Receipt{}, errors.New("private installation record changed while it was read")
	}

	var receipt Receipt
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return Receipt{}, fmt.Errorf("decode private installation record: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Receipt{}, errors.New("private installation record contains trailing data")
	}
	if err := validateReceipt(receipt); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func validateReceipt(receipt Receipt) error {
	if receipt.Schema != receiptSchema ||
		receipt.Owner != OwnerDesktopApp ||
		receipt.Method != MethodManagedSymlink ||
		receipt.SourceSize < 1 || receipt.SourceSize > maxCLIBytes ||
		receipt.InstalledAt.IsZero() {
		return errors.New("private installation record identity is incomplete")
	}
	if err := validateAbsolutePath(receipt.SourcePath); err != nil {
		return errors.New("private installation record has an invalid app command path")
	}
	if err := validateSourceLayout(receipt.SourcePath); err != nil {
		return errors.New("private installation record has an invalid app command location")
	}
	if err := validateAbsolutePath(receipt.TargetPath); err != nil ||
		filepath.Base(receipt.TargetPath) != "vibermate" {
		return errors.New("private installation record has an invalid terminal command path")
	}
	if err := validateVersion(receipt.Version); err != nil {
		return errors.New("private installation record has an invalid app version")
	}
	if !validSHA256(receipt.SourceSHA256) {
		return errors.New("private installation record has an invalid command digest")
	}
	if !validFileIdentity(receipt.TargetIdentity) {
		return errors.New("private installation record has an invalid terminal-command identity")
	}
	if offset := zoneOffset(receipt.InstalledAt); offset != 0 {
		return errors.New("private installation time must use UTC")
	}
	if !receipt.RefreshedAt.IsZero() {
		if zoneOffset(receipt.RefreshedAt) != 0 ||
			receipt.RefreshedAt.Before(receipt.InstalledAt) {
			return errors.New("private installation refresh time is invalid")
		}
	}
	return nil
}

func zoneOffset(value time.Time) int {
	_, offset := value.Zone()
	return offset
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size &&
		strings.ToLower(value) == value
}

func writeReceiptExclusive(
	directory *anchoredDirectory,
	name string,
	receipt Receipt,
) error {
	if err := validateReceipt(receipt); err != nil {
		return err
	}
	data, err := marshalReceipt(receipt)
	if err != nil {
		return err
	}
	temporaryName, err := writeTemporaryFile(
		directory,
		".vibermate-installation-record-",
		data,
	)
	if err != nil {
		return err
	}
	defer func() {
		_ = directory.unlinkIfExists(temporaryName)
	}()
	if err := directory.renameNoReplace(temporaryName, name); err != nil {
		return fmt.Errorf("publish installation record without replacement: %w", err)
	}
	return directory.sync()
}

func replaceReceipt(
	path string,
	previous Receipt,
	next Receipt,
	beforeExchange func(),
) error {
	if err := validateReceipt(next); err != nil {
		return err
	}
	directory, err := openAnchoredDirectory(filepath.Dir(path), true)
	if err != nil {
		return err
	}
	defer directory.close()
	name := filepath.Base(path)
	current, err := loadReceiptEntry(directory, name)
	if err != nil {
		return err
	}
	if !sameReceipt(current, previous) {
		return errors.New("private installation record changed after it was inspected")
	}
	data, err := marshalReceipt(next)
	if err != nil {
		return err
	}
	temporaryName, err := writeTemporaryFile(
		directory,
		".vibermate-installation-record-refresh-",
		data,
	)
	if err != nil {
		return err
	}
	defer func() {
		_ = directory.unlinkIfExists(temporaryName)
	}()
	if beforeExchange != nil {
		beforeExchange()
	}
	if err := directory.exchange(temporaryName, name); err != nil {
		return fmt.Errorf("atomically refresh installation record: %w", err)
	}
	oldRecord, readErr := loadReceiptEntry(directory, temporaryName)
	if readErr != nil || !sameReceipt(oldRecord, previous) {
		restoreErr := directory.exchange(temporaryName, name)
		return errors.Join(
			errors.New("private installation record changed during refresh"),
			readErr,
			restoreErr,
		)
	}
	if err := directory.unlink(temporaryName); err != nil {
		return fmt.Errorf("remove previous installation record after refresh: %w", err)
	}
	return directory.sync()
}

func marshalReceipt(receipt Receipt) ([]byte, error) {
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if int64(len(data)) > maxReceipt {
		return nil, errors.New("private installation record exceeds its size bound")
	}
	return data, nil
}

func writeTemporaryFile(
	directory *anchoredDirectory,
	prefix string,
	data []byte,
) (string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		name, err := randomEntryName(prefix)
		if err != nil {
			return "", err
		}
		file, err := directory.openFile(
			name,
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			0o600,
		)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		fail := func(root error) (string, error) {
			_ = file.Close()
			_ = directory.unlinkIfExists(name)
			return "", root
		}
		if err := file.Chmod(0o600); err != nil {
			return fail(err)
		}
		if _, err := file.Write(data); err != nil {
			return fail(err)
		}
		if err := file.Sync(); err != nil {
			return fail(err)
		}
		if err := file.Close(); err != nil {
			_ = directory.unlinkIfExists(name)
			return "", err
		}
		return name, nil
	}
	return "", errors.New("could not allocate a private temporary file")
}

func randomEntryName(prefix string) (string, error) {
	var entropy [16]byte
	if _, err := io.ReadFull(rand.Reader, entropy[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(entropy[:]), nil
}

func receiptMatchesSpec(receipt Receipt, spec LinkSpec) bool {
	return receipt.SourcePath == spec.SourcePath &&
		receiptOwnsTarget(receipt, spec)
}

func receiptOwnsTarget(receipt Receipt, spec LinkSpec) bool {
	return receipt.TargetPath == spec.TargetPath &&
		receipt.Owner == OwnerDesktopApp &&
		receipt.Method == MethodManagedSymlink
}

// Darwin's st_dev identifies the current mount, not the persistent volume.
// macOS may therefore renumber it after a reboot while preserving the file ID
// (st_ino) of the exact same symbolic link. SourcePath, TargetPath, entry kind,
// and link destination are checked separately before this comparison.
func sameManagedTargetIdentity(recorded, current string) bool {
	if recorded == current {
		return true
	}
	if runtime.GOOS != "darwin" ||
		!validFileIdentity(recorded) || !validFileIdentity(current) {
		return false
	}
	recordedParts := strings.Split(recorded, ":")
	currentParts := strings.Split(current, ":")
	return recordedParts[1] == currentParts[1]
}

func sameReceipt(left, right Receipt) bool {
	return left.Schema == right.Schema &&
		left.Owner == right.Owner &&
		left.Method == right.Method &&
		left.SourcePath == right.SourcePath &&
		left.TargetPath == right.TargetPath &&
		left.SourceSHA256 == right.SourceSHA256 &&
		left.SourceSize == right.SourceSize &&
		left.TargetIdentity == right.TargetIdentity &&
		left.Version == right.Version &&
		left.InstalledAt.Equal(right.InstalledAt) &&
		left.RefreshedAt.Equal(right.RefreshedAt)
}

func cloneReceipt(receipt Receipt) *Receipt {
	cloned := receipt
	return &cloned
}

func describeTarget(kind entryKind) string {
	switch kind {
	case entrySymlink:
		return "an unowned symbolic link already uses the terminal-command name"
	case entryRegular:
		return "an unowned file already uses the terminal-command name"
	default:
		return "an unowned filesystem object already uses the terminal-command name"
	}
}

type targetSnapshot struct {
	metadata    entryMetadata
	destination string
}

type entryKind uint8

const (
	entryOther entryKind = iota
	entryRegular
	entryDirectory
	entrySymlink
)

type entryMetadata struct {
	identity    string
	kind        entryKind
	size        int64
	permissions os.FileMode
	links       uint64
}

func sameEntry(left, right entryMetadata) bool {
	return left.identity == right.identity &&
		left.kind == right.kind &&
		left.size == right.size &&
		left.permissions == right.permissions &&
		left.links == right.links
}

func requireManagedLinkPlatform() error {
	if runtime.GOOS != "darwin" {
		return errors.New("app-managed terminal links are supported only on macOS")
	}
	return nil
}
