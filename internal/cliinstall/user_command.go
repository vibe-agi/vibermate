package cliinstall

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/runtimepath"
)

const terminalReceiptName = "terminal-command.json"

// UserCommand binds the packaged macOS CLI to one user-owned terminal link.
// It derives all paths from OS-owned locations and never edits a shell profile.
type UserCommand struct {
	manager *LinkManager
	spec    LinkSpec
}

// NewDefaultUserCommand resolves the running packaged CLI, current user's home
// and current user's configuration directory into the only Desktop-managed
// terminal entry. The source is canonicalized so invoking an existing managed
// link still identifies the executable inside the App bundle.
func NewDefaultUserCommand(version string, now func() time.Time) (*UserCommand, error) {
	source, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve packaged terminal command: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user home directory: %w", err)
	}
	configuration, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user configuration directory: %w", err)
	}
	return NewUserCommand(source, home, configuration, version, now)
}

// NewUserCommand constructs the user-owned terminal entry without mutating the
// filesystem. It is public to keep path derivation and its tests outside the
// native shell; callers cannot choose another target or receipt name.
func NewUserCommand(
	sourcePath string,
	homeDirectory string,
	configurationDirectory string,
	version string,
	now func() time.Time,
) (*UserCommand, error) {
	for label, value := range map[string]string{
		"user home":               homeDirectory,
		"configuration directory": configurationDirectory,
	} {
		if err := validateAbsolutePath(value); err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
	}
	canonicalSource, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("resolve packaged terminal command: %w", err)
	}
	canonicalHome, err := filepath.EvalSymlinks(homeDirectory)
	if err != nil {
		return nil, fmt.Errorf("resolve user home directory: %w", err)
	}
	if canonicalHome != homeDirectory {
		return nil, errors.New("user home directory must not traverse symbolic links")
	}
	spec := LinkSpec{
		SourcePath: canonicalSource,
		TargetPath: filepath.Join(
			canonicalHome,
			".local",
			"bin",
			"vibermate",
		),
		ReceiptPath: filepath.Join(
			configurationDirectory,
			runtimepath.ApplicationID,
			terminalReceiptName,
		),
		Version: version,
	}
	if err := validateSpec(spec); err != nil {
		return nil, err
	}
	return &UserCommand{
		manager: NewLinkManager(now),
		spec:    spec,
	}, nil
}

// Spec returns the public filesystem coordinates. The private receipt contains
// no credential, but its path remains an implementation detail of this type.
func (command *UserCommand) Spec() LinkSpec {
	if command == nil {
		return LinkSpec{}
	}
	return command.spec
}

func (command *UserCommand) Inspect() (Observation, error) {
	if command == nil || command.manager == nil {
		return Observation{}, errors.New("user terminal command is unavailable")
	}
	return command.manager.Inspect(command.spec)
}

func (command *UserCommand) Install() (Receipt, error) {
	if command == nil || command.manager == nil {
		return Receipt{}, errors.New("user terminal command is unavailable")
	}
	if err := prepareUserCommandDirectory(
		filepath.Dir(command.spec.TargetPath),
	); err != nil {
		return Receipt{}, err
	}
	return command.manager.Install(command.spec)
}

func (command *UserCommand) Refresh() (Receipt, error) {
	if command == nil || command.manager == nil {
		return Receipt{}, errors.New("user terminal command is unavailable")
	}
	observation, err := command.Inspect()
	if err != nil {
		return Receipt{}, err
	}
	if observation.State != StateSourceUpdated || observation.Receipt == nil {
		return Receipt{}, fmt.Errorf(
			"terminal command is not awaiting refresh: %s",
			observation.State,
		)
	}
	return command.manager.AcknowledgeUpdate(
		command.spec,
		observation.Receipt.SourceSHA256,
	)
}

// Repair recreates only a missing terminal entry backed by this App's exact
// private record. Removal compares the complete record again, and installation
// never replaces an object that appears while the repair is in progress.
func (command *UserCommand) Repair() (Receipt, error) {
	if command == nil || command.manager == nil {
		return Receipt{}, errors.New("user terminal command is unavailable")
	}
	observation, err := command.Inspect()
	if err != nil {
		return Receipt{}, err
	}
	if observation.State != StateTargetMissing || observation.Receipt == nil {
		return Receipt{}, fmt.Errorf(
			"terminal command is not safely repairable: %s",
			observation.State,
		)
	}
	removed, err := command.Remove()
	if err != nil {
		return Receipt{}, err
	}
	if removed.State != RemoveMissing {
		return Receipt{}, errors.New("terminal command changed during repair")
	}
	return command.Install()
}

func (command *UserCommand) Remove() (RemoveResult, error) {
	if command == nil || command.manager == nil {
		return RemoveResult{}, errors.New("user terminal command is unavailable")
	}
	if err := prepareUserCommandDirectory(
		filepath.Dir(command.spec.TargetPath),
	); err != nil {
		return RemoveResult{}, err
	}
	return command.manager.Remove(command.spec)
}

func prepareUserCommandDirectory(path string) error {
	if err := validateAbsolutePath(path); err != nil {
		return fmt.Errorf("terminal command directory: %w", err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("create terminal command directory: %w", err)
	}
	directory, err := openAnchoredDirectory(path, false)
	if err != nil {
		return fmt.Errorf("open terminal command directory: %w", err)
	}
	defer directory.close()
	info, err := directory.file.Stat()
	if err != nil {
		return fmt.Errorf("inspect terminal command directory: %w", err)
	}
	owned, err := fileOwnedByCurrentUser(info)
	if err != nil {
		return err
	}
	if !owned || info.Mode().Perm()&0o022 != 0 {
		return errors.New(
			"terminal command directory must be owned by the current user and not writable by other users",
		)
	}
	return nil
}
