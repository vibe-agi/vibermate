package ipallowlist

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// FileName is the allowlist file inside the Server's admin directory.
const FileName = "ip-allowlist.json"

const (
	fileSchema   = "vibermate-server-ip-allowlist-v1"
	maxFileBytes = 64 << 10
)

// ErrConflict means the list changed after the caller read it.
var ErrConflict = errors.New("IP allowlist changed since it was read")

// ErrStoredListInvalid means the file on disk cannot be trusted. The Server
// refuses to start rather than silently allowing every address.
var ErrStoredListInvalid = errors.New("stored IP allowlist is invalid")

// Snapshot is one saved version of the list. Revision 0 means never saved.
type Snapshot struct {
	Revision  int64
	List      List
	UpdatedAt time.Time
}

func (snapshot Snapshot) same(other Snapshot) bool {
	return snapshot.Revision == other.Revision &&
		slices.Equal(snapshot.List.prefixes, other.List.prefixes)
}

type storedFile struct {
	Schema    string   `json:"schema"`
	Revision  int64    `json:"revision"`
	UpdatedAt string   `json:"updatedAt"`
	Ranges    []string `json:"ranges"`
}

// Store persists the list with an atomic replace and a revision check, so two
// Owner sessions cannot silently overwrite each other.
type Store struct {
	path    string
	now     func() time.Time
	mu      sync.Mutex
	current Snapshot
}

// Open reads the list saved in directory. A missing file is an empty list.
func Open(directory string, now func() time.Time) (*Store, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || now == nil {
		return nil, errors.New("IP allowlist store options are invalid")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create IP allowlist directory: %w", err)
	}
	store := &Store{path: filepath.Join(directory, FileName), now: now}
	snapshot, err := readSnapshot(store.path)
	if err != nil {
		return nil, err
	}
	store.current = snapshot
	return store, nil
}

// Current is the list in effect.
func (store *Store) Current() Snapshot {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.current
}

// Replace saves list when expected is the stored revision.
func (store *Store) Replace(expected int64, list List) (Snapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	// Compare against the file, not memory, so a list cleared by the
	// server-local command is never overwritten by a stale browser.
	onDisk, err := readSnapshot(store.path)
	if err != nil {
		return Snapshot{}, err
	}
	store.current = onDisk
	if expected != onDisk.Revision {
		return Snapshot{}, ErrConflict
	}
	next := Snapshot{
		Revision:  onDisk.Revision + 1,
		List:      list,
		UpdatedAt: store.now().UTC().Truncate(time.Second),
	}
	if err := writeSnapshot(store.path, next); err != nil {
		return Snapshot{}, err
	}
	store.current = next
	return next, nil
}

// Reload picks up a change another process made to the file and reports
// whether the list in effect changed. An unreadable file keeps the current
// list in effect.
func (store *Store) Reload() (Snapshot, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	onDisk, err := readSnapshot(store.path)
	if err != nil {
		return store.current, false, err
	}
	changed := !onDisk.same(store.current)
	store.current = onDisk
	return onDisk, changed, nil
}

// Clear empties the list saved in directory. It is the server-local way back
// in when no allowed network can reach the Server; a running Server picks it
// up. It also replaces a file that cannot be read.
func Clear(directory string, now func() time.Time) (Snapshot, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || now == nil {
		return Snapshot{}, errors.New("IP allowlist store options are invalid")
	}
	path := filepath.Join(directory, FileName)
	previous, err := readSnapshot(path)
	if err != nil {
		previous = Snapshot{}
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return Snapshot{}, fmt.Errorf("create IP allowlist directory: %w", err)
	}
	next := Snapshot{
		Revision:  previous.Revision + 1,
		UpdatedAt: now().UTC().Truncate(time.Second),
	}
	if err := writeSnapshot(path, next); err != nil {
		return Snapshot{}, err
	}
	return next, nil
}

// Read returns the list saved in directory without creating anything.
func Read(directory string) (Snapshot, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return Snapshot{}, errors.New("IP allowlist directory is invalid")
	}
	return readSnapshot(filepath.Join(directory, FileName))
}

func readSnapshot(path string) (Snapshot, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, nil
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("read IP allowlist: %w", err)
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read IP allowlist: %w", err)
	}
	if len(payload) > maxFileBytes {
		return Snapshot{}, ErrStoredListInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var stored storedFile
	var trailing any
	if err := decoder.Decode(&stored); err != nil ||
		!errors.Is(decoder.Decode(&trailing), io.EOF) ||
		stored.Schema != fileSchema || stored.Revision < 1 || stored.Ranges == nil {
		return Snapshot{}, ErrStoredListInvalid
	}
	updatedAt, err := time.Parse(time.RFC3339, stored.UpdatedAt)
	if err != nil {
		return Snapshot{}, ErrStoredListInvalid
	}
	list, err := Parse(stored.Ranges)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: %v", ErrStoredListInvalid, err)
	}
	return Snapshot{Revision: stored.Revision, List: list, UpdatedAt: updatedAt.UTC()}, nil
}

func writeSnapshot(path string, snapshot Snapshot) error {
	payload, err := json.MarshalIndent(storedFile{
		Schema:    fileSchema,
		Revision:  snapshot.Revision,
		UpdatedAt: snapshot.UpdatedAt.UTC().Format(time.RFC3339),
		Ranges:    snapshot.List.Entries(),
	}, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".ip-allowlist-*")
	if err != nil {
		return fmt.Errorf("create IP allowlist replacement: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(append(payload, '\n')); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace IP allowlist: %w", err)
	}
	committed = true
	// The new list is already in place; a failed directory sync only weakens
	// durability across a power loss, so it does not undo the change.
	if directoryFile, err := os.Open(directory); err == nil {
		_ = directoryFile.Sync()
		_ = directoryFile.Close()
	}
	return nil
}
