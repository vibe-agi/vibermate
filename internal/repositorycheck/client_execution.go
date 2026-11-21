package repositorycheck

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// clientDecisionPackages decide what a launched client is and what it may do.
// The only process on the verification path is the platform signature check,
// and it lives in internal/codesignature under its own rule.
var clientDecisionPackages = []string{
	"internal/clientadapter",
	"internal/capturegrant",
	"internal/captureadmission",
	"internal/capturecontrol",
	"internal/capturerun",
	"internal/toolpolicy",
}

// clientExecutionCalls are the standard-library entry points that start or
// replace a process without os/exec.
var clientExecutionCalls = map[string]map[string]struct{}{
	"os":      {"StartProcess": {}},
	"syscall": {"Exec": {}, "ForkExec": {}},
}

const clientExecutionMessage = "this package decides what a client is and what it may do; it must never run the client (ADR 0026)"

// CheckClientExecutionBoundary keeps process execution out of the packages
// that identify a client and decide its protections. Nothing a recognized
// client receives may rest on running it: its version output and help text
// are the client's own claims, and the file is writable by the user.
//
// Selectors resolve through each file's own imports, as the composition rule
// does, so an alias does not hide a call; a dot import of a process package is
// refused because it removes the qualifier the resolution depends on. Tests
// are exempt: a test that runs an installed client is what a test is for.
func CheckClientExecutionBoundary(repositoryRoot string) []Violation {
	const rule = "client-execution-boundary"
	fileSet := token.NewFileSet()
	var violations []Violation
	for _, relativeRoot := range clientDecisionPackages {
		root := filepath.Join(repositoryRoot, filepath.FromSlash(relativeRoot))
		if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
			continue
		}
		walkErr := filepath.WalkDir(root, func(
			path string,
			entry fs.DirEntry,
			err error,
		) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "testdata" || entry.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") ||
				strings.HasSuffix(name, "_test.go") {
				return nil
			}
			parsed, parseErr := parser.ParseFile(
				fileSet, path, nil, parser.SkipObjectResolution,
			)
			if parseErr != nil {
				// A file the compiler cannot read cannot ship either.
				return nil
			}
			relative := relativeDisplayPath(repositoryRoot, path)
			aliases := map[string]string{}
			for _, imported := range parsed.Imports {
				importPath, unquoteErr := strconv.Unquote(imported.Path.Value)
				if unquoteErr != nil {
					continue
				}
				line := fileSet.Position(imported.Pos()).Line
				if importPath == "os/exec" {
					violations = append(violations, Violation{
						Rule:    rule,
						Path:    relative,
						Line:    line,
						Message: clientExecutionMessage,
					})
					continue
				}
				local := defaultImportName(importPath)
				if imported.Name != nil {
					local = imported.Name.Name
				}
				if _, process := clientExecutionCalls[importPath]; process &&
					local == "." {
					violations = append(violations, Violation{
						Rule: rule,
						Path: relative,
						Line: line,
						Message: "a process package may not be dot-imported here; " +
							"the qualifier is what makes its calls visible",
					})
					continue
				}
				aliases[local] = importPath
			}
			ast.Inspect(parsed, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				identifier, ok := selector.X.(*ast.Ident)
				if !ok {
					return true
				}
				names, process := clientExecutionCalls[aliases[identifier.Name]]
				if !process {
					return true
				}
				if _, forbidden := names[selector.Sel.Name]; forbidden {
					violations = append(violations, Violation{
						Rule:    rule,
						Path:    relative,
						Line:    fileSet.Position(selector.Pos()).Line,
						Message: clientExecutionMessage,
					})
				}
				return true
			})
			return nil
		})
		if walkErr != nil {
			violations = append(violations, Violation{
				Rule:    rule,
				Path:    relativeRoot,
				Message: "client decision package could not be inspected",
			})
		}
	}
	return violations
}
