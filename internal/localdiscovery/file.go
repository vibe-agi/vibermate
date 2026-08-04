package localdiscovery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/instanceguard"
)

const maxSessionBytes = 16 << 10

type Clock interface {
	Now() time.Time
}

// File owns the atomic publication and instance-scoped removal of one local
// control discovery record.
type File struct {
	mu    sync.Mutex
	path  string
	clock Clock
	guard *instanceguard.Guard
}

// NewFile creates a read-only local control discovery boundary. Publishing requires
// NewPublisher and a live generation guard for the same runtime directory.
func NewFile(path string, clock Clock) (*File, error) {
	return newFile(path, clock, nil)
}

// NewPublisher binds local control discovery publication to the kernel-backed
// generation owner for the same private runtime directory.
func NewPublisher(
	path string,
	clock Clock,
	guard *instanceguard.Guard,
) (*File, error) {
	file, err := newFile(path, clock, guard)
	if err != nil {
		return nil, err
	}
	if !guard.OwnsDirectory(filepath.Dir(path)) {
		return nil, errors.New("local control discovery generation ownership is invalid")
	}
	return file, nil
}

func newFile(
	path string,
	clock Clock,
	guard *instanceguard.Guard,
) (*File, error) {
	if path == "" ||
		!filepath.IsAbs(path) ||
		filepath.Clean(path) != path ||
		filepath.Base(path) == "." ||
		filepath.Base(path) == string(filepath.Separator) {
		return nil, errors.New("local control discovery path must be an absolute clean file path")
	}
	if clock == nil {
		return nil, errors.New("local control discovery clock is required")
	}
	return &File{path: path, clock: clock, guard: guard}, nil
}

func (file *File) Path() string {
	if file == nil {
		return ""
	}
	return file.path
}

func (file *File) Publish(session Session) error {
	if file == nil {
		return errors.New("local control discovery file is required")
	}
	if file.guard == nil ||
		!file.guard.OwnsDirectory(filepath.Dir(file.path)) {
		return errors.New("local control discovery publication requires generation ownership")
	}
	if err := validateSession(session, file.clock.Now(), true); err != nil {
		return err
	}
	payload, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("encode local control discovery: %w", err)
	}
	payload = append(payload, '\n')
	if len(payload) > maxSessionBytes {
		return errors.New("local control discovery record exceeds the size limit")
	}

	file.mu.Lock()
	defer file.mu.Unlock()
	if err := ensurePrivateDirectory(filepath.Dir(file.path)); err != nil {
		return err
	}
	_, loadErr := file.loadLocked(false)
	if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		return fmt.Errorf("inspect existing local control discovery: %w", loadErr)
	}
	return replacePrivateFile(file.path, payload)
}

func (file *File) Load() (Session, error) {
	if file == nil {
		return Session{}, errors.New("local control discovery file is required")
	}
	file.mu.Lock()
	defer file.mu.Unlock()
	return file.loadLocked(true)
}

// Remove deletes only the record owned by instanceID. It deliberately refuses
// to remove an unreadable record or a newer runtime's publication.
func (file *File) Remove(instanceID string) error {
	if file == nil || instanceID == "" {
		return errors.New("local control discovery file and instance ID are required")
	}
	file.mu.Lock()
	defer file.mu.Unlock()
	current, err := file.loadLocked(false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect local control discovery before removal: %w", err)
	}
	if current.InstanceID != instanceID {
		return ErrOwnerConflict
	}
	if err := os.Remove(file.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove local control discovery: %w", err)
	}
	return syncDirectory(filepath.Dir(file.path))
}

func (file *File) loadLocked(requireFresh bool) (Session, error) {
	payload, err := readPrivateFile(file.path)
	if err != nil {
		return Session{}, err
	}
	var session Session
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&session); err != nil {
		return Session{}, fmt.Errorf("decode local control discovery: %w", err)
	}
	var suffix any
	if err := decoder.Decode(&suffix); !errors.Is(err, io.EOF) {
		return Session{}, errors.New("local control discovery contains trailing JSON")
	}
	if err := validateSession(session, file.clock.Now(), requireFresh); err != nil {
		return Session{}, err
	}
	return session, nil
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create local control discovery directory: %w", err)
		}
		if runtime.GOOS != "windows" {
			if err := os.Chmod(path, 0o700); err != nil {
				return fmt.Errorf("secure local control discovery directory: %w", err)
			}
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("inspect local control discovery directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("local control discovery parent must be a directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		return errors.New("local control discovery directory must have mode 0700")
	}
	if err := validatePrivateDirectory(path); err != nil {
		return fmt.Errorf("validate local control discovery directory: %w", err)
	}
	return nil
}

func replacePrivateFile(path string, payload []byte) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("local control discovery target is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect local control discovery target: %w", err)
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".local-control-discovery-*")
	if err != nil {
		return fmt.Errorf("create local control discovery temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	fail := func(action string, root error) error {
		_ = temporary.Close()
		return fmt.Errorf("%s local control discovery: %w", action, root)
	}
	if err := temporary.Chmod(0o600); err != nil {
		return fail("secure", err)
	}
	if err := validateOpenedPrivateFile(temporary); err != nil {
		return fail("validate temporary", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		return fail("write", err)
	}
	if err := temporary.Sync(); err != nil {
		return fail("sync", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close local control discovery: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish local control discovery: %w", err)
	}
	return syncDirectory(directory)
}

func readPrivateFile(path string) ([]byte, error) {
	if err := validatePrivateDirectory(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("validate local control discovery directory: %w", err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("local control discovery is not a regular file")
	}
	if runtime.GOOS != "windows" && before.Mode().Perm() != 0o600 {
		return nil, errors.New("local control discovery file is not private")
	}
	opened, err := openReadNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	after, err := opened.Stat()
	if err != nil {
		return nil, err
	}
	if err := validateOpenedPrivateFile(opened); err != nil {
		return nil, fmt.Errorf("validate local control discovery file: %w", err)
	}
	if !os.SameFile(before, after) {
		return nil, errors.New("local control discovery changed while opening")
	}
	payload, err := io.ReadAll(io.LimitReader(opened, maxSessionBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read local control discovery: %w", err)
	}
	if len(payload) > maxSessionBytes {
		return nil, errors.New("local control discovery record exceeds the size limit")
	}
	return payload, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
