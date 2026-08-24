package repositorycheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
)

// Composition packages, by import path. Names are never trusted: a file
// chooses what to call an import, so `vmruntime.Start` and
// `productruntime.Start` are the same call and only the path says so.
const (
	daemonImportPath           = "github.com/vibe-agi/vibermate/internal/desktopdaemon"
	hostImportPath             = "github.com/vibe-agi/vibermate/internal/desktophost"
	serverDaemonImportPath     = "github.com/vibe-agi/vibermate/internal/serverdaemon"
	serverHostImportPath       = "github.com/vibe-agi/vibermate/internal/serverhost"
	runtimeImportPath          = "github.com/vibe-agi/vibermate/internal/productruntime"
	controlPrincipalImportPath = "github.com/vibe-agi/vibermate/internal/controlprincipal"
	captureGrantImportPath     = "github.com/vibe-agi/vibermate/internal/capturegrant"
	captureControlImportPath   = "github.com/vibe-agi/vibermate/internal/capturecontrol"
	captureRunImportPath       = "github.com/vibe-agi/vibermate/internal/capturerun"
	manualCaptureImportPath    = "github.com/vibe-agi/vibermate/internal/manualcapture"
	loopbackProxyPackageDir    = "internal/loopbackproxy"
)

const (
	desktopMainFile         = "cmd/vibermated/main.go"
	daemonPackageDir        = "internal/desktopdaemon"
	hostPackageDir          = "internal/desktophost"
	serverDaemonPackageDir  = "internal/serverdaemon"
	serverHostPackageDir    = "internal/serverhost"
	runtimePackageDir       = "internal/productruntime"
	productionBuildersLocal = "productionBuilders"
)

