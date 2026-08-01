// Package repositorycheck implements deterministic source and locale checks.
package repositorycheck

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

var (
	ErrCheckFailed           = errors.New("repository check failed")
	placeholderRE            = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
	jsxTextRE                = regexp.MustCompile(`>\s*([\pL][^<>{}]*)\s*</`)
	jsxExpressionLiteralRE   = regexp.MustCompile(">\\s*\\{\\s*[\"'`]([^\"'`]+)[\"'`]\\s*\\}\\s*</")
	jsxStaticUserAttributeRE = regexp.MustCompile(
		`\b(?:alt|aria-label|placeholder|title)\s*=\s*["']([^"']+)["']`,
	)
)

// Violation is one deterministic repository policy failure.
type Violation struct {
	Rule    string
	Path    string
	Line    int
	Message string
}

func (v Violation) String() string {
	if v.Line > 0 {
		return fmt.Sprintf("%s:%d: %s: %s", v.Path, v.Line, v.Rule, v.Message)
	}
	return fmt.Sprintf("%s: %s: %s", v.Path, v.Rule, v.Message)
}

// Check runs all checks that protect production shapes present in this slice.
func Check(repositoryRoot string) error {
	var violations []Violation
	violations = append(violations, CheckProductionEnglish(repositoryRoot)...)
	violations = append(violations, CheckProtocolSDKIsolation(repositoryRoot)...)
	violations = append(violations, CheckExternalEgressGate(repositoryRoot)...)
	violations = append(violations, CheckDataPlaneAccessBoundary(repositoryRoot)...)
	violations = append(violations, CheckDesktopFrontendBoundary(repositoryRoot)...)
	violations = append(violations, CheckSystemTrustBoundary(repositoryRoot)...)
	violations = append(violations, CheckPayloadDispatchBoundary(repositoryRoot)...)
	violations = append(violations, CheckIdentityComposition(repositoryRoot)...)
	violations = append(
		violations,
		CheckCatalogPair(
			filepath.Join(repositoryRoot, "locales", "en-US.json"),
			filepath.Join(repositoryRoot, "locales", "zh-CN.json"),
		)...,
	)
	if len(violations) == 0 {
		return nil
	}
	sort.Slice(violations, func(left, right int) bool {
		if violations[left].Path != violations[right].Path {
			return violations[left].Path < violations[right].Path
		}
		if violations[left].Line != violations[right].Line {
			return violations[left].Line < violations[right].Line
		}
		return violations[left].Rule < violations[right].Rule
	})
	joined := make([]error, 0, len(violations)+1)
	joined = append(joined, ErrCheckFailed)
	for _, violation := range violations {
		joined = append(joined, errors.New(violation.String()))
	}
	return errors.Join(joined...)
}

