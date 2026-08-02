package repositorycheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
)

// CheckProductionCompositionBoundary keeps one chain from the Desktop entry
// point to the runtime, and keeps it the only one.
//
// Today it happens to be single:
//
//	cmd/vibermated → desktopdaemon.ProductionOptions/Run → desktophost.Start
//	→ productruntime.Start → productionBuilders()
//
// That is a fact about the current source, not a property anything enforces.
// Nothing stops a second path being added, nothing stops the Desktop entry
// point reaching past the daemon into the host or the runtime, and nothing
// stops a test builder or a development dependency being selected on the way.
// A shipped product assembled through an unreviewed path is the failure this
// exists to make impossible to introduce quietly.
//
// It checks source shape and nothing else. It does not prove the packaged
// artefact for any commit runs.
func CheckProductionCompositionBoundary(repositoryRoot string) []Violation {
	const (
		desktopMain        = "cmd/vibermated/main.go"
		daemonPackage      = "internal/desktopdaemon"
		hostPackage        = "internal/desktophost"
		runtimePackage     = "internal/productruntime"
		daemonImport       = "github.com/vibe-agi/vibermate/internal/desktopdaemon"
		hostImport         = "github.com/vibe-agi/vibermate/internal/desktophost"
		runtimeImport      = "github.com/vibe-agi/vibermate/internal/productruntime"
		productionBuilders = "productionBuilders"
	)

	files, err := productionGoFiles(repositoryRoot)
	if err != nil {
		return []Violation{{
			Rule:    "production-composition-boundary",
			Path:    ".",
			Message: "production composition could not be inspected",
		}}
	}

	var violations []Violation
	// Each link is assumed absent until seen, so deleting a call is a
	// violation rather than a silent pass. A guard that only rejects extra
	// paths would let the chain be cut instead of widened.
	sawDaemonStart := false
	sawHostStart := false
	sawRuntimeStart := false
	sawProductionBuilders := false

	// A link is only required where its component exists. Other boundary
	// fixtures carry a Desktop entry point without a daemon, and demanding the
	// whole chain of them would make this rule fire on repositories it has
	// nothing to say about — a guard reporting on what it was not asked to
	// look at is noise that gets suppressed rather than fixed.
	hasDesktopMain := false
	hasDaemon := false
	hasHost := false
	hasRuntime := false
	for _, file := range files {
		switch {
		case file.relative == desktopMain:
			hasDesktopMain = true
		case strings.HasPrefix(file.relative, daemonPackage+"/"):
			hasDaemon = true
		case strings.HasPrefix(file.relative, hostPackage+"/"):
			hasHost = true
		case strings.HasPrefix(file.relative, runtimePackage+"/"):
			hasRuntime = true
		}
	}

	for _, file := range files {
		switch {
		case file.relative == desktopMain:
			// The entry point may reach the daemon and nothing deeper.
			// Reaching past it is how a second composition begins.
			for _, imported := range file.imports {
				switch imported.path {
				case hostImport, runtimeImport:
					violations = append(violations, Violation{
						Rule:    "desktop-entry-reaches-past-the-daemon",
						Path:    file.relative,
						Line:    imported.line,
						Message: "the Desktop entry point composes the product through desktopdaemon, not by importing the host or runtime itself",
					})
				}
			}
			if callsSelector(file.parsed, "desktopdaemon", "Run") {
				sawDaemonStart = true
			}
			if hasDaemon &&
				!callsSelector(file.parsed, "desktopdaemon", "ProductionOptions") {
				violations = append(violations, Violation{
					Rule:    "desktop-entry-skips-production-options",
					Path:    file.relative,
					Message: "the Desktop entry point starts the product from desktopdaemon.ProductionOptions",
				})
			}

		case strings.HasPrefix(file.relative, daemonPackage+"/"):
			if callsSelector(file.parsed, "desktophost", "Start") {
				sawHostStart = true
			}

		case strings.HasPrefix(file.relative, hostPackage+"/"):
			if callsSelector(file.parsed, "productruntime", "Start") {
				sawRuntimeStart = true
			}

		case strings.HasPrefix(file.relative, runtimePackage+"/"):
			if callsFunction(file.parsed, productionBuilders) {
				sawProductionBuilders = true
			}
		}

		// Whoever else starts a runtime has to be a reviewed Host.
		//
		// The act that matters is the call, not the import. A package that
		// builds Options or names RuntimeStatus is using the runtime's types,
		// which several legitimately do; a package that calls Start composes a
		// product. Guarding the import instead would have flagged both and
		// taught everyone to widen the allowlist, which is the opposite of
		// what a short allowlist is for.
		if !runtimeCallerAllowed(file.relative) &&
			callsSelector(file.parsed, "productruntime", "Start") {
			violations = append(violations, Violation{
				Rule:    "unreviewed-runtime-composition",
				Path:    file.relative,
				Message: "ProductRuntime is started by a reviewed Host; add a new one to the allowlist deliberately",
			})
		}
	}

	for _, link := range []struct {
		required bool
		seen     bool
		rule     string
		path     string
		text     string
	}{
		{hasDesktopMain && hasDaemon, sawDaemonStart,
			"desktop-entry-does-not-run-the-daemon", desktopMain,
			"the Desktop entry point runs the product through desktopdaemon.Run"},
		{hasDaemon && hasHost, sawHostStart,
			"daemon-does-not-start-the-host", daemonPackage,
			"the daemon starts the product through desktophost.Start"},
		{hasHost && hasRuntime, sawRuntimeStart,
			"host-does-not-start-the-runtime", hostPackage,
			"the host starts the product through productruntime.Start"},
		{hasRuntime, sawProductionBuilders,
			"runtime-does-not-select-production-builders", runtimePackage,
			"the runtime selects productionBuilders on its production path"},
	} {
		if !link.required || link.seen {
			continue
		}
		violations = append(violations, Violation{
			Rule:    link.rule,
			Path:    link.path,
			Message: link.text,
		})
	}
	return violations
}

