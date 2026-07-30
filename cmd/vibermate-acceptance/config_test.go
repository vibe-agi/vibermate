package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppBundlePathRequiresPackagedMembers(t *testing.T) {
	t.Parallel()

	bundle := filepath.Join(t.TempDir(), "VibeMate.app")
	executableDirectory := filepath.Join(bundle, "Contents", "MacOS")
	if err := os.MkdirAll(executableDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(bundle, "Contents", "Info.plist"),
		[]byte("plist"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"vibermate-desktop",
		"vibermated",
		"vibermate",
	} {
		if err := os.WriteFile(
			filepath.Join(executableDirectory, name),
			[]byte("binary-"+name),
			0o700,
		); err != nil {
			t.Fatal(err)
		}
	}
	canonical, err := appBundlePath(bundle)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := filepath.EvalSymlinks(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if canonical != expected {
		t.Fatalf("canonical bundle = %q", canonical)
	}
	if err := os.Chmod(
		filepath.Join(executableDirectory, "vibermated"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := appBundlePath(bundle); err == nil {
		t.Fatal("App bundle without an executable was accepted")
	}
}

func TestPackagedExecutableInputCannotSelectAnotherBinary(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	packaged := filepath.Join(directory, "VibeMate.app", "Contents", "MacOS", "vibermated")
	external := filepath.Join(directory, "external-vibermated")
	for _, path := range []string{packaged, external} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	canonicalPackaged, err := executablePath(packaged)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePackagedExecutableInput(
		"daemon",
		"",
		canonicalPackaged,
	); err != nil {
		t.Fatalf("omitted compatibility input was rejected: %v", err)
	}
	if err := validatePackagedExecutableInput(
		"daemon",
		packaged,
		canonicalPackaged,
	); err != nil {
		t.Fatalf("exact packaged member was rejected: %v", err)
	}
	if err := validatePackagedExecutableInput(
		"daemon",
		external,
		canonicalPackaged,
	); err == nil {
		t.Fatal("external daemon was accepted for the selected App")
	}
}

func TestDefaultAcceptanceRouteUsesApprovedRelay(t *testing.T) {
	t.Parallel()

	defaults := defaultConfig()
	if defaults.providerOrigin != defaultProviderOrigin ||
		defaults.providerModel != defaultProviderModel {
		t.Fatalf(
			"default route origin=%q model=%q",
			defaults.providerOrigin,
			defaults.providerModel,
		)
	}
	aggregate := assemblyAccess(defaults, 0)
	if len(aggregate.ProviderTargets) != 1 ||
		aggregate.ProviderTargets[0].Origin != defaultProviderOrigin ||
		len(aggregate.Profiles) != 1 ||
		aggregate.Profiles[0].DefaultModelPolicy.FixedModel !=
			defaultProviderModel {
		t.Fatalf("default executable route = %+v", aggregate)
	}
}