// CheckIdentityComposition rejects building one identity by joining another
// with a delimiter. ADR-0015 section 10 requires every identity to be generated
// independently, with association expressed only by typed references, because a
// joined string silently encodes containment that readers and storage then
// depend on. The rule fires only when a delimiter literal joins two or more
// operands that each read as an identity, which is the shape that derives a
// relationship. Composing one value's own parts into a documented format, such
// as a `secret://namespace/id` reference or a composite call key, contributes a
// single identity operand and is therefore unaffected, as is ordinary path and
// URL building.
func CheckIdentityComposition(repositoryRoot string) []Violation {
	const rule = "identity-composition"
	delimiters := map[string]struct{}{
		`"/"`: {}, `":"`: {}, `"|"`: {}, `"#"`: {},
	}
	var violations []Violation
	for _, relativeRoot := range []string{"cmd", "internal"} {
		sourceRoot := filepath.Join(repositoryRoot, relativeRoot)
		if _, err := os.Stat(sourceRoot); errors.Is(err, os.ErrNotExist) {
			continue
		}
		walkErr := filepath.WalkDir(
			sourceRoot,
			func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					if entry.Name() == "testdata" || entry.Name() == "vendor" {
						return filepath.SkipDir
					}
					return nil
				}
				if filepath.Ext(path) != ".go" ||
					strings.HasSuffix(path, "_test.go") {
					return nil
				}
				fileSet := token.NewFileSet()
				parsed, parseErr := parser.ParseFile(fileSet, path, nil, 0)
				if parseErr != nil {
					return parseErr
				}
				ast.Inspect(parsed, func(node ast.Node) bool {
					if call, isCall := node.(*ast.CallExpr); isCall {
						if line, found := identityContainmentProbe(
							fileSet,
							call,
							delimiters,
						); found {
							violations = append(violations, Violation{
								Path: relativeDisplayPath(repositoryRoot, path),
								Line: line,
								Rule: rule,
								Message: "a containment relationship is derived " +
									"by matching one identity against another " +
									"joined with a delimiter; carry the " +
									"relationship as a typed reference instead",
							})
						}
						return true
					}
					binary, ok := node.(*ast.BinaryExpr)
					if !ok || binary.Op != token.ADD {
						return true
					}
					operands := flattenAddition(binary)
					hasDelimiter := false
					identityOperands := 0
					for _, operand := range operands {
						if literal, isLiteral := operand.(*ast.BasicLit); isLiteral {
							if literal.Kind == token.STRING {
								if _, found := delimiters[literal.Value]; found {
									hasDelimiter = true
								}
							}
							continue
						}
						if identityOperandName(operand) {
							identityOperands++
						}
					}
					if !hasDelimiter || identityOperands < 2 {
						return true
					}
					violations = append(violations, Violation{
						Path: relativeDisplayPath(repositoryRoot, path),
						Line: fileSet.Position(binary.Pos()).Line,
						Rule: rule,
						Message: "an identity is composed by joining another " +
							"identity with a delimiter; generate identities " +
							"independently and associate them with typed " +
							"references",
					})
					return true
				})
				return nil
			},
		)
		if walkErr != nil {
			violations = append(violations, Violation{
				Path:    relativeRoot,
				Rule:    rule,
				Message: fmt.Sprintf("identity scan failed: %v", walkErr),
			})
		}
	}
	return violations
}

// identityContainmentProbe reports a string-matching call that reconstructs a
// parent-child relationship from a delimiter-joined identity.
func identityContainmentProbe(
	fileSet *token.FileSet,
	call *ast.CallExpr,
	delimiters map[string]struct{},
) (int, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return 0, false
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok || pkg.Name != "strings" {
		return 0, false
	}
	switch selector.Sel.Name {
	case "HasPrefix",
		"HasSuffix",
		"Contains",
		"TrimPrefix",
		"TrimSuffix",
		"Index",
		"Cut",
		"Split",
		"SplitN":
	default:
		return 0, false
	}
	for _, argument := range call.Args {
		operands := flattenAddition(argument)
		if len(operands) < 2 {
			continue
		}
		hasDelimiter := false
		hasIdentity := false
		for _, operand := range operands {
			if literal, isLiteral := operand.(*ast.BasicLit); isLiteral {
				if literal.Kind == token.STRING {
					if _, found := delimiters[literal.Value]; found {
						hasDelimiter = true
					}
				}
				continue
			}
			if identityOperandName(operand) {
				hasIdentity = true
			}
		}
		if hasDelimiter && hasIdentity {
			return fileSet.Position(call.Pos()).Line, true
		}
	}
	return 0, false
}

func flattenAddition(expression ast.Expr) []ast.Expr {
	binary, ok := expression.(*ast.BinaryExpr)
	if !ok || binary.Op != token.ADD {
		return []ast.Expr{expression}
	}
	return append(
		flattenAddition(binary.X),
		flattenAddition(binary.Y)...,
	)
}

// identityOperandName reports whether an operand reads as an identity value.
func identityOperandName(expression ast.Expr) bool {
	name := ""
	switch typed := expression.(type) {
	case *ast.Ident:
		name = typed.Name
	case *ast.SelectorExpr:
		name = typed.Sel.Name
	case *ast.CallExpr:
		return identityOperandName(typed.Fun)
	default:
		return false
	}
	lowered := strings.ToLower(name)
	return strings.HasSuffix(lowered, "id") ||
		strings.HasSuffix(lowered, "ids") ||
		strings.HasSuffix(lowered, "identity")
}

