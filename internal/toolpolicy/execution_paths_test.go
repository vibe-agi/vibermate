package toolpolicy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

func mutatingCalls(t *testing.T, path string) []toolCall {
	t.Helper()
	return []toolCall{
		{"Write", argumentsJSON(t, map[string]any{"file_path": path, "content": "x"})},
		{"Edit", argumentsJSON(t, map[string]any{"file_path": path, "old_string": "a", "new_string": "b"})},
		{"NotebookEdit", argumentsJSON(t, map[string]any{"notebook_path": path, "new_source": "x"})},
	}
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// On the recognized basis a mutation of a file a local tool runs or loads as
// configuration is asked (Review) or refused (Strict). The exact basis keeps
// today's containment-only outcome: an existing file, or a file Write may
// create, inside the workspace is approved.
func TestExecutionBearingPathsAreAskedOnTheRecognizedBasis(t *testing.T) {
	t.Parallel()

	root := workspaceRoot(t)
	for _, relative := range []string{
		".git/hooks/pre-commit", ".git/config", ".claude/settings.json",
		".vscode/tasks.json", ".idea/workspace.xml", ".husky/pre-commit",
		".devcontainer/devcontainer.json", "sub/.envrc", ".mcp.json", ".hg/hgrc",
		"a/.git/config", "src/main.go",
	} {
		writeWorkspaceFile(t, root, relative, "a\n")
	}
	captured := capturedDefinitions(t)
	paths := []string{
		".git/hooks/pre-commit", ".GIT/hooks/x", ".claude/settings.json",
		".vscode/tasks.json", ".idea/workspace.xml", ".husky/pre-commit",
		".devcontainer/devcontainer.json", "sub/.envrc", ".mcp.json", ".hg/hgrc",
		"a/.git/config",
		// U+200C, which HFS+ ignores in a name: this is .git/hooks/x there.
		".g\u200cit/hooks/x",
	}
	link := filepath.Join(root, "link")
	linkErr := os.Symlink(filepath.Join(root, ".git"), link)
	if linkErr == nil {
		// An existing file and a new file under link -> .git.
		paths = append(paths, "link/hooks/pre-commit", "link/hooks/new-hook")
	}
	for _, relative := range paths {
		path := filepath.Join(root, filepath.FromSlash(relative))
		for _, call := range mutatingCalls(t, path) {
			t.Run(call.name+" "+relative, func(t *testing.T) {
				if approvedWithoutQuestion(t, root, clientadapter.ProtectionRecognizedClient, captured, call) {
					t.Fatal("the recognized basis approved a mutation of an execution-bearing path")
				}
				exact := pathExists(path) || call.name == "Write"
				if got := approvedWithoutQuestion(t, root, clientadapter.ProtectionExactRelease, captured, call); got != exact {
					t.Fatalf("the exact basis changed: approved %v, want %v", got, exact)
				}
			})
		}
	}
	if linkErr != nil {
		t.Logf("symlinks unavailable, link cases skipped: %v", linkErr)
	}

	read := toolCall{"Read", argumentsJSON(t, map[string]any{"file_path": filepath.Join(root, ".git", "config")})}
	if !approvedWithoutQuestion(t, root, clientadapter.ProtectionRecognizedClient, captured, read) {
		t.Fatal("reading .git/config was asked on the recognized basis")
	}
	for _, call := range mutatingCalls(t, filepath.Join(root, "src", "main.go")) {
		if call.name == "NotebookEdit" {
			continue
		}
		if !approvedWithoutQuestion(t, root, clientadapter.ProtectionRecognizedClient, captured, call) {
			t.Fatalf("%s of src/main.go was asked on the recognized basis", call.name)
		}
	}
}

func TestExecutionBearingComparesLexicalAndResolvedPaths(t *testing.T) {
	t.Parallel()

	root := workspaceRoot(t)
	writeWorkspaceFile(t, root, ".git/hooks/pre-commit", "a\n")
	writeWorkspaceFile(t, root, "src/main.go", "package main\n")
	for relative, want := range map[string]bool{
		"src/main.go":           false,
		"src/new.go":            false,
		"new/dir/file.txt":      false,
		"gitignore":             false,
		".gitignore":            false,
		".git":                  true,
		".git/hooks/pre-commit": true,
		".Git/HOOKS/x":          true,
		"deep/a/.Husky/x":       true,
		"x/.ENVRC":              true,
		".MCP.json":             true,
		// HFS+ ignores Unicode format characters in a name, as Git's
		// is_hfs_dotgit does, so these name the protected entries too.
		".g\u200cit/hooks/pre-commit": true, // U+200C ZERO WIDTH NON-JOINER
		".git\u200d/config":           true, // U+200D ZERO WIDTH JOINER
		"\ufeff.git/config":           true, // U+FEFF ZERO WIDTH NO-BREAK SPACE
		".hus\u206aky/pre-commit":     true, // U+206A INHIBIT SYMMETRIC SWAPPING
		"sub/.env\u202erc":            true, // U+202E RIGHT-TO-LEFT OVERRIDE
		".mcp\u200e.json":             true, // U+200E LEFT-TO-RIGHT MARK
		// Removing them must not make an unrelated name protected.
		".g\u200citignore":  false,
		"src/\u200bmain.go": false, // U+200B ZERO WIDTH SPACE
	} {
		if got := executionBearing(root, filepath.Join(root, filepath.FromSlash(relative))); got != want {
			t.Errorf("executionBearing(%q) = %v, want %v", relative, got, want)
		}
	}
	if executionBearing(root, root) {
		t.Error("the workspace root itself is execution-bearing")
	}

	if err := os.Symlink(filepath.Join(root, ".git", "hooks"), filepath.Join(root, "hooks")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink("loop", filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing-target"), filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	for relative, want := range map[string]bool{
		"hooks/pre-commit": true, // existing file through the link
		"hooks/new-hook":   true, // a file Write would create through the link
		"loop":             true, // resolution error
		"loop/x":           true, // resolution error below the leaf
		"dangling":         true, // the nearest existing ancestor does not resolve
	} {
		if got := executionBearing(root, filepath.Join(root, filepath.FromSlash(relative))); got != want {
			t.Errorf("executionBearing(%q) = %v, want %v", relative, got, want)
		}
	}
}
