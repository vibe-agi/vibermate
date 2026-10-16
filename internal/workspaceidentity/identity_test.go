package workspaceidentity

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

var identityTestTime = time.Date(2026, time.August, 3, 8, 0, 0, 0, time.UTC)

func TestIdentityPersistsMachineAndDerivesStableScopedWorkspaces(t *testing.T) {
	t.Parallel()
	dataDirectory := t.TempDir()
	first, err := Open(
		context.Background(),
		dataDirectory,
		bytes.NewReader(bytes.Repeat([]byte{0x11}, identityBytes*2)),
		identityTestTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	firstScope, err := first.ResolveLocal(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	second, err := Open(
		context.Background(),
		dataDirectory,
		bytes.NewReader(bytes.Repeat([]byte{0x22}, identityBytes*2)),
		identityTestTime.Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	secondScope, err := second.ResolveLocal(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if firstScope.MachineID() != secondScope.MachineID() ||
		firstScope.WorkspaceID() != secondScope.WorkspaceID() ||
		firstScope.WorkspaceLabel() != filepath.Base(directory) ||
		firstScope.Evidence() != EvidenceLocalLauncher {
		t.Fatalf("identity changed across reopen: first=%+v second=%+v", firstScope, secondScope)
	}
	other := t.TempDir()
	otherScope, err := second.ResolveLocal(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	if otherScope.WorkspaceID() == secondScope.WorkspaceID() {
		t.Fatal("different canonical directories shared a WorkspaceID")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dataDirectory, identityFileName))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("identity permissions = %#o, want 0600", info.Mode().Perm())
		}
	}
}

func TestIdentitySeparatesInstallationsWithTheSameWorkspacePath(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	first, err := Open(
		context.Background(),
		t.TempDir(),
		bytes.NewReader(bytes.Repeat([]byte{0x31}, identityBytes*2)),
		identityTestTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(
		context.Background(),
		t.TempDir(),
		bytes.NewReader(bytes.Repeat([]byte{0x41}, identityBytes*2)),
		identityTestTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	left, err := first.ResolveLocal(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	right, err := second.ResolveLocal(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if left.MachineID() == right.MachineID() || left.WorkspaceID() == right.WorkspaceID() {
		t.Fatal("separate installations shared an identity")
	}
}

func TestIdentityRegeneratesCorruptStateAndFailsClosedAfterShutdown(t *testing.T) {
	t.Parallel()
	dataDirectory := t.TempDir()
	manager, err := Open(
		context.Background(),
		dataDirectory,
		bytes.NewReader(bytes.Repeat([]byte{0x51}, identityBytes*2)),
		identityTestTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	oldID := manager.MachineID()
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ResolveLocal(context.Background(), t.TempDir()); !errors.Is(err, ErrResolverStopped) {
		t.Fatalf("ResolveLocal error = %v", err)
	}

	identityPath := filepath.Join(dataDirectory, identityFileName)
	if err := os.WriteFile(identityPath, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	replacement, err := Open(
		context.Background(),
		dataDirectory,
		bytes.NewReader(bytes.Repeat([]byte{0x61}, identityBytes*2)),
		identityTestTime.Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.MachineID() == oldID {
		t.Fatal("corrupt state retained the old installation identity")
	}
}

func TestConcurrentFirstOpenReturnsOnePersistentMachineIdentity(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	results := make(chan *Manager, 2)
	errors := make(chan error, 2)
	for _, value := range []byte{0x71, 0x72} {
		go func(value byte) {
			manager, err := Open(
				context.Background(),
				directory,
				&gatedIdentityRandom{
					value: value, entered: entered, release: release,
				},
				identityTestTime,
			)
			if err != nil {
				errors <- err
				return
			}
			results <- manager
		}(value)
	}
	<-entered
	select {
	case <-entered:
	case <-time.After(250 * time.Millisecond):
		// A cross-process transaction allows only the winner to consume entropy;
		// the other opener will read its committed identity afterwards.
	}
	close(release)

	managers := make([]*Manager, 0, 2)
	for len(managers) < 2 {
		select {
		case err := <-errors:
			t.Fatal(err)
		case manager := <-results:
			managers = append(managers, manager)
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent identity opens did not finish")
		}
	}
	defer managers[0].Shutdown(context.Background())
	defer managers[1].Shutdown(context.Background())
	if managers[0].MachineID() != managers[1].MachineID() {
		t.Fatalf(
			"concurrent first opens returned %q and %q",
			managers[0].MachineID(),
			managers[1].MachineID(),
		)
	}
}

type gatedIdentityRandom struct {
	value   byte
	entered chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (random *gatedIdentityRandom) Read(destination []byte) (int, error) {
	random.once.Do(func() {
		random.entered <- struct{}{}
		<-random.release
	})
	for index := range destination {
		destination[index] = random.value
	}
	return len(destination), nil
}

func TestWorkspaceDisplayLabelsDoNotAlterExactPathIdentity(t *testing.T) {
	key := bytes.Repeat([]byte{0x51}, identityBytes)
	manager, err := Open(context.Background(), t.TempDir(), bytes.NewReader(bytes.Repeat([]byte{0x51}, identityBytes*2)), identityTestTime)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown(context.Background())
	for _, test := range []struct{ name, label string }{
		{strings.Repeat("a", 121), strings.Repeat("a", 120)},
		{strings.Repeat("a", 119) + "é", strings.Repeat("a", 119)},
		{strings.Repeat("界", 41), strings.Repeat("界", 40)},
		{" project ", "project"}, {"   ", "workspace"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), test.name)
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			scope, err := manager.ResolveLocal(context.Background(), directory)
			if err != nil {
				t.Fatalf("valid cwd rejected by display label: %v", err)
			}
			if scope.WorkspaceLabel() != test.label || !utf8.ValidString(scope.WorkspaceLabel()) {
				t.Fatalf("display label=%q; want %q", scope.WorkspaceLabel(), test.label)
			}
			canonical, err := filepath.EvalSymlinks(directory)
			if err != nil {
				t.Fatal(err)
			}
			mac := hmac.New(sha256.New, key)
			_, _ = mac.Write([]byte("vibermate.workspace/v1\x00" + runtime.GOOS + "\x00" + canonical))
			expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
			if scope.WorkspaceID().String() != expected {
				t.Fatal("display normalization altered exact canonical-path HMAC")
			}
		})
	}
}
