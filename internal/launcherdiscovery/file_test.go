package launcherdiscovery_test

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/launcherdiscovery"
)

func TestFilePublishesLoadsAndRemovesOnlyItsInstance(t *testing.T) {
	t.Parallel()

	clock := &fixedClock{now: time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "private", "launcher.json")
	file := newPublisher(t, path, clock)
	session := validSession(clock.now, 0x11)
	if err := file.Publish(session); err != nil {
		t.Fatal(err)
	}
	loaded, err := file.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded != session {
		t.Fatalf("loaded session = %+v, want %+v", loaded, session)
	}
	if runtime.GOOS != "windows" {
		directory, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		record, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if directory.Mode().Perm() != 0o700 || record.Mode().Perm() != 0o600 {
			t.Fatalf(
				"private modes directory=%04o record=%04o",
				directory.Mode().Perm(),
				record.Mode().Perm(),
			)
		}
	}
	if err := file.Remove(validSession(clock.now, 0x22).InstanceID); !errors.Is(
		err,
		launcherdiscovery.ErrOwnerConflict,
	) {
		t.Fatalf("Remove(other instance) error = %v", err)
	}
	if _, err := file.Load(); err != nil {
		t.Fatalf("other instance removal changed record: %v", err)
	}
	if err := file.Remove(session.InstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load() after owner removal error = %v", err)
	}
}

func TestFileRejectsExpiredUnsafeAndConflictingRecords(t *testing.T) {
	t.Parallel()

	clock := &fixedClock{now: time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "private", "launcher.json")
	file := newPublisher(t, path, clock)
	current := validSession(clock.now, 0x31)
	if err := file.Publish(current); err != nil {
		t.Fatal(err)
	}
	replacement := validSession(clock.now, 0x32)
	if err := file.Publish(replacement); err != nil {
		t.Fatalf("Publish(new generation) error = %v", err)
	}
	loaded, err := file.Load()
	if err != nil || loaded.InstanceID != replacement.InstanceID {
		t.Fatalf("Load(new generation) = %+v, err=%v", loaded, err)
	}
	clock.now = replacement.ExpiresAt
	if _, err := file.Load(); !errors.Is(err, launcherdiscovery.ErrExpired) {
		t.Fatalf("Load(expired) error = %v", err)
	}
	fresh := validSession(clock.now, 0x34)
	if err := file.Publish(fresh); err != nil {
		t.Fatalf("Publish(after expiry) error = %v", err)
	}

	if err := file.Remove(fresh.InstanceID); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		filepath.Join(t.TempDir(), "target"),
		path,
	); err != nil {
		t.Fatal(err)
	}
	if err := file.Publish(validSession(clock.now, 0x33)); err == nil {
		t.Fatal("Publish() accepted a symlink target")
	}
	if _, err := file.Load(); err == nil {
		t.Fatal("Load() accepted a symlink target")
	}
}

func TestFileRejectsUnsafeParentAndRecordModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix ownership and mode contract")
	}
	t.Parallel()

	clock := &fixedClock{now: time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)}
	directory := filepath.Join(t.TempDir(), "unsafe")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "launcher.json")
	guard, err := instanceguard.Acquire(filepath.Join(directory, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Release()
	file, err := launcherdiscovery.NewPublisher(path, clock, guard)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := file.Publish(validSession(clock.now, 0x35)); err == nil {
		t.Fatal("Publish() accepted a non-private parent directory")
	}

	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := file.Publish(validSession(clock.now, 0x36)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Load(); err == nil {
		t.Fatal("Load() accepted a non-private discovery record")
	}
}

func TestFileRejectsNonLoopbackAndMalformedSession(t *testing.T) {
	t.Parallel()

	clock := &fixedClock{now: time.Now().UTC()}
	file := newPublisher(
		t,
		filepath.Join(t.TempDir(), "private", "launcher.json"),
		clock,
	)
	cases := []launcherdiscovery.Session{
		{},
		validSession(clock.now, 0x41),
		validSession(clock.now, 0x42),
		validSession(clock.now, 0x43),
	}
	cases[1].BaseURL = "http://localhost:4321"
	cases[2].LauncherToken = "not-a-capability"
	cases[3].Schema = "unknown"
	for _, session := range cases {
		if err := file.Publish(session); err == nil {
			t.Fatalf("Publish(%+v) succeeded", session)
		}
	}
}

func TestFileReaderCannotPublishWithoutGenerationOwnership(t *testing.T) {
	t.Parallel()

	clock := &fixedClock{now: time.Now().UTC()}
	file, err := launcherdiscovery.NewFile(
		filepath.Join(t.TempDir(), "launcher.json"),
		clock,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Publish(validSession(clock.now, 0x51)); err == nil {
		t.Fatal("read-only discovery boundary published a launcher session")
	}
}

func newPublisher(
	t *testing.T,
	path string,
	clock launcherdiscovery.Clock,
) *launcherdiscovery.File {
	t.Helper()
	guard, err := instanceguard.Acquire(
		filepath.Join(filepath.Dir(path), "daemon.lock"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := guard.Release(); err != nil {
			t.Error(err)
		}
	})
	file, err := launcherdiscovery.NewPublisher(path, clock, guard)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func validSession(now time.Time, fill byte) launcherdiscovery.Session {
	return launcherdiscovery.Session{
		Schema:        launcherdiscovery.SchemaV1,
		InstanceID:    base64.RawURLEncoding.EncodeToString(repeat(fill, 20)),
		ProcessID:     1234,
		BaseURL:       "http://127.0.0.1:43210",
		LauncherToken: base64.RawURLEncoding.EncodeToString(repeat(fill, 32)),
		ExpiresAt:     now.Add(time.Minute),
	}
}

func repeat(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}

type fixedClock struct {
	now time.Time
}

func (clock *fixedClock) Now() time.Time {
	return clock.now
}