// CheckPayloadDispatchBoundary keeps the auxiliary and opaque ingress dispatch
// arms separate. Merging them is the exact shape that forwarded a
// payload-bearing auxiliary operation, including the complete prompt and the
// client's own credential, to the inbound origin. Each arm must prove its own
// payload-class admission, so a shared case clause is rejected structurally
// rather than relying on review.
func CheckPayloadDispatchBoundary(repositoryRoot string) []Violation {
	const rule = "payload-dispatch-boundary"
	sourcePath := filepath.Join(
		repositoryRoot,
		"internal",
		"loopbackproxy",
		"handler.go",
	)
	if _, err := os.Stat(sourcePath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, sourcePath, nil, 0)
	if err != nil {
		return []Violation{{
			Path:    relativeDisplayPath(repositoryRoot, sourcePath),
			Rule:    rule,
			Message: fmt.Sprintf("ingress dispatch is unparsable: %v", err),
		}}
	}
	var violations []Violation
	ast.Inspect(parsed, func(node ast.Node) bool {
		clause, ok := node.(*ast.CaseClause)
		if !ok || len(clause.List) < 2 {
			return true
		}
		names := make(map[string]struct{}, len(clause.List))
		for _, expression := range clause.List {
			names[dispatchKindName(expression)] = struct{}{}
		}
		_, auxiliary := names["KindAuxiliary"]
		_, opaque := names["KindOpaque"]
		if !auxiliary || !opaque {
			return true
		}
		violations = append(violations, Violation{
			Path: relativeDisplayPath(repositoryRoot, sourcePath),
			Line: fileSet.Position(clause.Pos()).Line,
			Rule: rule,
			Message: "auxiliary and opaque ingress dispatch share one case " +
				"clause; each arm must prove its own payload-class admission",
		})
		return true
	})
	return violations
}

// dispatchKindName returns the trailing identifier of a case expression so a
// bare KindOpaque and a qualified pathcapability.KindOpaque compare equal.
func dispatchKindName(expression ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.SelectorExpr:
		return typed.Sel.Name
	default:
		return ""
	}
}

