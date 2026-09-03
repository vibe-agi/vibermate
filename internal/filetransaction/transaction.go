// Package filetransaction serializes one small local-state document across
// goroutines and processes. Every mutation re-reads under an advisory lock and
// commits with an atomic replace, so callers cannot publish a stale snapshot.
package filetransaction

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

type Options struct {
	Path            string
	MaximumBytes    int
	Mode            fs.FileMode
	TemporaryPrefix string
}

type Snapshot struct {
	Payload []byte
	Mode    fs.FileMode
	Exists  bool
}

type Mutation struct {
	Payload []byte
	Write   bool
}

type processLock struct {
	mu   sync.Mutex
	refs int
}

var processLocks = struct {
	sync.Mutex
	byPath map[string]*processLock
}{byPath: make(map[string]*processLock)}

func Read(options Options) (Snapshot, error) {
	if err := options.validate(); err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	err := withLock(options.Path, func() error {
		var err error
		snapshot, err = read(options)
		return err
	})
	return snapshot, err
}

func Update(
	options Options,
	mutate func(Snapshot) (Mutation, error),
) error {
	if err := options.validate(); err != nil || mutate == nil {
		if err != nil {
			return err
		}
		return errors.New("file transaction mutation is missing")
	}
	return withLock(options.Path, func() error {
		snapshot, err := read(options)
		if err != nil {
			return err
		}
		mutation, err := mutate(snapshot)
		if err != nil || !mutation.Write {
			return err
		}
		if len(mutation.Payload) == 0 || len(mutation.Payload) > options.MaximumBytes {
			return errors.New("file transaction payload is outside its bounds")
		}
		return replace(options, mutation.Payload)
	})
}

func (options Options) validate() error {
	if options.Path == "" || !filepath.IsAbs(options.Path) ||
		filepath.Clean(options.Path) != options.Path ||
		filepath.Base(options.Path) == "." ||
		options.MaximumBytes <= 0 ||
		options.Mode.Perm() == 0 || options.Mode.Perm()&0o077 != 0 ||
		options.TemporaryPrefix == "" ||
		filepath.Base(options.TemporaryPrefix) != options.TemporaryPrefix {
		return errors.New("file transaction options are invalid")
	}
	return nil
}

func withLock(path string, operation func() error) error {
	local := retainProcessLock(path)
	local.mu.Lock()
	defer func() {
		local.mu.Unlock()
		releaseProcessLock(path, local)
	}()

	lockPath := path + ".lock"
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open file transaction lock: %w", err)
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("protect file transaction lock: %w", err)
	}
	if err := lockFile(file); err != nil {
		return fmt.Errorf("acquire file transaction lock: %w", err)
	}
	defer func() { _ = unlockFile(file) }()
	return operation()
}

func retainProcessLock(path string) *processLock {
	processLocks.Lock()
	defer processLocks.Unlock()
	lock := processLocks.byPath[path]
	if lock == nil {
		lock = &processLock{}
		processLocks.byPath[path] = lock
	}
	lock.refs++
	return lock
}

func releaseProcessLock(path string, lock *processLock) {
	processLocks.Lock()
	defer processLocks.Unlock()
	lock.refs--
	if lock.refs == 0 && processLocks.byPath[path] == lock {
		delete(processLocks.byPath, path)
	}
}

func read(options Options) (Snapshot, error) {
	info, err := os.Lstat(options.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, nil
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect file transaction document: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > int64(options.MaximumBytes) {
		return Snapshot{}, errors.New("file transaction document is invalid")
	}
	payload, err := os.ReadFile(options.Path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read file transaction document: %w", err)
	}
	if len(payload) > options.MaximumBytes {
		return Snapshot{}, errors.New("file transaction document is invalid")
	}
	return Snapshot{Payload: payload, Mode: info.Mode(), Exists: true}, nil
}

func replace(options Options, payload []byte) error {
	directory := filepath.Dir(options.Path)
	temporary, err := os.CreateTemp(directory, options.TemporaryPrefix)
	if err != nil {
		return fmt.Errorf("create file transaction document: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(options.Mode.Perm()); err != nil {
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := replaceFile(temporaryPath, options.Path); err != nil {
		return err
	}
	committed = true
	if directoryHandle, err := os.Open(directory); err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return nil
}
