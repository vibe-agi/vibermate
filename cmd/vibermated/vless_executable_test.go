package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func vlessFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	daemon := filepath.Join(root, "vibermated")
	kernelName := "vibermate-xray"
	if runtime.GOOS == "windows" {
		kernelName += ".exe"
	}
	kernel := filepath.Join(root, kernelName)
	for _, path := range []string{daemon, kernel} {
		if err := os.WriteFile(path, []byte("fixture, never executed"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return daemon, kernel
}

func requireVLESSPath(t *testing.T, explicit, daemon, want string) {
	t.Helper()
	got, err := resolveVLESSExecutable(explicit, daemon)
	if err != nil || got != want {
		t.Fatalf("got %q, err %v; want %q", got, err, want)
	}
}

func requireVLESSError(t *testing.T, explicit, daemon string) {
	t.Helper()
	got, err := resolveVLESSExecutable(explicit, daemon)
	if got != "" || err == nil {
		t.Fatalf("got %q, err %v; want empty path and error", got, err)
	}
	if len(err.Error()) > 160 || !strings.Contains(err.Error(), "VLESS") ||
		strings.Contains(err.Error(), explicit) && explicit != "" ||
		strings.Contains(err.Error(), daemon) {
		t.Fatalf("error must be bounded and omit input paths: %q", err)
	}
}

func TestResolveVLESSExecutableFindsPackagedSibling(t *testing.T) {
	daemon, kernel := vlessFixture(t)
	want, err := filepath.EvalSymlinks(kernel)
	if err != nil {
		t.Fatal(err)
	}
	requireVLESSPath(t, "", daemon, want)
}

func TestResolveVLESSExecutableResolvesDaemonLaunchSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Unix symlink fixture; Windows is cross-compile only")
	}
	daemon, kernel := vlessFixture(t)
	launch := filepath.Join(t.TempDir(), "vibermated")
	if err := os.Symlink(daemon, launch); err != nil {
		t.Fatal(err)
	}
	// A helper next to the launch alias must not override the installed sibling.
	if err := os.WriteFile(filepath.Join(filepath.Dir(launch), filepath.Base(kernel)), []byte("decoy"), 0755); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(kernel)
	if err != nil {
		t.Fatal(err)
	}
	requireVLESSPath(t, "", launch, want)
}

func TestResolveVLESSExecutableMissingOptionalSiblingDoesNotSearch(t *testing.T) {
	daemon, kernel := vlessFixture(t)
	if err := os.Remove(kernel); err != nil {
		t.Fatal(err)
	}
	_, decoy := vlessFixture(t)
	t.Setenv("PATH", filepath.Dir(decoy))
	t.Chdir(filepath.Dir(decoy))
	requireVLESSPath(t, "", daemon, "")
}

func TestResolveVLESSExecutableExplicitOverride(t *testing.T) {
	daemon, _ := vlessFixture(t)
	_, override := vlessFixture(t)
	want, err := filepath.EvalSymlinks(override)
	if err != nil {
		t.Fatal(err)
	}
	requireVLESSPath(t, override, daemon, want)
}

func TestResolveVLESSExecutableCanonicalizesParentAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Unix symlink fixture; Windows is cross-compile only")
	}
	daemon, kernel := vlessFixture(t)
	alias := filepath.Join(t.TempDir(), "package")
	if err := os.Symlink(filepath.Dir(kernel), alias); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(kernel)
	if err != nil {
		t.Fatal(err)
	}
	requireVLESSPath(t, filepath.Join(alias, filepath.Base(kernel)), daemon, want)
	requireVLESSPath(t, "", filepath.Join(alias, filepath.Base(daemon)), want)
}

func TestResolveVLESSExecutableRejectsInvalidExplicitWithoutFallback(t *testing.T) {
	daemon, kernel := vlessFixture(t)
	for _, path := range []string{
		"relative-xray",
		filepath.Dir(kernel) + string(os.PathSeparator) + "." + string(os.PathSeparator) + filepath.Base(kernel),
		filepath.Join(filepath.Dir(kernel), "missing-secret-node-path"),
	} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			requireVLESSError(t, path, daemon)
		})
	}
}

func TestResolveVLESSExecutableRejectsPresentInvalidHelper(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "dangling-symlink", "non-executable"} {
		t.Run(kind, func(t *testing.T) {
			if runtime.GOOS == "windows" && kind != "directory" {
				t.Skip("native Unix file mode/symlink fixture; Windows is cross-compile only")
			}
			daemon, kernel := vlessFixture(t)
			if err := os.Remove(kernel); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "directory":
				err = os.Mkdir(kernel, 0755)
			case "symlink":
				err = os.Symlink(daemon, kernel)
			case "dangling-symlink":
				err = os.Symlink(filepath.Join(filepath.Dir(kernel), "missing"), kernel)
			case "non-executable":
				err = os.WriteFile(kernel, []byte("fixture"), 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			requireVLESSError(t, "", daemon)
			requireVLESSError(t, kernel, daemon)
		})
	}
}