// CheckSystemTrustBoundary protects the fixture-only trust-operation shape:
// production composition cannot import it, the package cannot acquire a live
// process runner or concrete command executor, mutation command literals stay
// in the one bounded macOS adapter, and the exact capability namespace cannot
// appear in user-facing production surfaces.
func CheckSystemTrustBoundary(repositoryRoot string) []Violation {
	const (
		systemTrustImport = "github.com/vibe-agi/vibermate/internal/systemtrust"
		systemTrustRoot   = "internal/systemtrust"
		adapterPath       = "internal/systemtrust/macos.go"
		checkerPath       = "internal/repositorycheck/check.go"
	)
	mutationCommands := map[string]struct{}{
		"add-trusted-cert":    {},
		"remove-trusted-cert": {},
		"delete-certificate":  {},
	}
	userSurfaceTokens := []string{
		"systemtrust",
		"system-trust",
		"system_trust",
		"add-trusted-cert",
		"remove-trusted-cert",
		"delete-certificate",
	}
	var violations []Violation
	walkErr := filepath.WalkDir(repositoryRoot, func(
		path string,
		entry fs.DirEntry,
		err error,
	) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "target", "vendor", "testdata":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		relative := filepath.ToSlash(relativeDisplayPath(repositoryRoot, path))
		if filepath.Ext(path) != ".go" {
			if !isSystemTrustUserSurface(relative) ||
				!isSystemTrustSurfaceSource(path) {
				return nil
			}
			file, openErr := os.Open(path)
			if openErr != nil {
				return openErr
			}
			scanner := bufio.NewScanner(file)
			scanner.Buffer(make([]byte, 64*1024), 1024*1024)
			lineNumber := 0
			for scanner.Scan() {
				lineNumber++
				line := strings.ToLower(scanner.Text())
				for _, forbidden := range userSurfaceTokens {
					if !strings.Contains(line, forbidden) {
						continue
					}
					violations = append(violations, Violation{
						Rule:    "system-trust-user-surface",
						Path:    relative,
						Line:    lineNumber,
						Message: "fixture-only system trust capability entered a user-facing production surface",
					})
				}
			}
			scanErr := scanner.Err()
			closeErr := file.Close()
			return errors.Join(scanErr, closeErr)
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fileSet := token.NewFileSet()
		parsed, parseErr := parser.ParseFile(fileSet, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		insideSystemTrust := relative == systemTrustRoot ||
			strings.HasPrefix(relative, systemTrustRoot+"/")
		for _, imported := range parsed.Imports {
			importPath := strings.Trim(imported.Path.Value, `"`)
			position := fileSet.Position(imported.Pos())
			switch {
			case importPath == systemTrustImport && !insideSystemTrust:
				violations = append(violations, Violation{
					Rule:    "system-trust-composition",
					Path:    relative,
					Line:    position.Line,
					Message: "fixture-backed system trust cannot enter production composition",
				})
			case importPath == "os/exec" && insideSystemTrust:
				violations = append(violations, Violation{
					Rule:    "system-trust-live-executor",
					Path:    relative,
					Line:    position.Line,
					Message: "system trust foundation cannot provide a live process executor",
				})
			}
		}
		if insideSystemTrust {
			for _, declaration := range parsed.Decls {
				method, ok := declaration.(*ast.FuncDecl)
				if !ok || method.Recv == nil || method.Name.Name != "Execute" ||
					!functionSignatureNamesType(method.Type, "CommandSpec") {
					continue
				}
				position := fileSet.Position(method.Pos())
				violations = append(violations, Violation{
					Rule:    "system-trust-production-executor",
					Path:    relative,
					Line:    position.Line,
					Message: "concrete command executors cannot exist in production system trust source",
				})
			}
		}
		if relative == adapterPath || relative == checkerPath {
			return nil
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, unquoteErr := strconv.Unquote(literal.Value)
			if unquoteErr != nil {
				return true
			}
			if _, dangerous := mutationCommands[value]; !dangerous {
				return true
			}
			position := fileSet.Position(literal.Pos())
			violations = append(violations, Violation{
				Rule:    "system-trust-command-scope",
				Path:    relative,
				Line:    position.Line,
				Message: "trust mutation command literal is outside the bounded macOS adapter",
			})
			return true
		})
		return nil
	})
	if walkErr != nil {
		violations = append(violations, Violation{
			Rule:    "system-trust-composition",
			Path:    ".",
			Message: walkErr.Error(),
		})
	}
	return violations
}

func functionSignatureNamesType(function *ast.FuncType, name string) bool {
	found := false
	ast.Inspect(function, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if ok && identifier.Name == name {
			found = true
			return false
		}
		return !found
	})
	return found
}

func isSystemTrustUserSurface(relative string) bool {
	for _, prefix := range []string{
		"api/",
		"openapi/",
		"ui/desktop/src/",
		"ui/desktop/src-tauri/src/",
		"ui/desktop/src-tauri/capabilities/",
	} {
		if strings.HasPrefix(relative, prefix) {
			return true
		}
	}
	return strings.Contains(strings.ToLower(relative), "openapi")
}

func isSystemTrustSurfaceSource(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".js", ".json", ".mjs", ".rs", ".ts", ".tsx", ".yaml", ".yml":
		return true
	default:
		return false
	}
}

