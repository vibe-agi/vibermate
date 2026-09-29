package ipallowlist

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func fixedNow() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) }

func mustParse(t *testing.T, entries ...string) List {
	t.Helper()
	list, err := Parse(entries)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestStoreReplacesWithRevisionCheck(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "server-admin")
	store, err := Open(directory, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if current := store.Current(); current.Revision != 0 || current.List.Len() != 0 {
		t.Fatalf("new store = %+v, want revision 0 and an empty list", current)
	}
	if _, err := os.Stat(filepath.Join(directory, FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("opening wrote a file: %v", err)
	}

	saved, err := store.Replace(0, mustParse(t, "203.0.113.0/24"))
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 1 || !saved.UpdatedAt.Equal(fixedNow()) ||
		!slices.Equal(saved.List.Entries(), []string{"203.0.113.0/24"}) {
		t.Fatalf("Replace() = %+v", saved)
	}
	if _, err := store.Replace(0, mustParse(t, "198.51.100.0/24")); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Replace() = %v, want ErrConflict", err)
	}

	info, err := os.Stat(filepath.Join(directory, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("allowlist file mode = %v, want 0600", info.Mode().Perm())
	}
	reopened, err := Open(directory, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if current := reopened.Current(); current.Revision != 1 ||
		!slices.Equal(current.List.Entries(), []string{"203.0.113.0/24"}) {
		t.Fatalf("reopened store = %+v", current)
	}
}

func TestClearReachesARunningStore(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "server-admin")
	store, err := Open(directory, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Replace(0, mustParse(t, "203.0.113.0/24")); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := store.Reload(); err != nil || changed {
		t.Fatalf("Reload() without a change = %v, %v", changed, err)
	}

	cleared, err := Clear(directory, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Revision != 2 || cleared.List.Len() != 0 {
		t.Fatalf("Clear() = %+v", cleared)
	}
	current, changed, err := store.Reload()
	if err != nil || !changed || current.List.Len() != 0 || current.Revision != 2 {
		t.Fatalf("Reload() after Clear = %+v, %v, %v", current, changed, err)
	}
	// A browser that read revision 1 must not undo the server-local clear.
	if _, err := store.Replace(1, mustParse(t, "198.51.100.0/24")); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Replace() after Clear = %v, want ErrConflict", err)
	}
	read, err := Read(directory)
	if err != nil || read.Revision != 2 || read.List.Len() != 0 {
		t.Fatalf("Read() = %+v, %v", read, err)
	}
}

func TestStoredListMustBeTrustworthy(t *testing.T) {
	t.Parallel()

	for name, contents := range map[string]string{
		"not JSON":        "allow everyone",
		"unknown schema":  `{"schema":"other","revision":1,"updatedAt":"2026-09-29T12:00:00Z","ranges":[]}`,
		"unknown field":   `{"schema":"vibermate-server-ip-allowlist-v1","revision":1,"updatedAt":"2026-09-29T12:00:00Z","ranges":[],"deny":[]}`,
		"missing ranges":  `{"schema":"vibermate-server-ip-allowlist-v1","revision":1,"updatedAt":"2026-09-29T12:00:00Z"}`,
		"invalid network": `{"schema":"vibermate-server-ip-allowlist-v1","revision":1,"updatedAt":"2026-09-29T12:00:00Z","ranges":["203.0.113.7/24"]}`,
		"zero revision":   `{"schema":"vibermate-server-ip-allowlist-v1","revision":0,"updatedAt":"2026-09-29T12:00:00Z","ranges":[]}`,
		"trailing data":   `{"schema":"vibermate-server-ip-allowlist-v1","revision":1,"updatedAt":"2026-09-29T12:00:00Z","ranges":[]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			directory := filepath.Join(t.TempDir(), "server-admin")
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, FileName), []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(directory, fixedNow); !errors.Is(err, ErrStoredListInvalid) {
				t.Fatalf("Open() = %v, want ErrStoredListInvalid", err)
			}
			// The server-local command still gets the Owner back in.
			cleared, err := Clear(directory, fixedNow)
			if err != nil || cleared.List.Len() != 0 {
				t.Fatalf("Clear() = %+v, %v", cleared, err)
			}
			if _, err := Open(directory, fixedNow); err != nil {
				t.Fatalf("Open() after Clear = %v", err)
			}
		})
	}
}

func TestReloadKeepsTheListWhenTheFileBreaks(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "server-admin")
	store, err := Open(directory, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Replace(0, mustParse(t, "203.0.113.0/24")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, FileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	current, changed, err := store.Reload()
	if !errors.Is(err, ErrStoredListInvalid) || changed ||
		!slices.Equal(current.List.Entries(), []string{"203.0.113.0/24"}) {
		t.Fatalf("Reload() of a broken file = %+v, %v, %v", current, changed, err)
	}
}
