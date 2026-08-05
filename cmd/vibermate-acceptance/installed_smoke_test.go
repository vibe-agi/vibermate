package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInstalledSmokePathsStayInExactRunnerChildren(t *testing.T) {
	runner := filepath.Join(t.TempDir(), "runner")
	app := filepath.Join(
		runner,
		"vibermate-install-root-1-1",
		"Applications",
		"ViberMate.app",
	)
	home := filepath.Join(runner, "vibermate-install-home-1-1")
	report := filepath.Join(
		runner,
		"vibermate-install-state-1-1",
		installedSmokeReport,
	)
	if err := validateInstalledSmokePaths(runner, app, home, report); err != nil {
		t.Fatal(err)
	}
	for name, changed := range map[string]string{
		"real Applications": filepath.Join("/Applications", "ViberMate.app"),
		"wrong App":         filepath.Join(runner, "vibermate-install-root-1-1", "ViberMate.app"),
		"broad home":        runner,
		"wrong report":      filepath.Join(runner, "vibermate-install-state-1-1", "other.json"),
	} {
		t.Run(name, func(t *testing.T) {
			candidateApp, candidateHome, candidateReport := app, home, report
			switch name {
			case "real Applications", "wrong App":
				candidateApp = changed
			case "broad home":
				candidateHome = changed
			case "wrong report":
				candidateReport = changed
			}
			if err := validateInstalledSmokePaths(
				runner,
				candidateApp,
				candidateHome,
				candidateReport,
			); err == nil {
				t.Fatal("unsafe installed smoke path was accepted")
			}
		})
	}
}

func TestInstalledSmokeEvidenceIsPrivateAndClosed(t *testing.T) {
	temporary, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(temporary, "state", installedSmokeReport)
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := openInstalledSmokeStateRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	evidence := installedSmokeEvidence{
		Schema:                installedSmokeSchema,
		Status:                "passed",
		Launches:              2,
		Readiness:             "launcher-discovery-and-router-mounted",
		NavigationPersistence: true,
		GracefulExit:          true,
		IsolatedHome:          true,
	}
	if err := writeInstalledSmokeEvidence(root, evidence); err != nil {
		t.Fatal(err)
	}
	metadata, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Mode().Perm() != 0o600 {
		t.Fatalf("smoke report mode = %o", metadata.Mode().Perm())
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 7 || decoded["schema"] != installedSmokeSchema {
		t.Fatalf("smoke evidence = %+v", decoded)
	}
	broken := evidence
	broken.GracefulExit = false
	brokenTemporary, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	brokenDirectory := filepath.Join(brokenTemporary, "broken")
	if err := os.Mkdir(brokenDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	brokenRoot, err := os.OpenRoot(brokenDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer brokenRoot.Close()
	if err := writeInstalledSmokeEvidence(
		brokenRoot,
		broken,
	); err == nil {
		t.Fatal("incomplete smoke evidence was written")
	}
}

func TestInstalledSmokeEvidenceRootDoesNotFollowReplacement(t *testing.T) {
	temporary, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stateDirectory := filepath.Join(temporary, "state")
	movedDirectory := filepath.Join(temporary, "moved-state")
	replacementDirectory := filepath.Join(temporary, "replacement")
	for _, directory := range []string{stateDirectory, replacementDirectory} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	root, err := openInstalledSmokeStateRoot(
		filepath.Join(stateDirectory, installedSmokeReport),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Rename(stateDirectory, movedDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(replacementDirectory, stateDirectory); err != nil {
		t.Fatal(err)
	}
	evidence := installedSmokeEvidence{
		Schema:                installedSmokeSchema,
		Status:                "passed",
		Launches:              2,
		Readiness:             "launcher-discovery-and-router-mounted",
		NavigationPersistence: true,
		GracefulExit:          true,
		IsolatedHome:          true,
	}
	if err := writeInstalledSmokeEvidence(root, evidence); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(movedDirectory, installedSmokeReport)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(replacementDirectory, installedSmokeReport)); !os.IsNotExist(err) {
		t.Fatal("smoke report followed a replaced state path")
	}
}