// CheckDesktopFrontendBoundary protects the production Desktop UI shape that
// now exists: only the native Host adapter may import Tauri, Web Storage cannot
// hold capabilities, and visible TSX copy must come from the locale catalogs.
func CheckDesktopFrontendBoundary(repositoryRoot string) []Violation {
	sourceRoot := filepath.Join(repositoryRoot, "ui", "desktop", "src")
	if _, err := os.Stat(sourceRoot); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	allowedTauriImport := filepath.Join(
		"ui",
		"desktop",
		"src",
		"desktop-host.ts",
	)
	var violations []Violation
	walkErr := filepath.WalkDir(sourceRoot, func(
		path string,
		entry fs.DirEntry,
		err error,
	) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "generated" {
				return filepath.SkipDir
			}
			return nil
		}
		extension := filepath.Ext(path)
		if extension != ".ts" && extension != ".tsx" {
			return nil
		}
		relative := relativeDisplayPath(repositoryRoot, path)
		file, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			line := scanner.Text()
			if strings.Contains(line, "@tauri-apps/") &&
				filepath.Clean(relative) != allowedTauriImport {
				violations = append(violations, Violation{
					Rule:    "desktop-host-boundary",
					Path:    relative,
					Line:    lineNumber,
					Message: "only the Desktop Host adapter may import Tauri",
				})
			}
			if strings.Contains(line, "localStorage") ||
				strings.Contains(line, "sessionStorage") {
				violations = append(violations, Violation{
					Rule:    "desktop-capability-storage",
					Path:    relative,
					Line:    lineNumber,
					Message: "Desktop capabilities cannot use Web Storage",
				})
			}
			if extension == ".tsx" && hasJSXUserCopy(line) {
				violations = append(violations, Violation{
					Rule:    "frontend-i18n",
					Path:    relative,
					Line:    lineNumber,
					Message: "visible TSX copy must use a stable locale key",
				})
			}
		}
		return scanner.Err()
	})
	if walkErr != nil {
		violations = append(violations, Violation{
			Rule:    "desktop-host-boundary",
			Path:    filepath.Join("ui", "desktop", "src"),
			Message: walkErr.Error(),
		})
	}
	return violations
}

func hasJSXUserCopy(line string) bool {
	for _, expression := range []*regexp.Regexp{
		jsxTextRE,
		jsxExpressionLiteralRE,
		jsxStaticUserAttributeRE,
	} {
		for _, match := range expression.FindAllStringSubmatch(line, -1) {
			if len(match) > 1 && containsLetter(match[1]) {
				return true
			}
		}
	}
	return false
}

func containsLetter(value string) bool {
	for _, character := range value {
		if unicode.IsLetter(character) {
			return true
		}
	}
	return false
}

// CheckDataPlaneAccessBoundary keeps the Exchange hot path on the immutable
// SnapshotResolver boundary. It rejects persistence access and Access mutation
// contracts from the production package that now executes provider requests.
func CheckDataPlaneAccessBoundary(repositoryRoot string) []Violation {
	sourceRoot := filepath.Join(repositoryRoot, "internal", "exchange")
	if _, err := os.Stat(sourceRoot); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	const accessImport = "github.com/vibe-agi/vibermate/internal/access"
	const persistenceImport = "github.com/vibe-agi/vibermate/internal/runtimepersistence"
	blockedAccessSymbols := map[string]struct{}{
		"Manager":            {},
		"Repository":         {},
		"SnapshotProjection": {},
		"WriteCommand":       {},
		"WriteResult":        {},
		"Writer":             {},
	}
	var violations []Violation
	walkErr := filepath.WalkDir(sourceRoot, func(
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
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fileSet := token.NewFileSet()
		parsed, parseErr := parser.ParseFile(fileSet, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		relative := relativeDisplayPath(repositoryRoot, path)
		accessNames := make(map[string]struct{})
		for _, imported := range parsed.Imports {
			importPath := strings.Trim(imported.Path.Value, `"`)
			if importPath == persistenceImport {
				position := fileSet.Position(imported.Pos())
				violations = append(violations, Violation{
					Rule:    "data-plane-access-boundary",
					Path:    relative,
					Line:    position.Line,
					Message: "Exchange hot path cannot import runtime persistence",
				})
			}
			if importPath != accessImport {
				continue
			}
			name := "access"
			if imported.Name != nil {
				if imported.Name.Name == "." || imported.Name.Name == "_" {
					position := fileSet.Position(imported.Pos())
					violations = append(violations, Violation{
						Rule:    "data-plane-access-boundary",
						Path:    relative,
						Line:    position.Line,
						Message: "Access contracts must use a named import",
					})
					continue
				}
				name = imported.Name.Name
			}
			accessNames[name] = struct{}{}
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
			if _, imported := accessNames[identifier.Name]; !imported {
				return true
			}
			if _, blocked := blockedAccessSymbols[selector.Sel.Name]; !blocked {
				return true
			}
			position := fileSet.Position(selector.Pos())
			violations = append(violations, Violation{
				Rule: "data-plane-access-boundary",
				Path: relative,
				Line: position.Line,
				Message: fmt.Sprintf(
					"Exchange hot path cannot use Access mutation symbol %s.%s",
					identifier.Name,
					selector.Sel.Name,
				),
			})
			return true
		})
		return nil
	})
	if walkErr != nil {
		violations = append(violations, Violation{
			Rule:    "data-plane-access-boundary",
			Path:    filepath.Join("internal", "exchange"),
			Message: walkErr.Error(),
		})
	}
	return violations
}

