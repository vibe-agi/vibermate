package cliinstall

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDeliveryStrategiesKeepOwnershipPlatformNative(t *testing.T) {
	t.Parallel()
	tests := []struct {
		platform  Platform
		shape     PackageShape
		owner     Owner
		method    Method
		mutates   bool
		stableApp bool
	}{
		{PlatformDarwin, ShapeMacApp, OwnerDesktopApp, MethodManagedSymlink, true, true},
		{PlatformWindows, ShapeWindowsNSIS, OwnerInstaller, MethodInstallerPath, false, false},
		{PlatformWindows, ShapeWindowsMSI, OwnerInstaller, MethodInstallerPath, false, false},
		{PlatformLinux, ShapeLinuxDeb, OwnerPackageManager, MethodPackageBinary, false, false},
		{PlatformLinux, ShapeLinuxRPM, OwnerPackageManager, MethodPackageBinary, false, false},
		{PlatformLinux, ShapeLinuxAppImage, OwnerNone, MethodAbsoluteOnly, false, false},
		{PlatformDarwin, ShapePortable, OwnerNone, MethodAbsoluteOnly, false, false},
	}
	for _, test := range tests {
		test := test
		t.Run(string(test.platform)+"/"+string(test.shape), func(t *testing.T) {
			t.Parallel()
			strategy, err := ResolveStrategy(test.platform, test.shape)
			if err != nil {
				t.Fatal(err)
			}
			if strategy.Owner != test.owner ||
				strategy.Method != test.method ||
				strategy.RuntimeMutation != test.mutates ||
				strategy.RequiresStableApp != test.stableApp ||
				strategy.EditsShellProfile {
				t.Fatalf("strategy = %+v", strategy)
			}
		})
	}
	if _, err := ResolveStrategy(PlatformWindows, ShapeMacApp); err == nil {
		t.Fatal("accepted an impossible package/platform combination")
	}
}

