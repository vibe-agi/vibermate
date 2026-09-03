package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const testDesktopPreferencesEnvironmentID = "test.environment"

func TestPackagedDesktopInvocationIsolatesAppDataWithoutReplacingLoginHome(t *testing.T) {
	t.Parallel()

	arguments := desktopOpenArguments(
		"/private/tmp/ViberMate.app",
		"/private/tmp/vibermate-home",
	)
	if !slices.Contains(
		arguments,
		"CFFIXED_USER_HOME=/private/tmp/vibermate-home",
	) {
		t.Fatalf("Desktop open arguments = %v", arguments)
	}
	if slices.Contains(arguments, "HOME=/private/tmp/vibermate-home") {
		t.Fatalf("Desktop invocation replaced the login HOME: %v", arguments)
	}
	if !slices.Contains(arguments, "-F") {
		t.Fatalf("Desktop open arguments do not disable saved-window restore: %v", arguments)
	}
	if arguments[len(arguments)-1] != "/private/tmp/ViberMate.app" {
		t.Fatalf("Desktop App argument = %q", arguments[len(arguments)-1])
	}
}

func TestDesktopPreferencesFixtureProvesAtomicCanonicalRewrite(t *testing.T) {
	t.Parallel()

	homeDirectory := filepath.Join(t.TempDir(), "home")
	path := desktopPreferencesStatePath(homeDirectory)
	seed, err := publishDesktopPreferencesFixture(
		path,
		staleDesktopPreferences(),
	)
	if err != nil {
		t.Fatal(err)
	}
	expected := canonicalDesktopPreferences(
		desktopPreferencesRestoreLanguage,
		desktopPreferencesRestoreSection,
		nil,
		nil,
	)
	if bytes.Equal(seed.encoded, expected) {
		t.Fatal("preference seed was already canonical")
	}
	staleEnvironmentID := testDesktopPreferencesEnvironmentID
	assertDesktopPreferencesValue(
		t,
		seed.encoded,
		desktopPreferencesRestoreLanguage,
		desktopPreferencesRestoreSection,
		&staleEnvironmentID,
	)

	committed, err := publishDesktopPreferencesFixture(path, expected)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(seed.info, committed.info) {
		t.Fatal("preference fixture replaced contents without replacing the inode")
	}
	observed, err := waitForDesktopPreferencesRewrite(
		context.Background(),
		make(chan error),
		path,
		seed,
		expected,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(committed.info, observed.info) {
		t.Fatal("preference observation returned the wrong committed file")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("preference permission=%04o", info.Mode().Perm())
	}
}

func TestDesktopPreferencesObservationFailsOnPrematureExit(t *testing.T) {
	t.Parallel()

	path := desktopPreferencesStatePath(filepath.Join(t.TempDir(), "home"))
	seed, err := publishDesktopPreferencesFixture(
		path,
		staleDesktopPreferences(),
	)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	done <- nil
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := waitForDesktopPreferencesRewrite(
		ctx,
		done,
		path,
		seed,
		canonicalDesktopPreferences(
			desktopPreferencesRestoreLanguage,
			desktopPreferencesRestoreSection,
			nil,
			nil,
		),
	); err == nil {
		t.Fatal("premature packaged Desktop exit was accepted")
	}
}

func TestDesktopPreferencesFixtureRejectsSymbolicLinkDestination(t *testing.T) {
	t.Parallel()

	homeDirectory := filepath.Join(t.TempDir(), "home")
	path := desktopPreferencesStatePath(homeDirectory)
	if _, err := publishDesktopPreferencesFixture(
		path,
		staleDesktopPreferences(),
	); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.json")
	if err := os.WriteFile(target, canonicalDesktopPreferences("en-US", "captures", nil, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := publishDesktopPreferencesFixture(
		path,
		canonicalDesktopPreferences(
			desktopPreferencesRestoreLanguage,
			desktopPreferencesRestoreSection,
			nil,
			nil,
		),
	); err == nil {
		t.Fatal("symbolic-link preference destination was accepted")
	}
}

func staleDesktopPreferences() []byte {
	environmentID := testDesktopPreferencesEnvironmentID
	return canonicalDesktopPreferences(
		desktopPreferencesRestoreLanguage,
		desktopPreferencesRestoreSection,
		&environmentID,
		nil,
	)
}

func TestDesktopPreferencesStaleEnvironmentIdentityIsFreshAndBounded(t *testing.T) {
	t.Parallel()

	identity, err := newDesktopPreferencesStaleEnvironmentID()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(identity, "acceptance.") {
		t.Fatalf("preference identity = %q", identity)
	}
	encodedIdentity := strings.TrimPrefix(identity, "acceptance.")
	decoded, err := hex.DecodeString(encodedIdentity)
	if err != nil || len(decoded) != 16 {
		t.Fatalf("preference environment identity = %q, error = %v", encodedIdentity, err)
	}
	if identity == testDesktopPreferencesEnvironmentID {
		t.Fatalf("preference identity reused a static value: %q", identity)
	}
}

func TestDesktopApplicationIdentityOutputIsClosedAndExact(t *testing.T) {
	t.Parallel()

	payload := []byte(`[
  {"processId":101,"bundlePath":"/Applications/ViberMate.app","executablePath":"/Applications/ViberMate.app/Contents/MacOS/ViberMate"},
  {"processId":202,"bundlePath":"/private/tmp/ViberMate.app","executablePath":"/private/tmp/ViberMate.app/Contents/MacOS/ViberMate"}
]`)
	applications, err := parseDesktopApplications(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(applications) != 2 || applications[0].ProcessID != 101 ||
		applications[1].ProcessID != 202 {
		t.Fatalf("Desktop applications = %+v", applications)
	}
	for _, invalid := range [][]byte{
		[]byte(`[{"processId":0,"bundlePath":"/a","executablePath":"/b"}]`),
		[]byte(`[
  {"processId":101,"bundlePath":"/a","executablePath":"/b"},
  {"processId":101,"bundlePath":"/c","executablePath":"/d"}
]`),
		[]byte(`[{"processId":101,"bundlePath":"relative","executablePath":"/b"}]`),
		[]byte(`[{"processId":101,"bundlePath":"/a","executablePath":"/b","extra":true}]`),
		[]byte(`[] trailing`),
	} {
		if _, err := parseDesktopApplications(invalid); err == nil {
			t.Fatalf("accepted invalid Desktop process output %q", invalid)
		}
	}
	if applications, err := parseDesktopApplications([]byte(`[]`)); err != nil ||
		applications == nil || len(applications) != 0 {
		t.Fatalf("empty Desktop process output = %v, %v", applications, err)
	}

	identity := desktopApplicationIdentity{
		ProcessID:      4242,
		BundlePath:     "/private/tmp/ViberMate.app",
		ExecutablePath: "/private/tmp/ViberMate.app/Contents/MacOS/ViberMate",
	}
	query := desktopApplicationsScript()
	guardian := desktopApplicationGuardianScript(identity)
	if !strings.Contains(query, desktopBundleID) ||
		!strings.Contains(guardian, desktopBundleID) ||
		!strings.Contains(guardian, "=== 4242") ||
		!strings.Contains(guardian, identity.BundlePath) ||
		!strings.Contains(guardian, identity.ExecutablePath) ||
		!strings.Contains(guardian, "matched.terminate") ||
		!strings.Contains(guardian, "matched.forceTerminate") ||
		!strings.Contains(guardian, "availableData") ||
		strings.Contains(guardian, "tell application id") {
		t.Fatalf(
			"Desktop application scripts are not exact: query=%q guardian=%q",
			query,
			guardian,
		)
	}
}

func TestDesktopProcessExitRequiresTheBoundBirthIdentityToDisappear(t *testing.T) {
	t.Parallel()

	expected := desktopProcessStart{seconds: 41, microseconds: 73}
	for name, test := range map[string]struct {
		snapshot desktopProcessSnapshot
		err      error
		present  bool
		wantErr  bool
	}{
		"same process": {
			snapshot: desktopProcessSnapshot{started: expected},
			present:  true,
		},
		"PID reused": {
			snapshot: desktopProcessSnapshot{
				started: desktopProcessStart{seconds: 42, microseconds: 73},
			},
		},
		"process gone": {err: errDesktopProcessUnavailable},
		"inspection failed": {
			err:     errors.New("process table unavailable"),
			wantErr: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			present, err := desktopProcessIdentityPresent(
				expected,
				test.snapshot,
				test.err,
			)
			if present != test.present {
				t.Fatalf("present = %v, want %v", present, test.present)
			}
			if !test.wantErr && err != nil {
				t.Fatal(err)
			}
			if test.wantErr && err == nil {
				t.Fatal("process inspection failure was treated as an exit")
			}
		})
	}
	if _, err := desktopProcessIdentityPresent(
		desktopProcessStart{},
		desktopProcessSnapshot{},
		nil,
	); err == nil {
		t.Fatal("invalid process birth identity was accepted")
	}
}

func TestPackagedDesktopApplicationSelectionRequiresSidecarParentAndExactPath(
	t *testing.T,
) {
	t.Parallel()

	appPath := filepath.Join(t.TempDir(), "ViberMate.app")
	executablePath := filepath.Join(appPath, "Contents", "MacOS", "ViberMate")
	if err := os.MkdirAll(filepath.Dir(executablePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executablePath, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	canonicalAppPath, err := canonicalDesktopBundlePath(appPath)
	if err != nil {
		t.Fatal(err)
	}
	canonicalExecutablePath, err := canonicalDesktopExecutablePath(executablePath)
	if err != nil {
		t.Fatal(err)
	}
	expected := desktopApplicationIdentity{
		ProcessID:      4242,
		BundlePath:     canonicalAppPath,
		ExecutablePath: canonicalExecutablePath,
	}

	selected, err := selectPackagedDesktopApplication(
		[]desktopApplicationIdentity{expected},
		9999,
		canonicalAppPath,
	)
	if err != nil || selected.ProcessID != 0 {
		t.Fatalf("unrelated unique Desktop was adopted: %+v, %v", selected, err)
	}

	selected, err = selectPackagedDesktopApplication(
		[]desktopApplicationIdentity{
			expected,
			{
				ProcessID:      5252,
				BundlePath:     "/Applications/ViberMate.app",
				ExecutablePath: "/Applications/ViberMate.app/Contents/MacOS/ViberMate",
			},
		},
		expected.ProcessID,
		canonicalAppPath,
	)
	if err == nil || selected.ProcessID != expected.ProcessID {
		t.Fatalf("overlap did not retain only the bound process: %+v, %v", selected, err)
	}

	wrong := expected
	wrong.BundlePath = filepath.Dir(canonicalAppPath)
	selected, err = selectPackagedDesktopApplication(
		[]desktopApplicationIdentity{wrong},
		wrong.ProcessID,
		canonicalAppPath,
	)
	if err == nil || selected.ProcessID != 0 {
		t.Fatalf("wrong App path was accepted or made killable: %+v, %v", selected, err)
	}
}

func assertDesktopPreferencesValue(
	t *testing.T,
	encoded []byte,
	language string,
	section string,
	environmentID *string,
) {
	t.Helper()
	var state desktopWorkbenchPreferences
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		t.Fatal(err)
	}
	if state.Schema != desktopPreferencesSchema ||
		state.Language != language ||
		state.Theme != desktopPreferencesTheme ||
		state.Section != section ||
		!equalOptionalString(state.SelectedEnvironmentID, environmentID) {
		t.Fatalf("workbench preferences=%+v", state)
	}
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