// CheckExternalEgressGate rejects new raw outbound-client construction outside
// the gated provider and original-origin transports and their typed probes.
func CheckExternalEgressGate(repositoryRoot string) []Violation {
	allowedFiles := map[string]struct{}{
		"internal/providertransport/transport.go": {},
		"internal/providertransport/loopback.go":  {},
		"internal/providertransport/probe.go":     {},
		"internal/originaltransport/transport.go": {},
		"internal/originaltransport/probe.go":     {},
		"internal/loopbackclient/client.go":       {},
	}
	protectedSymbols := map[string]map[string]struct{}{
		"net": {
			"Dial":        {},
			"DialTimeout": {},
			"Dialer":      {},
			"Resolver":    {},
		},
		"net/http": {
			"Client":           {},
			"Transport":        {},
			"DefaultClient":    {},
			"DefaultTransport": {},
			"Get":              {},
			"Post":             {},
			"PostForm":         {},
			"Head":             {},
		},
		"crypto/tls": {
			"Dial":           {},
			"DialWithDialer": {},
			"Dialer":         {},
		},
	}
	var violations []Violation
	for _, relativeRoot := range []string{"cmd", "internal"} {
		sourceRoot := filepath.Join(repositoryRoot, relativeRoot)
		if _, err := os.Stat(sourceRoot); errors.Is(err, os.ErrNotExist) {
			continue
		}
		walkErr := filepath.WalkDir(
			sourceRoot,
			func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					if entry.Name() == "testdata" || entry.Name() == "vendor" {
						return filepath.SkipDir
					}
					return nil
				}
				if filepath.Ext(path) != ".go" ||
					strings.HasSuffix(path, "_test.go") {
					return nil
				}
				relative := relativeDisplayPath(repositoryRoot, path)
				if _, allowed := allowedFiles[relative]; allowed {
					return nil
				}
				fileSet := token.NewFileSet()
				parsed, parseErr := parser.ParseFile(fileSet, path, nil, 0)
				if parseErr != nil {
					return parseErr
				}
				imports := make(map[string]string)
				for _, imported := range parsed.Imports {
					importPath := strings.Trim(imported.Path.Value, `"`)
					if _, protected := protectedSymbols[importPath]; !protected {
						continue
					}
					name := filepath.Base(importPath)
					if imported.Name != nil {
						if imported.Name.Name == "." ||
							imported.Name.Name == "_" {
							position := fileSet.Position(imported.Pos())
							violations = append(violations, Violation{
								Rule:    "external-egress-gate",
								Path:    relative,
								Line:    position.Line,
								Message: "protected network packages must use a named import",
							})
							continue
						}
						name = imported.Name.Name
					}
					imports[name] = importPath
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
					importPath, protectedImport := imports[identifier.Name]
					if !protectedImport {
						return true
					}
					if _, protected := protectedSymbols[importPath][selector.Sel.Name]; !protected {
						return true
					}
					position := fileSet.Position(selector.Pos())
					violations = append(violations, Violation{
						Rule: "external-egress-gate",
						Path: relative,
						Line: position.Line,
						Message: fmt.Sprintf(
							"raw outbound symbol %s.%s is outside the gated transport",
							identifier.Name,
							selector.Sel.Name,
						),
					})
					return true
				})
				return nil
			},
		)
		if walkErr != nil {
			violations = append(violations, Violation{
				Rule:    "external-egress-gate",
				Path:    relativeRoot,
				Message: walkErr.Error(),
			})
		}
	}
	return violations
}

