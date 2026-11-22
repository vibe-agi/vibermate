package toolpolicy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Some files inside a workspace run as code, or are loaded as executable
// configuration, on an ordinary local action, and are often outside the
// reviewed diff: Git and Husky hooks and Git config (core.fsmonitor,
// core.hooksPath), editor tasks and run configurations, direnv, Claude Code's
// own project hooks and MCP servers, and dev-container definitions. On the
// recognized basis a mutation there is asked (Review) or refused (Strict),
// never approved without a question (ADR 0026). Exact releases keep the
// containment-only rule.
var executionBearingDirectories = []string{
	".git", ".hg", ".husky", ".vscode", ".idea", ".claude", ".devcontainer",
}

var executionBearingFiles = []string{".envrc", ".mcp.json"}

// executionBearing reports whether candidate (absolute, cleaned, already proven
// inside realRoot) names or lies under an execution-bearing path. It checks
// both the lexical path and its resolved form: EvalSymlinks of the path, or,
// for a file Write may create, EvalSymlinks of the nearest existing ancestor
// (found with Lstat, as insideWorkspace does) joined with the remaining
// components. Every component of each path relative to realRoot is compared
// case-insensitively: the recognized tier exists only on macOS, whose default
// file system ignores case, so ".GIT/hooks" is ".git/hooks". Any resolution
// error counts as execution-bearing.
func executionBearing(realRoot, candidate string) bool {
	if executionBearingRelative(realRoot, candidate) {
		return true
	}
	resolved, err := resolvedPath(candidate)
	if err != nil {
		return true
	}
	return executionBearingRelative(realRoot, resolved)
}

// resolvedPath is candidate with every symlink resolved. A path that does not
// exist yet resolves through its nearest existing ancestor, which a Write
// would follow when it creates the file.
func resolvedPath(candidate string) (string, error) {
	resolved, err := filepath.EvalSymlinks(candidate)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	ancestor := candidate
	var remaining []string
	for {
		_, lstatErr := os.Lstat(ancestor)
		if lstatErr == nil {
			realAncestor, ancestorErr := filepath.EvalSymlinks(ancestor)
			if ancestorErr != nil {
				return "", ancestorErr
			}
			return filepath.Join(append([]string{realAncestor}, remaining...)...), nil
		}
		parent := filepath.Dir(ancestor)
		if !errors.Is(lstatErr, os.ErrNotExist) || parent == ancestor {
			return "", lstatErr
		}
		remaining = append([]string{filepath.Base(ancestor)}, remaining...)
		ancestor = parent
	}
}

// executionBearingRelative compares every component of path relative to
// root. A path that is not inside root counts as execution-bearing, so a
// resolution that left the workspace fails closed.
func executionBearingRelative(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return true
	}
	if relative == "." {
		return false
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		for _, name := range executionBearingDirectories {
			if strings.EqualFold(component, name) {
				return true
			}
		}
		for _, name := range executionBearingFiles {
			if strings.EqualFold(component, name) {
				return true
			}
		}
	}
	return false
}