// CheckProductionCompositionBoundary keeps the Desktop and headless Server
// entry chains explicit, and keeps them the only production composition paths.
//
//	main.main → desktopdaemon.Run → desktophost.Start → productruntime.Start
//	→ productionBuilders()
//	main.runServer → serverdaemon.Run → serverhost.Start → productruntime.Start
//	→ productionBuilders()
//
// The first version asked whether a package contained a call anywhere, and
// resolved selectors by receiver name. Both were bypassable and a review found
// them:
//
//   - a decoy satisfied it. `desktopdaemon.Run` could stop starting the host
//     entirely so long as some unused function in the package still called it;
//   - an alias defeated it. `import vmruntime ".../productruntime"` followed by
//     `vmruntime.Start(...)` was invisible, as was a dot import or taking the
//     function as a value.
//
// So a call must appear inside the exact function body that owes it, and
// selectors resolve through the file's own import declarations. A dot import of
// a composition package is refused outright rather than analysed, because it
// erases the qualifier the resolution depends on.
//
// It checks source shape and nothing else. It does not prove the packaged
// artefact for any commit runs.
func CheckProductionCompositionBoundary(repositoryRoot string) []Violation {
	files, err := productionGoFiles(repositoryRoot)
	if err != nil {
		return []Violation{{
			Rule:    "production-composition-boundary",
			Path:    ".",
			Message: "production composition could not be inspected",
		}}
	}

	var violations []Violation
	present := map[string]bool{}
	for _, file := range files {
		switch {
		case file.relative == desktopMainFile:
			present["main"] = true
		case strings.HasPrefix(file.relative, daemonPackageDir+"/"):
			present["daemon"] = true
		case strings.HasPrefix(file.relative, serverDaemonPackageDir+"/"):
			present["server-daemon"] = true
		case strings.HasPrefix(file.relative, serverHostPackageDir+"/"):
			present["server-host"] = true
		case strings.HasPrefix(file.relative, hostPackageDir+"/"):
			present["host"] = true
		case strings.HasPrefix(file.relative, runtimePackageDir+"/"):
			present["runtime"] = true
		case strings.HasPrefix(file.relative, "internal/controlprincipal/"):
			present["control-principal"] = true
		case strings.HasPrefix(file.relative, "internal/capturegrant/"):
			present["capture-grant"] = true
		case strings.HasPrefix(file.relative, "internal/capturecontrol/"):
			present["capture-control"] = true
		}
		violations = append(violations, file.dotImportViolations()...)
	}

	// Importing either capture aggregate here would recreate a mode-specific
	// data plane.
	// The listener consumes only the route-neutral shared admission authority.
	for _, file := range files {
		if !strings.HasPrefix(file.relative, loopbackProxyPackageDir+"/") {
			continue
		}
		for _, imported := range file.imports {
			if imported.path != captureRunImportPath &&
				imported.path != manualCaptureImportPath {
				continue
			}
			rule := "proxy-imports-capture-run"
			message := "the proxy authenticates through route-neutral capture admission, not the CaptureRun aggregate"
			if imported.path == manualCaptureImportPath {
				rule = "proxy-imports-manual-capture"
				message = "the proxy authenticates through route-neutral capture admission, not the ManualCapture aggregate"
			}
			violations = append(violations, Violation{
				Rule:    rule,
				Path:    file.relative,
				Line:    imported.line,
				Message: message,
			})
		}
	}

	// A ManualCapture can likewise be created only by the same typed grant
	// issuer. Owner scope must come from its authenticated principal rather than
	// a transport body or Host-specific handler.
	manualCreateCommand := calledFunction{
		importPath: manualCaptureImportPath,
		name:       "CreateCommand",
	}
	for _, file := range files {
		if strings.HasPrefix(file.relative, "internal/capturegrant/") ||
			strings.HasPrefix(file.relative, "internal/manualcapture/") {
			continue
		}
		if file.referencesSelector(manualCreateCommand) {
			violations = append(violations, Violation{
				Rule:    "manual-capture-create-outside-issuer",
				Path:    file.relative,
				Message: "production ManualCapture creation belongs to the typed capture-grant issuer",
			})
		}
	}

	// Each link names the exact function that owes the call, and who else may
	// make it. A caller outside the allowlist is a second composition path,
	// which is what "one chain" means.
	for _, link := range []compositionLink{{
		required: present["main"] && present["daemon"],
		holder:   desktopMainFile,
		function: "main",
		target:   calledFunction{importPath: daemonImportPath, name: "Run"},
		callers:  []string{desktopMainFile},
		missing:  "desktop-entry-does-not-run-the-daemon",
		extra:    "unreviewed-daemon-composition",
	}, {
		required: present["daemon"] && present["host"],
		holder:   daemonPackageDir,
		function: "Run",
		target:   calledFunction{importPath: hostImportPath, name: "Start"},
		callers:  []string{daemonPackageDir + "/"},
		missing:  "daemon-does-not-start-the-host",
		extra:    "unreviewed-host-composition",
	}, {
		required: present["host"] && present["runtime"],
		holder:   hostPackageDir,
		function: "Start",
		target:   calledFunction{importPath: runtimeImportPath, name: "Start"},
		callers: []string{
			hostPackageDir + "/", serverHostPackageDir + "/",
			runtimePackageDir + "/",
		},
		missing: "host-does-not-start-the-runtime",
		extra:   "unreviewed-runtime-composition",
	}, {
		required: present["host"] && present["control-principal"],
		holder:   hostPackageDir,
		function: "Start",
		target: calledFunction{
			importPath: controlPrincipalImportPath,
			name:       "NewAuthority",
		},
		callers: []string{hostPackageDir + "/", serverHostPackageDir + "/"},
		missing: "host-does-not-create-control-authority",
		extra:   "unreviewed-control-authority-composition",
	}, {
		required: present["host"] && present["capture-grant"],
		holder:   hostPackageDir,
		function: "Start",
		target: calledFunction{
			importPath: captureGrantImportPath,
			name:       "New",
		},
		callers: []string{hostPackageDir + "/", serverHostPackageDir + "/"},
		missing: "host-does-not-create-capture-grant-issuer",
		extra:   "unreviewed-capture-grant-composition",
	}, {
		required: present["host"] && present["capture-control"],
		holder:   hostPackageDir,
		function: "Start",
		target: calledFunction{
			importPath: captureControlImportPath,
			name:       "NewManualHandler",
		},
		callers: []string{hostPackageDir + "/", serverHostPackageDir + "/"},
		missing: "host-does-not-create-manual-capture-handler",
		extra:   "unreviewed-manual-capture-handler-composition",
	}, {
		required: present["host"] && present["capture-control"],
		holder:   hostPackageDir,
		function: "Start",
		target: calledFunction{
			importPath: captureControlImportPath,
			name:       "New",
		},
		callers: []string{hostPackageDir + "/", serverHostPackageDir + "/"},
		missing: "host-does-not-create-capture-control-handler",
		extra:   "unreviewed-capture-control-composition",
	}, {
		required: present["main"] && present["server-daemon"],
		holder:   desktopMainFile,
		function: "runServer",
		target: calledFunction{
			importPath: serverDaemonImportPath,
			name:       "Run",
		},
		callers: []string{desktopMainFile},
		missing: "server-entry-does-not-run-the-daemon",
		extra:   "unreviewed-server-daemon-composition",
	}, {
		required: present["server-daemon"] && present["server-host"],
		holder:   serverDaemonPackageDir,
		function: "Run",
		target: calledFunction{
			importPath: serverHostImportPath,
			name:       "Start",
		},
		callers: []string{serverDaemonPackageDir + "/"},
		missing: "server-daemon-does-not-start-the-host",
		extra:   "unreviewed-server-host-composition",
	}, {
		required: present["server-host"] && present["runtime"],
		holder:   serverHostPackageDir,
		function: "Start",
		target: calledFunction{
			importPath: runtimeImportPath,
			name:       "Start",
		},
		callers: []string{
			hostPackageDir + "/", serverHostPackageDir + "/",
			runtimePackageDir + "/",
		},
		missing: "server-host-does-not-start-the-runtime",
		extra:   "unreviewed-runtime-composition",
	}, {
		required: present["server-host"] && present["capture-grant"],
		holder:   serverHostPackageDir,
		function: "startAttached",
		target: calledFunction{
			importPath: captureGrantImportPath,
			name:       "New",
		},
		callers: []string{hostPackageDir + "/", serverHostPackageDir + "/"},
		missing: "server-host-does-not-create-capture-grant-issuer",
		extra:   "unreviewed-capture-grant-composition",
	}, {
		required: present["server-host"] && present["capture-control"],
		holder:   serverHostPackageDir,
		function: "startAttached",
		target: calledFunction{
			importPath: captureControlImportPath,
			name:       "NewManualHandler",
		},
		callers: []string{hostPackageDir + "/", serverHostPackageDir + "/"},
		missing: "server-host-does-not-create-manual-capture-handler",
		extra:   "unreviewed-manual-capture-handler-composition",
	}, {
		required: present["server-host"] && present["capture-control"],
		holder:   serverHostPackageDir,
		function: "startAttached",
		target: calledFunction{
			importPath: captureControlImportPath,
			name:       "New",
		},
		callers: []string{hostPackageDir + "/", serverHostPackageDir + "/"},
		missing: "server-host-does-not-create-capture-control-handler",
		extra:   "unreviewed-capture-control-composition",
	}} {
		violations = append(violations, link.check(files)...)
	}

	// The runtime's selection of production builders is a plain call in its
	// own package, so it needs no import resolution — only the same insistence
	// that it appear in the function that owes it.
	if present["runtime"] {
		violations = append(violations, checkLocalCallInFunction(
			files,
			runtimePackageDir,
			"Start",
			productionBuildersLocal,
			"runtime-does-not-select-production-builders",
		)...)
	}

	// A CaptureRun can be created only by the typed grant issuer. Tests may
	// construct manager commands directly, but production transports and Hosts
	// cannot regain a second create path by importing the command type.
	createCommand := calledFunction{
		importPath: captureRunImportPath,
		name:       "CreateCommand",
	}
	for _, file := range files {
		if strings.HasPrefix(file.relative, "internal/capturegrant/") ||
			strings.HasPrefix(file.relative, "internal/capturerun/") {
			continue
		}
		if file.referencesSelector(createCommand) {
			violations = append(violations, Violation{
				Rule:    "capture-run-create-outside-issuer",
				Path:    file.relative,
				Message: "production CaptureRun creation belongs to the typed capture-grant issuer",
			})
		}
	}

	// The Desktop entry point composes through the daemon. Reaching past it is
	// how a second chain begins, and it is checked by import path so an alias
	// does not hide it.
	for _, file := range files {
		if file.relative != desktopMainFile {
			continue
		}
		for _, imported := range file.imports {
			if imported.path != hostImportPath &&
				imported.path != runtimeImportPath {
				continue
			}
			violations = append(violations, Violation{
				Rule:    "desktop-entry-reaches-past-the-daemon",
				Path:    file.relative,
				Line:    imported.line,
				Message: "the Desktop entry point composes the product through desktopdaemon, not by importing the host or runtime itself",
			})
		}
		if present["daemon"] && !file.callsInFunction(
			"main",
			calledFunction{importPath: daemonImportPath, name: "ProductionOptions"},
		) {
			violations = append(violations, Violation{
				Rule:    "desktop-entry-skips-production-options",
				Path:    file.relative,
				Message: "the Desktop entry point starts the product from desktopdaemon.ProductionOptions",
			})
		}
		if present["server-daemon"] && !file.callsInFunction(
			"runServer",
			calledFunction{importPath: serverDaemonImportPath, name: "ProductionOptions"},
		) {
			violations = append(violations, Violation{
				Rule:    "server-entry-skips-production-options",
				Path:    file.relative,
				Message: "the Server entry point starts from serverdaemon.ProductionOptions",
			})
		}
	}
	return violations
}