// CheckProtocolSDKIsolation keeps official SDKs in oracle tests rather than
// allowing them to become the production protocol hot path.
func CheckProtocolSDKIsolation(repositoryRoot string) []Violation {
	protectedRoots := []string{
		filepath.Join("internal", "anthropicchat"),
		filepath.Join("internal", "openairesponses"),
		filepath.Join("internal", "protocolcore"),
		filepath.Join("internal", "responseschat"),
		filepath.Join("internal", "ssewire"),
	}
	blockedImportRoots := []string{
		"github.com/anthropics/anthropic-sdk-go",
		"github.com/openai/openai-go/v3",
	}
	var violations []Violation
	for _, relativeRoot := range protectedRoots {
		sourceRoot := filepath.Join(repositoryRoot, relativeRoot)
		if _, err := os.Stat(sourceRoot); errors.Is(err, os.ErrNotExist) {
			continue
		}
		walkErr := filepath.WalkDir(
			sourceRoot,
			func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					if entry.Name() == "testdata" || entry.Name() == "vendor" {
						return filepath.SkipDir
					}
					return nil
				}
				if filepath.Ext(path) != ".go" ||
					strings.HasSuffix(path, "_test.go") {
					return nil
				}
				fileSet := token.NewFileSet()
				parsed, parseErr := parser.ParseFile(
					fileSet,
					path,
					nil,
					parser.ImportsOnly,
				)
				if parseErr != nil {
					return parseErr
				}
				for _, imported := range parsed.Imports {
					importPath := strings.Trim(imported.Path.Value, `"`)
					if !hasImportRoot(importPath, blockedImportRoots) {
						continue
					}
					position := fileSet.Position(imported.Pos())
					violations = append(violations, Violation{
						Rule: "protocol-sdk-hotpath",
						Path: relativeDisplayPath(repositoryRoot, path),
						Line: position.Line,
						Message: fmt.Sprintf(
							"official SDK import %q is allowed only in oracle tests",
							importPath,
						),
					})
				}
				return nil
			},
		)
		if walkErr != nil {
			violations = append(violations, Violation{
				Rule:    "protocol-sdk-hotpath",
				Path:    relativeRoot,
				Message: walkErr.Error(),
			})
		}
	}
	return violations
}

func hasImportRoot(importPath string, roots []string) bool {
	for _, root := range roots {
		if importPath == root ||
			strings.HasPrefix(importPath, root+"/") {
			return true
		}
	}
	return false
}

// CheckProductionEnglish rejects non-ASCII letters in implementation source,
// developer messages, tests, migrations, and machine schema files.
func CheckProductionEnglish(repositoryRoot string) []Violation {
	var violations []Violation
	for _, relativeRoot := range []string{"cmd", "internal", "api"} {
		sourceRoot := filepath.Join(repositoryRoot, relativeRoot)
		if _, err := os.Stat(sourceRoot); errors.Is(err, os.ErrNotExist) {
			continue
		}
		walkErr := filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "testdata" || entry.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if !isCheckedSource(path) {
				return nil
			}
			violations = append(violations, checkEnglishFile(repositoryRoot, path)...)
			return nil
		})
		if walkErr != nil {
			violations = append(violations, Violation{
				Rule:    "english-source",
				Path:    relativeRoot,
				Message: walkErr.Error(),
			})
		}
	}
	return violations
}

func isCheckedSource(path string) bool {
	switch filepath.Ext(path) {
	case ".go", ".sql", ".json", ".yaml", ".yml":
		return true
	default:
		return false
	}
}

