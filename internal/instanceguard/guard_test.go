//go:build unix

package instanceguard_test

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/instanceguard"
)

func TestGuardSerializesGenerationAndLeavesStablePrivateFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "runtime", "daemon.lock")
	first, err := instanceguard.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	if _, err := instanceguard.Acquire(path); !errors.Is(
		err,
		instanceguard.ErrAlreadyOwned,
	) {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := instanceguard.Acquire(path)
	if err != nil {
		t.Fatalf("Acquire() after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stable lock file was removed: %v", err)
	}
	if runtime.GOOS != "windows" {
		directory, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		lock, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if directory.Mode().Perm() != 0o700 || lock.Mode().Perm() != 0o600 {
			t.Fatalf(
				"private modes directory=%04o lock=%04o",
				directory.Mode().Perm(),
				lock.Mode().Perm(),
			)
		}
	}
}

func TestGuardRejectsUnsafeDirectoryAndSymlinkLock(t *testing.T) {
	t.Parallel()

	unsafeDirectory := filepath.Join(t.TempDir(), "unsafe")
	if err := os.Mkdir(unsafeDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := instanceguard.Acquire(
		filepath.Join(unsafeDirectory, "daemon.lock"),
	); err == nil {
		t.Fatal("Acquire() accepted a non-private runtime directory")
	}

	privateDirectory := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(privateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(privateDirectory, "daemon.lock")
	if err := os.Symlink(target, lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := instanceguard.Acquire(lockPath); err == nil {
		t.Fatal("Acquire() followed a symlink lock file")
	}
}

func TestGuardReleasesGenerationOwnershipAfterProcessKill(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "runtime", "daemon.lock")
	command := exec.Command(
		os.Args[0],
		"-test.run=^TestGuardHelperProcess$",
		"--",
		path,
	)
	command.Env = append(os.Environ(), "VIBEMATE_GUARD_HELPER=1")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if waited {
			return
		}
		_ = command.Process.Kill()
		_ = stdin.Close()
		_ = command.Wait()
	}()

	ready := make(chan error, 1)
	go func() {
		line, readErr := bufio.NewReader(stdout).ReadString('\n')
		switch {
		case readErr != nil:
			ready <- readErr
		case line != "ready\n":
			ready <- fmt.Errorf("unexpected helper readiness %q", line)
		default:
			ready <- nil
		}
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("wait for helper readiness: %v; stderr=%s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("helper readiness deadline exceeded; stderr=%s", stderr.String())
	}

	if _, err := instanceguard.Acquire(path); !errors.Is(
		err,
		instanceguard.ErrAlreadyOwned,
	) {
		t.Fatalf("Acquire() while helper owns lock error = %v", err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	_ = stdin.Close()
	if err := command.Wait(); err == nil {
		t.Fatal("killed helper exited successfully")
	}
	waited = true

	deadline := time.Now().Add(5 * time.Second)
	for {
		recovered, err := instanceguard.Acquire(path)
		if err == nil {
			if err := recovered.Release(); err != nil {
				t.Fatal(err)
			}
			break
		}
		if !errors.Is(err, instanceguard.ErrAlreadyOwned) {
			t.Fatalf("Acquire() after helper kill error = %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("generation ownership was not released after helper kill")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGuardHelperProcess(t *testing.T) {
	if os.Getenv("VIBEMATE_GUARD_HELPER") != "1" {
		return
	}
	if len(os.Args) == 0 {
		os.Exit(2)
	}
	guard, err := instanceguard.Acquire(os.Args[len(os.Args)-1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer guard.Release()
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		os.Exit(2)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}