type calledFunction struct {
	importPath string
	name       string
}

type compositionLink struct {
	required bool
	holder   string
	function string
	target   calledFunction
	callers  []string
	missing  string
	extra    string
}

func (link compositionLink) check(files []productionFile) []Violation {
	if !link.required {
		return nil
	}
	var violations []Violation
	satisfied := false
	for _, file := range files {
		inHolder := file.relative == link.holder ||
			strings.HasPrefix(file.relative, link.holder+"/")
		if inHolder && file.callsInFunction(link.function, link.target) {
			satisfied = true
		}
		// Anyone outside the allowlist reaching this step is a second path,
		// whether by calling it or by taking it as a value.
		if link.allows(file.relative) {
			continue
		}
		if file.referencesSelector(link.target) {
			violations = append(violations, Violation{
				Rule:    link.extra,
				Path:    file.relative,
				Message: "this composition step belongs to a reviewed caller; add a new one to the allowlist deliberately",
			})
		}
	}
	if !satisfied {
		violations = append(violations, Violation{
			Rule:    link.missing,
			Path:    link.holder,
			Message: "the composition call must appear in the function that owes this step, not merely somewhere in its package",
		})
	}
	return violations
}

func (link compositionLink) allows(relative string) bool {
	for _, caller := range link.callers {
		if strings.HasSuffix(caller, "/") {
			if strings.HasPrefix(relative, caller) {
				return true
			}
			continue
		}
		if relative == caller {
			return true
		}
	}
	return false
}