func checkEnglishFile(repositoryRoot, path string) []Violation {
	file, err := os.Open(path)
	if err != nil {
		return []Violation{{
			Rule:    "english-source",
			Path:    relativeDisplayPath(repositoryRoot, path),
			Message: err.Error(),
		}}
	}
	defer func() {
		_ = file.Close()
	}()

	var violations []Violation
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		for _, character := range scanner.Text() {
			if character > unicode.MaxASCII && unicode.IsLetter(character) {
				violations = append(violations, Violation{
					Rule:    "english-source",
					Path:    relativeDisplayPath(repositoryRoot, path),
					Line:    lineNumber,
					Message: "non-ASCII letter in implementation source",
				})
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		violations = append(violations, Violation{
			Rule:    "english-source",
			Path:    relativeDisplayPath(repositoryRoot, path),
			Message: err.Error(),
		})
	}
	return violations
}

// CheckCatalogPair verifies key, parameter, and non-empty-value parity for the
// two canonical locale catalogs.
func CheckCatalogPair(enUSPath, zhCNPath string) []Violation {
	enUS, enViolations := readCatalog(enUSPath)
	zhCN, zhViolations := readCatalog(zhCNPath)
	violations := append(enViolations, zhViolations...)
	if len(enViolations) > 0 || len(zhViolations) > 0 {
		return violations
	}

	keys := make(map[string]struct{}, len(enUS)+len(zhCN))
	for key := range enUS {
		keys[key] = struct{}{}
	}
	for key := range zhCN {
		keys[key] = struct{}{}
	}
	sortedKeys := make([]string, 0, len(keys))
	for key := range keys {
		sortedKeys = append(sortedKeys, key)
	}
	sort.Strings(sortedKeys)

	for _, key := range sortedKeys {
		enValue, enExists := enUS[key]
		zhValue, zhExists := zhCN[key]
		if !enExists {
			violations = append(violations, Violation{
				Rule:    "locale-parity",
				Path:    enUSPath,
				Message: fmt.Sprintf("missing key %q", key),
			})
			continue
		}
		if !zhExists {
			violations = append(violations, Violation{
				Rule:    "locale-parity",
				Path:    zhCNPath,
				Message: fmt.Sprintf("missing key %q", key),
			})
			continue
		}
		if strings.TrimSpace(enValue) == "" {
			violations = append(violations, Violation{
				Rule:    "locale-nonempty",
				Path:    enUSPath,
				Message: fmt.Sprintf("empty value for key %q", key),
			})
		}
		if strings.TrimSpace(zhValue) == "" {
			violations = append(violations, Violation{
				Rule:    "locale-nonempty",
				Path:    zhCNPath,
				Message: fmt.Sprintf("empty value for key %q", key),
			})
		}
		enParameters := catalogParameters(enValue)
		zhParameters := catalogParameters(zhValue)
		if strings.Join(enParameters, ",") != strings.Join(zhParameters, ",") {
			violations = append(violations, Violation{
				Rule:    "locale-parameters",
				Path:    zhCNPath,
				Message: fmt.Sprintf("parameter mismatch for key %q", key),
			})
		}
	}
	return violations
}

func readCatalog(path string) (map[string]string, []Violation) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, []Violation{{
			Rule:    "locale-catalog",
			Path:    path,
			Message: err.Error(),
		}}
	}
	var catalog map[string]string
	if err := json.Unmarshal(contents, &catalog); err != nil {
		return nil, []Violation{{
			Rule:    "locale-catalog",
			Path:    path,
			Message: err.Error(),
		}}
	}
	if catalog == nil {
		return nil, []Violation{{
			Rule:    "locale-catalog",
			Path:    path,
			Message: "catalog must be a JSON object",
		}}
	}
	return catalog, nil
}

func catalogParameters(message string) []string {
	matches := placeholderRE.FindAllStringSubmatch(message, -1)
	parameterSet := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		parameterSet[match[1]] = struct{}{}
	}
	parameters := make([]string, 0, len(parameterSet))
	for parameter := range parameterSet {
		parameters = append(parameters, parameter)
	}
	sort.Strings(parameters)
	return parameters
}

func relativeDisplayPath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(relative)
}