func TestManagedLinkInstallRefreshAndRemove(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	fixture := newLinkFixture(t)
	manager := NewLinkManager(func() time.Time { return fixture.installedAt })
	profilePath := filepath.Join(fixture.root, ".zshrc")
	if err := os.WriteFile(profilePath, []byte("user profile\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	observation, err := manager.Inspect(fixture.spec)
	if err != nil || observation.State != StateNotInstalled {
		t.Fatalf("before install: observation=%+v err=%v", observation, err)
	}
	installed, err := manager.Install(fixture.spec)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Schema != receiptSchema ||
		installed.Owner != OwnerDesktopApp ||
		installed.Method != MethodManagedSymlink ||
		installed.InstalledAt != fixture.installedAt ||
		!validFileIdentity(installed.TargetIdentity) {
		t.Fatalf("installed record = %+v", installed)
	}
	assertCurrent(t, manager, fixture.spec)
	assertLinkDestination(t, fixture.spec.TargetPath, fixture.spec.SourcePath)
	assertMode(t, fixture.spec.ReceiptPath, 0o600)
	assertMode(t, filepath.Dir(fixture.spec.ReceiptPath), 0o700)
	if profile, err := os.ReadFile(profilePath); err != nil ||
		string(profile) != "user profile\n" {
		t.Fatalf("shell profile changed: data=%q err=%v", profile, err)
	}

	if err := writeExecutable(
		fixture.spec.SourcePath,
		"#!/bin/sh\nexit 1\n",
	); err != nil {
		t.Fatal(err)
	}
	fixture.spec.Version = "0.2.0"
	observation, err = manager.Inspect(fixture.spec)
	if err != nil || observation.State != StateSourceUpdated {
		t.Fatalf("after app update: observation=%+v err=%v", observation, err)
	}
	refreshedAt := fixture.installedAt.Add(time.Hour)
	manager.now = func() time.Time { return refreshedAt }
	updated, err := manager.AcknowledgeUpdate(
		fixture.spec,
		installed.SourceSHA256,
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != "0.2.0" ||
		updated.SourceSHA256 == installed.SourceSHA256 ||
		updated.TargetIdentity != installed.TargetIdentity ||
		updated.RefreshedAt != refreshedAt {
		t.Fatalf("updated record = %+v", updated)
	}
	assertCurrent(t, manager, fixture.spec)
	assertOnlyNames(
		t,
		filepath.Dir(fixture.spec.ReceiptPath),
		filepath.Base(fixture.spec.ReceiptPath),
	)

	result, err := manager.Remove(fixture.spec)
	if err != nil || result.State != RemoveRemoved {
		t.Fatalf("remove: result=%+v err=%v", result, err)
	}
	assertMissing(t, fixture.spec.TargetPath)
	assertMissing(t, fixture.spec.ReceiptPath)
	assertOnlyNames(t, filepath.Dir(fixture.spec.ReceiptPath))
}

func TestUserCommandOwnsOneUserLocalTerminalEntry(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	realRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(realRoot, "home")
	configuration := filepath.Join(realRoot, "configuration")
	source := filepath.Join(
		realRoot,
		"Applications",
		"ViberMate.app",
		"Contents",
		"MacOS",
		"vibermate",
	)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeExecutable(source, "#!/bin/sh\nexit 0\n"); err != nil {
		t.Fatal(err)
	}
	installedAt := time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC)
	command, err := NewUserCommand(
		source,
		home,
		configuration,
		"0.1.0+test",
		func() time.Time { return installedAt },
	)
	if err != nil {
		t.Fatal(err)
	}
	spec := command.Spec()
	if spec.TargetPath != filepath.Join(home, ".local", "bin", "vibermate") ||
		spec.ReceiptPath != filepath.Join(
			configuration,
			"io.vibermate.desktop",
			"terminal-command.json",
		) {
		t.Fatalf("user terminal command spec = %+v", spec)
	}
	observation, err := command.Inspect()
	if err != nil || observation.State != StateNotInstalled {
		t.Fatalf("initial observation = %+v, %v", observation, err)
	}
	receipt, err := command.Install()
	if err != nil {
		t.Fatal(err)
	}
	if receipt.TargetPath != spec.TargetPath || receipt.SourcePath != source {
		t.Fatalf("installed receipt = %+v", receipt)
	}
	observation, err = command.Inspect()
	if err != nil || observation.State != StateCurrent {
		t.Fatalf("installed observation = %+v, %v", observation, err)
	}
	if profileMatches, _ := filepath.Glob(filepath.Join(home, ".*rc")); len(profileMatches) != 0 {
		t.Fatalf("user terminal command created shell profiles: %v", profileMatches)
	}
	removed, err := command.Remove()
	if err != nil || removed.State != RemoveRemoved {
		t.Fatalf("remove = %+v, %v", removed, err)
	}
	observation, err = command.Inspect()
	if err != nil || observation.State != StateNotInstalled {
		t.Fatalf("removed observation = %+v, %v", observation, err)
	}
}

func TestUserCommandRepairsAStaleRecordAfterTheAppMoves(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	realRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(realRoot, "home")
	configuration := filepath.Join(realRoot, "configuration")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	packagedCommand := func(appName string) string {
		return filepath.Join(
			realRoot,
			appName+".app",
			"Contents",
			"MacOS",
			"vibermate",
		)
	}
	oldSource := packagedCommand("OldViberMate")
	currentSource := packagedCommand("ViberMate")
	for _, source := range []string{oldSource, currentSource} {
		if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := writeExecutable(source, "#!/bin/sh\nexit 0\n"); err != nil {
			t.Fatal(err)
		}
	}
	oldCommand, err := NewUserCommand(
		oldSource,
		home,
		configuration,
		"0.1.0",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oldCommand.Install(); err != nil {
		t.Fatal(err)
	}
	target := oldCommand.Spec().TargetPath
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(target)); err != nil {
		t.Fatal(err)
	}

	currentCommand, err := NewUserCommand(
		currentSource,
		home,
		configuration,
		"0.2.0",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := currentCommand.Inspect()
	if err != nil || observation.State != StateTargetMissing {
		t.Fatalf("Inspect = %+v, %v", observation, err)
	}
	if _, err := currentCommand.Repair(); err != nil {
		t.Fatal(err)
	}
	observation, err = currentCommand.Inspect()
	if err != nil || observation.State != StateCurrent {
		t.Fatalf("repaired observation = %+v, %v", observation, err)
	}
	assertLinkDestination(t, target, currentSource)
}

func TestUserCommandCanonicalizesAnExistingManagedSourceLink(t *testing.T) {
	t.Parallel()
	realRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(realRoot, "home")
	source := filepath.Join(
		realRoot,
		"Applications",
		"ViberMate.app",
		"Contents",
		"MacOS",
		"vibermate",
	)
	alias := filepath.Join(realRoot, "vibermate")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeExecutable(source, "#!/bin/sh\nexit 0\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, alias); err != nil {
		t.Fatal(err)
	}
	command, err := NewUserCommand(
		alias,
		home,
		filepath.Join(realRoot, "configuration"),
		"0.1.0+test",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if command.Spec().SourcePath != source {
		t.Fatalf("canonical source = %q, want %q", command.Spec().SourcePath, source)
	}
}

func TestManagedLinkNeverOverwritesExistingTerminalObject(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	tests := []struct {
		name   string
		create func(*testing.T, linkFixture)
	}{
		{
			name: "file",
			create: func(t *testing.T, fixture linkFixture) {
				t.Helper()
				if err := os.WriteFile(
					fixture.spec.TargetPath,
					[]byte("user-owned"),
					0o700,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			create: func(t *testing.T, fixture linkFixture) {
				t.Helper()
				other := filepath.Join(fixture.root, "other-command")
				if err := os.WriteFile(other, []byte("other"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, fixture.spec.TargetPath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "directory",
			create: func(t *testing.T, fixture linkFixture) {
				t.Helper()
				if err := os.Mkdir(fixture.spec.TargetPath, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := newLinkFixture(t)
			test.create(t, fixture)
			before, err := os.Lstat(fixture.spec.TargetPath)
			if err != nil {
				t.Fatal(err)
			}
			manager := NewLinkManager(nil)
			observation, err := manager.Inspect(fixture.spec)
			if err != nil || observation.State != StateUnownedTarget {
				t.Fatalf("inspect: observation=%+v err=%v", observation, err)
			}
			if _, err := manager.Install(fixture.spec); err == nil {
				t.Fatal("Install replaced an existing terminal object")
			}
			after, err := os.Lstat(fixture.spec.TargetPath)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf(
					"existing object changed: before=%v after=%v err=%v",
					before,
					after,
					err,
				)
			}
			assertMissing(t, fixture.spec.ReceiptPath)
		})
	}
}

func TestInstallRefusesExistingPrivateRecordBeforeCreatingLink(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	fixture := newLinkFixture(t)
	if err := os.MkdirAll(filepath.Dir(fixture.spec.ReceiptPath), 0o700); err != nil {
		t.Fatal(err)
	}
	external := []byte("external record\n")
	if err := os.WriteFile(fixture.spec.ReceiptPath, external, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLinkManager(nil).Install(fixture.spec); err == nil {
		t.Fatal("Install accepted an existing private record")
	}
	assertMissing(t, fixture.spec.TargetPath)
	data, err := os.ReadFile(fixture.spec.ReceiptPath)
	if err != nil || string(data) != string(external) {
		t.Fatalf("external record changed: data=%q err=%v", data, err)
	}
}

func TestInstallValidatesPathsAndPackagedCommandIdentity(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	t.Run("relative terminal path", func(t *testing.T) {
		fixture := newLinkFixture(t)
		fixture.spec.TargetPath = "vibermate"
		if _, err := NewLinkManager(nil).Install(fixture.spec); err == nil {
			t.Fatal("accepted a relative terminal path")
		}
	})
	t.Run("unclean path", func(t *testing.T) {
		fixture := newLinkFixture(t)
		fixture.spec.ReceiptPath = fixture.root +
			"/state/../state/record.json"
		if _, err := NewLinkManager(nil).Install(fixture.spec); err == nil {
			t.Fatal("accepted an unclean record path")
		}
	})
	t.Run("wrong app layout", func(t *testing.T) {
		fixture := newLinkFixture(t)
		wrong := filepath.Join(fixture.root, "app", "vibermate")
		if err := os.MkdirAll(filepath.Dir(wrong), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := writeExecutable(wrong, "command"); err != nil {
			t.Fatal(err)
		}
		fixture.spec.SourcePath = wrong
		if _, err := NewLinkManager(nil).Install(fixture.spec); err == nil {
			t.Fatal("accepted a command outside a .app bundle")
		}
	})
	t.Run("source path traverses symlink", func(t *testing.T) {
		fixture := newLinkFixture(t)
		alias := filepath.Join(fixture.root, "Alias.app")
		appRoot := filepath.Join(fixture.root, "ViberMate.app")
		if err := os.Symlink(appRoot, alias); err != nil {
			t.Fatal(err)
		}
		fixture.spec.SourcePath = filepath.Join(
			alias,
			"Contents",
			"MacOS",
			"vibermate",
		)
		if _, err := NewLinkManager(nil).Install(fixture.spec); err == nil {
			t.Fatal("accepted a packaged-command path through a symlink")
		}
	})
	t.Run("non executable source", func(t *testing.T) {
		fixture := newLinkFixture(t)
		if err := os.Chmod(fixture.spec.SourcePath, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewLinkManager(nil).Install(fixture.spec); err == nil {
			t.Fatal("accepted a non-executable packaged command")
		}
	})
	t.Run("hard linked source", func(t *testing.T) {
		fixture := newLinkFixture(t)
		if err := os.Link(
			fixture.spec.SourcePath,
			filepath.Join(fixture.root, "second-name"),
		); err != nil {
			t.Fatal(err)
		}
		if _, err := NewLinkManager(nil).Install(fixture.spec); err == nil {
			t.Fatal("accepted a multiply-named packaged command")
		}
	})
	t.Run("oversized source", func(t *testing.T) {
		fixture := newLinkFixture(t)
		file, err := os.OpenFile(fixture.spec.SourcePath, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxCLIBytes + 1); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := NewLinkManager(nil).Install(fixture.spec); err == nil {
			t.Fatal("accepted an oversized packaged command")
		}
	})
}

func TestStableMacAppLayoutsAreAcceptedByPathPolicy(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		"/Applications/ViberMate.app/Contents/MacOS/vibermate",
		"/Users/example/Applications/ViberMate.app/Contents/MacOS/vibermate",
	} {
		source := source
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			spec := LinkSpec{
				SourcePath:  source,
				TargetPath:  "/usr/local/bin/vibermate",
				ReceiptPath: "/Users/example/Library/Application Support/ViberMate/terminal-command.json",
				Version:     "1.0.0",
			}
			if err := validateSpec(spec); err != nil {
				t.Fatalf("validateSpec() error = %v", err)
			}
		})
	}
}

func TestTransientMacAppLayoutsAreRejectedByPathPolicy(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		"/Volumes/ViberMate/ViberMate.app/Contents/MacOS/vibermate",
		"/Users/example/Downloads/ViberMate.app/Contents/MacOS/vibermate",
		"/private/var/folders/AppTranslocation/ViberMate.app/Contents/MacOS/vibermate",
		"/Users/example/.Trash/ViberMate.app/Contents/MacOS/vibermate",
	} {
		source := source
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			spec := LinkSpec{
				SourcePath:  source,
				TargetPath:  "/usr/local/bin/vibermate",
				ReceiptPath: "/Users/example/Library/Application Support/ViberMate/terminal-command.json",
				Version:     "1.0.0",
			}
			if err := validateSpec(spec); err == nil {
				t.Fatal("validateSpec accepted a temporary app location")
			}
		})
	}
}

func TestInstallRefusesAliasedMutationDirectories(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	t.Run("terminal directory", func(t *testing.T) {
		fixture := newLinkFixture(t)
		realDirectory := filepath.Join(fixture.root, "real-bin")
		alias := filepath.Join(fixture.root, "alias-bin")
		if err := os.Mkdir(realDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realDirectory, alias); err != nil {
			t.Fatal(err)
		}
		fixture.spec.TargetPath = filepath.Join(alias, "vibermate")
		if _, err := NewLinkManager(nil).Install(fixture.spec); err == nil {
			t.Fatal("accepted an aliased terminal directory")
		}
		assertMissing(t, filepath.Join(realDirectory, "vibermate"))
	})
	t.Run("installation-record directory", func(t *testing.T) {
		fixture := newLinkFixture(t)
		realDirectory := filepath.Join(fixture.root, "real-state")
		alias := filepath.Join(fixture.root, "alias-state")
		if err := os.Mkdir(realDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realDirectory, alias); err != nil {
			t.Fatal(err)
		}
		fixture.spec.ReceiptPath = filepath.Join(alias, "record.json")
		if _, err := NewLinkManager(nil).Install(fixture.spec); err == nil {
			t.Fatal("accepted an aliased installation-record directory")
		}
		assertMissing(t, fixture.spec.TargetPath)
	})
}

func TestPrivateRecordPermissionsAndLinkCountAreEnforced(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	t.Run("record mode", func(t *testing.T) {
		fixture := newLinkFixture(t)
		manager := NewLinkManager(nil)
		if _, err := manager.Install(fixture.spec); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(fixture.spec.ReceiptPath, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Inspect(fixture.spec); err == nil ||
			!strings.Contains(err.Error(), "accessible by another user") {
			t.Fatalf("Inspect error = %v", err)
		}
		assertLinkDestination(t, fixture.spec.TargetPath, fixture.spec.SourcePath)
	})
	t.Run("directory mode", func(t *testing.T) {
		fixture := newLinkFixture(t)
		manager := NewLinkManager(nil)
		if _, err := manager.Install(fixture.spec); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(
			filepath.Dir(fixture.spec.ReceiptPath),
			0o755,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Inspect(fixture.spec); err == nil ||
			!strings.Contains(err.Error(), "accessible by another user") {
			t.Fatalf("Inspect error = %v", err)
		}
	})
	t.Run("second record name", func(t *testing.T) {
		fixture := newLinkFixture(t)
		manager := NewLinkManager(nil)
		if _, err := manager.Install(fixture.spec); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(
			fixture.spec.ReceiptPath,
			fixture.spec.ReceiptPath+".copy",
		); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Inspect(fixture.spec); err == nil ||
			!strings.Contains(err.Error(), "more than one filesystem name") {
			t.Fatalf("Inspect error = %v", err)
		}
	})
}

func TestMalformedPrivateRecordFailsClosed(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	tests := []struct {
		name   string
		mutate func(*testing.T, []byte) []byte
	}{
		{
			name: "unknown field",
			mutate: func(t *testing.T, input []byte) []byte {
				t.Helper()
				var record map[string]any
				if err := json.Unmarshal(input, &record); err != nil {
					t.Fatal(err)
				}
				record["unexpected"] = true
				output, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				return output
			},
		},
		{
			name: "trailing value",
			mutate: func(_ *testing.T, input []byte) []byte {
				return append(input, []byte("{}\n")...)
			},
		},
		{
			name: "invalid identity",
			mutate: func(t *testing.T, input []byte) []byte {
				t.Helper()
				var record map[string]any
				if err := json.Unmarshal(input, &record); err != nil {
					t.Fatal(err)
				}
				record["targetIdentity"] = "same destination is not identity"
				output, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				return output
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := newLinkFixture(t)
			manager := NewLinkManager(nil)
			if _, err := manager.Install(fixture.spec); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(fixture.spec.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(
				fixture.spec.ReceiptPath,
				test.mutate(t, data),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Inspect(fixture.spec); err == nil {
				t.Fatal("Inspect accepted a malformed private record")
			}
			if _, err := manager.Remove(fixture.spec); err == nil {
				t.Fatal("Remove acted on a malformed private record")
			}
			assertLinkDestination(
				t,
				fixture.spec.TargetPath,
				fixture.spec.SourcePath,
			)
		})
	}
}

func TestSameDestinationReplacementIsConflictAndIsNeverRemoved(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	fixture := newLinkFixture(t)
	manager := NewLinkManager(nil)
	installed, err := manager.Install(fixture.spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fixture.spec.TargetPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		fixture.spec.SourcePath,
		fixture.spec.TargetPath,
	); err != nil {
		t.Fatal(err)
	}
	replacement, err := inspectTarget(fixture.spec.TargetPath)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.metadata.identity == installed.TargetIdentity {
		t.Fatal("test replacement unexpectedly reused the original filesystem identity")
	}
	observation, err := manager.Inspect(fixture.spec)
	if err != nil || observation.State != StateConflict ||
		!strings.Contains(observation.Detail, "replaced outside") {
		t.Fatalf("Inspect = %+v, %v", observation, err)
	}
	result, err := manager.Remove(fixture.spec)
	if err != nil || result.State != RemoveConflict {
		t.Fatalf("Remove = %+v, %v", result, err)
	}
	assertLinkDestination(t, fixture.spec.TargetPath, fixture.spec.SourcePath)
	if _, err := os.Lstat(fixture.spec.ReceiptPath); err != nil {
		t.Fatalf("private record was removed: %v", err)
	}
}

func TestMissingTargetCanForgetOnlyItsPrivateRecord(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	fixture := newLinkFixture(t)
	manager := NewLinkManager(nil)
	if _, err := manager.Install(fixture.spec); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fixture.spec.TargetPath); err != nil {
		t.Fatal(err)
	}
	observation, err := manager.Inspect(fixture.spec)
	if err != nil || observation.State != StateTargetMissing {
		t.Fatalf("Inspect = %+v, %v", observation, err)
	}
	result, err := manager.Remove(fixture.spec)
	if err != nil || result.State != RemoveMissing {
		t.Fatalf("Remove = %+v, %v", result, err)
	}
	assertMissing(t, fixture.spec.ReceiptPath)
}

func TestMissingTargetRecordFromPreviousAppLocationCanBeRepaired(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	fixture := newLinkFixture(t)
	manager := NewLinkManager(nil)
	if _, err := manager.Install(fixture.spec); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fixture.spec.TargetPath); err != nil {
		t.Fatal(err)
	}

	current := fixture.spec
	current.SourcePath = filepath.Join(
		fixture.root,
		"CurrentViberMate.app",
		"Contents",
		"MacOS",
		"vibermate",
	)
	if err := os.MkdirAll(filepath.Dir(current.SourcePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeExecutable(current.SourcePath, "#!/bin/sh\nexit 0\n"); err != nil {
		t.Fatal(err)
	}

	observation, err := manager.Inspect(current)
	if err != nil || observation.State != StateTargetMissing || observation.Receipt == nil {
		t.Fatalf("Inspect = %+v, %v", observation, err)
	}
	if observation.Receipt.SourcePath != fixture.spec.SourcePath {
		t.Fatalf("stale record source = %q", observation.Receipt.SourcePath)
	}
	removed, err := manager.Remove(current)
	if err != nil || removed.State != RemoveMissing {
		t.Fatalf("Remove = %+v, %v", removed, err)
	}
	if _, err := manager.Install(current); err != nil {
		t.Fatal(err)
	}
	assertCurrent(t, manager, current)
	assertLinkDestination(t, current.TargetPath, current.SourcePath)
}

func TestMissingTargetNeverClaimsARecordForAnotherTerminalEntry(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	fixture := newLinkFixture(t)
	manager := NewLinkManager(nil)
	other := fixture.spec
	other.TargetPath = filepath.Join(fixture.root, "other-bin", "vibermate")
	if err := os.MkdirAll(filepath.Dir(other.TargetPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(other); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(other.TargetPath); err != nil {
		t.Fatal(err)
	}

	observation, err := manager.Inspect(fixture.spec)
	if err != nil || observation.State != StateConflict {
		t.Fatalf("Inspect = %+v, %v", observation, err)
	}
	removed, err := manager.Remove(fixture.spec)
	if err != nil || removed.State != RemoveConflict {
		t.Fatalf("Remove = %+v, %v", removed, err)
	}
	if _, err := os.Lstat(fixture.spec.ReceiptPath); err != nil {
		t.Fatalf("record for another target was removed: %v", err)
	}
}

func TestOwnedLinkCanBeRemovedAfterAppLocationDisappears(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	fixture := newLinkFixture(t)
	manager := NewLinkManager(nil)
	if _, err := manager.Install(fixture.spec); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fixture.spec.SourcePath); err != nil {
		t.Fatal(err)
	}
	observation, err := manager.Inspect(fixture.spec)
	if err != nil || observation.State != StateSourceMissing {
		t.Fatalf("Inspect = %+v, %v", observation, err)
	}
	result, err := manager.Remove(fixture.spec)
	if err != nil || result.State != RemoveRemoved {
		t.Fatalf("Remove = %+v, %v", result, err)
	}
	assertMissing(t, fixture.spec.TargetPath)
	assertMissing(t, fixture.spec.ReceiptPath)
}

func TestReceiptPublishFailureRollsBackOnlyTheExactNewLink(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	t.Run("unchanged link is rolled back", func(t *testing.T) {
		fixture := newLinkFixture(t)
		external := []byte("external record\n")
		var hookErr error
		manager := NewLinkManager(nil)
		manager.beforeReceiptPublish = func() {
			hookErr = os.WriteFile(fixture.spec.ReceiptPath, external, 0o600)
		}
		if _, err := manager.Install(fixture.spec); err == nil {
			t.Fatal("Install succeeded despite an externally published record")
		}
		if hookErr != nil {
			t.Fatal(hookErr)
		}
		assertMissing(t, fixture.spec.TargetPath)
		data, err := os.ReadFile(fixture.spec.ReceiptPath)
		if err != nil || string(data) != string(external) {
			t.Fatalf("external record changed: data=%q err=%v", data, err)
		}
	})
	t.Run("same-destination replacement is preserved", func(t *testing.T) {
		fixture := newLinkFixture(t)
		external := []byte("external record\n")
		var hookErr error
		var createdIdentity string
		var replacementIdentity string
		manager := NewLinkManager(nil)
		manager.beforeReceiptPublish = func() {
			created, err := inspectTarget(fixture.spec.TargetPath)
			if err != nil {
				hookErr = err
				return
			}
			createdIdentity = created.metadata.identity
			if err := os.Remove(fixture.spec.TargetPath); err != nil {
				hookErr = err
				return
			}
			if err := os.Symlink(
				fixture.spec.SourcePath,
				fixture.spec.TargetPath,
			); err != nil {
				hookErr = err
				return
			}
			replacement, err := inspectTarget(fixture.spec.TargetPath)
			if err != nil {
				hookErr = err
				return
			}
			replacementIdentity = replacement.metadata.identity
			hookErr = os.WriteFile(
				fixture.spec.ReceiptPath,
				external,
				0o600,
			)
		}
		if _, err := manager.Install(fixture.spec); err == nil {
			t.Fatal("Install succeeded despite concurrent replacement")
		}
		if hookErr != nil {
			t.Fatal(hookErr)
		}
		if createdIdentity == replacementIdentity {
			t.Fatal("test replacement reused the created link identity")
		}
		assertLinkDestination(
			t,
			fixture.spec.TargetPath,
			fixture.spec.SourcePath,
		)
		data, err := os.ReadFile(fixture.spec.ReceiptPath)
		if err != nil || string(data) != string(external) {
			t.Fatalf("external record changed: data=%q err=%v", data, err)
		}
	})
}

func TestRefreshUsesAtomicCompareAndSwap(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	fixture := newLinkFixture(t)
	manager := NewLinkManager(func() time.Time { return fixture.installedAt })
	installed, err := manager.Install(fixture.spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeExecutable(
		fixture.spec.SourcePath,
		"updated command",
	); err != nil {
		t.Fatal(err)
	}
	fixture.spec.Version = "0.2.0"
	external := installed
	external.Version = "external-version"
	external.RefreshedAt = fixture.installedAt.Add(time.Minute)
	var hookErr error
	manager.beforeReceiptExchange = func() {
		data, err := marshalReceipt(external)
		if err != nil {
			hookErr = err
			return
		}
		temporary := fixture.spec.ReceiptPath + ".external"
		if err := os.WriteFile(temporary, data, 0o600); err != nil {
			hookErr = err
			return
		}
		hookErr = os.Rename(temporary, fixture.spec.ReceiptPath)
	}
	if _, err := manager.AcknowledgeUpdate(
		fixture.spec,
		installed.SourceSHA256,
	); err == nil {
		t.Fatal("refresh overwrote a concurrently replaced installation record")
	}
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	current, err := loadReceipt(fixture.spec.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !sameReceipt(current, external) {
		t.Fatalf(
			"concurrent record was not restored: got=%+v want=%+v",
			current,
			external,
		)
	}
	assertOnlyNames(
		t,
		filepath.Dir(fixture.spec.ReceiptPath),
		filepath.Base(fixture.spec.ReceiptPath),
	)
}

func TestRefreshSupportsVersionOnlyAppUpdateAndRejectsStalePreview(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	fixture := newLinkFixture(t)
	manager := NewLinkManager(func() time.Time { return fixture.installedAt })
	installed, err := manager.Install(fixture.spec)
	if err != nil {
		t.Fatal(err)
	}
	fixture.spec.Version = "0.1.1"
	observation, err := manager.Inspect(fixture.spec)
	if err != nil || observation.State != StateSourceUpdated {
		t.Fatalf("Inspect = %+v, %v", observation, err)
	}
	if _, err := manager.AcknowledgeUpdate(
		fixture.spec,
		strings.Repeat("0", 64),
	); err == nil {
		t.Fatal("refresh accepted a stale update preview")
	}
	updated, err := manager.AcknowledgeUpdate(
		fixture.spec,
		installed.SourceSHA256,
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.SourceSHA256 != installed.SourceSHA256 ||
		updated.Version != fixture.spec.Version {
		t.Fatalf("updated record = %+v", updated)
	}
	assertCurrent(t, manager, fixture.spec)
}

func TestConcurrentInstallAndRefreshHaveOneWinner(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	t.Run("install", func(t *testing.T) {
		fixture := newLinkFixture(t)
		managers := []*LinkManager{NewLinkManager(nil), NewLinkManager(nil)}
		errorsByCall := runConcurrently(2, func(index int) error {
			_, err := managers[index].Install(fixture.spec)
			return err
		})
		if countNil(errorsByCall) != 1 {
			t.Fatalf("Install errors = %v", errorsByCall)
		}
		assertCurrent(t, NewLinkManager(nil), fixture.spec)
	})
	t.Run("refresh", func(t *testing.T) {
		fixture := newLinkFixture(t)
		manager := NewLinkManager(func() time.Time { return fixture.installedAt })
		installed, err := manager.Install(fixture.spec)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeExecutable(
			fixture.spec.SourcePath,
			"updated command",
		); err != nil {
			t.Fatal(err)
		}
		fixture.spec.Version = "0.2.0"
		managers := []*LinkManager{NewLinkManager(nil), NewLinkManager(nil)}
		errorsByCall := runConcurrently(2, func(index int) error {
			_, err := managers[index].AcknowledgeUpdate(
				fixture.spec,
				installed.SourceSHA256,
			)
			return err
		})
		if countNil(errorsByCall) != 1 {
			t.Fatalf("AcknowledgeUpdate errors = %v", errorsByCall)
		}
		assertCurrent(t, NewLinkManager(nil), fixture.spec)
	})
}

func TestRemoveRechecksLinkIdentityAtMutationBoundary(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	fixture := newLinkFixture(t)
	manager := NewLinkManager(nil)
	installed, err := manager.Install(fixture.spec)
	if err != nil {
		t.Fatal(err)
	}
	var hookErr error
	var replacementIdentity string
	manager.beforeTargetMove = func() {
		if err := os.Remove(fixture.spec.TargetPath); err != nil {
			hookErr = err
			return
		}
		if err := os.Symlink(
			fixture.spec.SourcePath,
			fixture.spec.TargetPath,
		); err != nil {
			hookErr = err
			return
		}
		replacement, err := inspectTarget(fixture.spec.TargetPath)
		if err != nil {
			hookErr = err
			return
		}
		replacementIdentity = replacement.metadata.identity
	}
	result, err := manager.Remove(fixture.spec)
	if err != nil || result.State != RemoveConflict {
		t.Fatalf("Remove = %+v, %v", result, err)
	}
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if replacementIdentity == installed.TargetIdentity {
		t.Fatal("test replacement reused the installed link identity")
	}
	assertLinkDestination(t, fixture.spec.TargetPath, fixture.spec.SourcePath)
	if _, err := os.Lstat(fixture.spec.ReceiptPath); err != nil {
		t.Fatalf("private record was removed: %v", err)
	}
}

func TestConcurrentRemoveIsIdempotent(t *testing.T) {
	requireManagedLinkTestPlatform(t)
	fixture := newLinkFixture(t)
	if _, err := NewLinkManager(nil).Install(fixture.spec); err != nil {
		t.Fatal(err)
	}
	results := make([]RemoveResult, 2)
	errorsByCall := runConcurrently(2, func(index int) error {
		result, err := NewLinkManager(nil).Remove(fixture.spec)
		results[index] = result
		return err
	})
	if countNil(errorsByCall) != 2 {
		t.Fatalf("Remove errors = %v", errorsByCall)
	}
	states := map[RemoveState]int{}
	for _, result := range results {
		states[result.State]++
	}
	if states[RemoveRemoved] != 1 || states[RemoveMissing] != 1 {
		t.Fatalf("Remove results = %+v", results)
	}
	assertMissing(t, fixture.spec.TargetPath)
	assertMissing(t, fixture.spec.ReceiptPath)
}

type linkFixture struct {
	root        string
	spec        LinkSpec
	installedAt time.Time
}

func newLinkFixture(t *testing.T) linkFixture {
	t.Helper()
	realRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(
		realRoot,
		"ViberMate.app",
		"Contents",
		"MacOS",
		"vibermate",
	)
	targetDirectory := filepath.Join(realRoot, "bin")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeExecutable(source, "#!/bin/sh\nexit 0\n"); err != nil {
		t.Fatal(err)
	}
	return linkFixture{
		root: realRoot,
		spec: LinkSpec{
			SourcePath: source,
			TargetPath: filepath.Join(targetDirectory, "vibermate"),
			ReceiptPath: filepath.Join(
				realRoot,
				"private-state",
				"terminal-command.json",
			),
			Version: "0.1.0",
		},
		installedAt: time.Date(2026, 8, 3, 2, 3, 4, 0, time.UTC),
	}
}

func requireManagedLinkTestPlatform(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("managed symbolic links are the macOS delivery strategy")
	}
}

func writeExecutable(path, content string) error {
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func assertCurrent(t *testing.T, manager *LinkManager, spec LinkSpec) {
	t.Helper()
	observation, err := manager.Inspect(spec)
	if err != nil ||
		observation.State != StateCurrent ||
		observation.Receipt == nil {
		t.Fatalf("Inspect = %+v, %v", observation, err)
	}
}

func assertLinkDestination(t *testing.T, path, expected string) {
	t.Helper()
	destination, err := os.Readlink(path)
	if err != nil || destination != expected {
		t.Fatalf(
			"Readlink(%q) = %q, %v; want %q",
			path,
			destination,
			err,
			expected,
		)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%q still exists: %v", path, err)
	}
}

func assertMode(t *testing.T, path string, expected os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != expected {
		t.Fatalf(
			"mode(%q) = %04o, want %04o",
			path,
			info.Mode().Perm(),
			expected,
		)
	}
}

func assertOnlyNames(t *testing.T, directory string, expected ...string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(expected) {
		t.Fatalf(
			"entries in %q = %v, want %v",
			directory,
			entryNames(entries),
			expected,
		)
	}
	for index, entry := range entries {
		if entry.Name() != expected[index] {
			t.Fatalf(
				"entries in %q = %v, want %v",
				directory,
				entryNames(entries),
				expected,
			)
		}
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func runConcurrently(count int, operation func(int) error) []error {
	errorsByCall := make([]error, count)
	ready := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(count)
	for index := 0; index < count; index++ {
		index := index
		go func() {
			defer wait.Done()
			<-ready
			errorsByCall[index] = operation(index)
		}()
	}
	close(ready)
	wait.Wait()
	return errorsByCall
}

func countNil(values []error) int {
	count := 0
	for _, value := range values {
		if value == nil {
			count++
		}
	}
	return count
}