// runtimeCallerAllowed names what may compose a ProductRuntime.
//
// The runtime's own package is here because it builds itself; the Desktop host
// is the one reviewed Host today. A Server host would be added here, in a diff
// somebody reviews, rather than by simply writing the import.
func runtimeCallerAllowed(relative string) bool {
	switch {
	case strings.HasPrefix(relative, "internal/productruntime/"):
		return true
	case strings.HasPrefix(relative, "internal/desktophost/"):
		return true
	default:
		return false
	}
}

type productionFile struct {
	relative string
	parsed   *ast.File
	imports  []productionImport
}

type productionImport struct {
	path string
	line int
}

// productionGoFiles returns non-test Go files. Tests are excluded because a
// test composing a runtime directly is what a test is for; the guard is about
// what ships.
func productionGoFiles(repositoryRoot string) ([]productionFile, error) {
	fileSet := token.NewFileSet()
	var files []productionFile
	err := filepath.WalkDir(
		repositoryRoot,
		func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				switch entry.Name() {
				case ".git", "node_modules", "target", "testdata", "vendor":
					return filepath.SkipDir
				}
				return nil
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") ||
				strings.HasSuffix(name, "_test.go") {
				return nil
			}
			relative, err := filepath.Rel(repositoryRoot, path)
			if err != nil {
				return err
			}
			parsed, err := parser.ParseFile(fileSet, path, nil, 0)
			if err != nil {
				return nil
			}
			file := productionFile{
				relative: filepath.ToSlash(relative),
				parsed:   parsed,
			}
			for _, imported := range parsed.Imports {
				file.imports = append(file.imports, productionImport{
					path: strings.Trim(imported.Path.Value, `"`),
					line: fileSet.Position(imported.Pos()).Line,
				})
			}
			files = append(files, file)
			return nil
		},
	)
	return files, err
}

func callsSelector(file *ast.File, receiver, name string) bool {
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != name {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if ok && identifier.Name == receiver {
			found = true
		}
		return true
	})
	return found
}

func callsFunction(file *ast.File, name string) bool {
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if identifier, ok := call.Fun.(*ast.Ident); ok &&
			identifier.Name == name {
			found = true
		}
		return true
	})
	return found
}
