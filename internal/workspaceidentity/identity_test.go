package workspaceidentity

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
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