func checkLocalCallInFunction(
	files []productionFile,
	packageDir string,
	function string,
	callee string,
	rule string,
) []Violation {
	for _, file := range files {
		if !strings.HasPrefix(file.relative, packageDir+"/") {
			continue
		}
		body := file.functionBody(function)
		if body == nil {
			continue
		}
		found := false
		ast.Inspect(body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if identifier, ok := call.Fun.(*ast.Ident); ok &&
				identifier.Name == callee {
				found = true
			}
			return true
		})
		if found {
			return nil
		}
	}
	return []Violation{{
		Rule:    rule,
		Path:    packageDir,
		Message: "the selection must appear in the function that owes this step, not merely somewhere in its package",
	}}
}

type productionFile struct {
	relative string
	parsed   *ast.File
	imports  []productionImport
	// aliases maps the local name a file uses to the import path it refers to.
	// A file decides its own names, so this is the only reliable way to say
	// which package a selector belongs to.
	aliases map[string]string
	dotted  []productionImport
}

type productionImport struct {
	path string
	line int
}

// dotImportViolations refuses a dot import of a composition package rather
// than trying to analyse one. A dot import removes the qualifier that says
// which package a call belongs to, so every check here would silently stop
// seeing it.
func (file productionFile) dotImportViolations() []Violation {
	var violations []Violation
	for _, imported := range file.dotted {
		violations = append(violations, Violation{
			Rule:    "composition-dot-import",
			Path:    file.relative,
			Line:    imported.line,
			Message: "a composition package may not be dot-imported; the qualifier is what makes its calls visible",
		})
	}
	return violations
}

func (file productionFile) functionBody(name string) *ast.BlockStmt {
	for _, declaration := range file.parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil || function.Name.Name != name {
			continue
		}
		return function.Body
	}
	return nil
}

func (file productionFile) callsInFunction(
	function string,
	target calledFunction,
) bool {
	body := file.functionBody(function)
	if body == nil {
		return false
	}
	return file.selectorAppears(body, target)
}

// referencesSelector finds the step however it is reached: called directly,
// assigned to a variable, or handed to something else. `f := pkg.Start` never
// appears as a call at the reference site and reaches the same function.
func (file productionFile) referencesSelector(target calledFunction) bool {
	return file.selectorAppears(file.parsed, target)
}

func (file productionFile) selectorAppears(
	root ast.Node,
	target calledFunction,
) bool {
	found := false
	ast.Inspect(root, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != target.name {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		if file.aliases[identifier.Name] == target.importPath {
			found = true
		}
		return true
	})
	return found
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
				aliases:  map[string]string{},
			}
			for _, imported := range parsed.Imports {
				importPath := strings.Trim(imported.Path.Value, `"`)
				line := fileSet.Position(imported.Pos()).Line
				file.imports = append(file.imports, productionImport{
					path: importPath,
					line: line,
				})
				local := defaultImportName(importPath)
				if imported.Name != nil {
					local = imported.Name.Name
				}
				if local == "." {
					if isCompositionPackage(importPath) {
						file.dotted = append(file.dotted, productionImport{
							path: importPath,
							line: line,
						})
					}
					continue
				}
				if local == "_" {
					continue
				}
				file.aliases[local] = importPath
			}
			files = append(files, file)
			return nil
		},
	)
	return files, err
}

func isCompositionPackage(importPath string) bool {
	switch importPath {
	case daemonImportPath,
		hostImportPath,
		runtimeImportPath,
		controlPrincipalImportPath,
		captureGrantImportPath,
		captureControlImportPath,
		captureRunImportPath:
		return true
	default:
		return false
	}
}

func defaultImportName(importPath string) string {
	if index := strings.LastIndex(importPath, "/"); index >= 0 {
		return importPath[index+1:]
	}
	return importPath
}
