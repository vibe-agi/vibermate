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
	ErrCheckFailed            = errors.New("repository check failed")
	placeholderRE             = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
	retiredProductAuthorityRE = regexp.MustCompile(
		`\b(?:AccessID|ProfileID|AccessPlan|AccessBinding|EndpointProfile|` +
			`AccessWriter|AccessCatalog|AccessDeletion)\b|` +
			`internal/access(?:/|")|` +
			`\b(?:accessId|profileId|access_id|profile_id|workspace_route)\b|` +
			`/api/v1/accesses\b|workspace-route-bindings\b|\bAI Access\b`,
	)
	// atRestEncryptionRE names only constructs that can mean application-layer
	// field encryption of the local archive. Provider-encrypted reasoning blocks
	// and TLS blind tunnelling legitimately use the word "encrypted", so the word
	// itself is deliberately not matched.
	atRestEncryptionRE = regexp.MustCompile(
		`\b(?:CipherNonce|cipherNonce|cipher_nonce|Ciphertext|ciphertext|` +
			`EncryptionKeyRevision|encryptionKeyRevision|encryption_key_revision)\b|` +
			`\bSQLCipher\b|raw-evidence-key`,
	)
	// Symbols were not the only way the removed encryption survived: comments
	// claiming the archive is encrypted outlived it and read as current
	// documentation. Inside the packages that own stored evidence, the word is
	// allowed only in a sentence that states the absence.
	storageEncryptionProseRE = regexp.MustCompile(`(?i)encrypt`)
	// A user-facing surface may say "encrypted" about a provider's reasoning
	// blocks or a TLS tunnel. What it may never say is that the archive itself is
	// encrypted, or that a data key can be rotated - the two claims
	// INV-STORE-DISCLOSED names outright.
	// The Simplified Chinese alternatives are escaped for the same ASCII-only
	// reason; they read "content", "storage", "database", "archive", "rotate"
	// and "data key".
	archiveEncryptionClaimRE = regexp.MustCompile(
		`(?i)(?:encrypt\w*)[^\n]{0,40}` +
			`(?:at rest|stored|storage|database|archive|on disk|\x{6b63}\x{6587}|\x{5b58}\x{50a8}|\x{6570}\x{636e}\x{5e93}|\x{5b58}\x{6863})|` +
			`(?:at rest|stored|storage|database|archive|on disk|\x{6b63}\x{6587}|\x{5b58}\x{50a8}|\x{6570}\x{636e}\x{5e93}|\x{5b58}\x{6863})` +
			`[^\n]{0,40}(?:encrypt\w*)|` +
			`(?i)(?:rotate|\x{8f6e}\x{6362})[^\n]{0,24}(?:data key|\x{6570}\x{636e}\x{5bc6}\x{94a5})`,
	)
	flutterCopyKeyRE = regexp.MustCompile(`^\s+'([^']+)':`)
	// A sentence, key or identifier that states the absence is exactly what the
	// product must be able to say, on every surface. The Simplified Chinese
	// alternatives are escaped because implementation source is ASCII-only; they
	// read "not encrypted" and "does not encrypt".
	storageEncryptionAbsenceRE = regexp.MustCompile(
		`(?i)forbids|not[_ ]encrypted|never encrypt|no application-layer|` +
			`without.{0,24}encrypt|rules out|disclose an absence|` +
			`\x{672a}\x{52a0}\x{5bc6}|\x{4e0d}\x{52a0}\x{5bc6}`,
	)
	// A credential-absence claim is recognized by three independent signals
	// rather than one combined pattern. Alternating unbounded runs across a
	// sentence backtracks badly on real prose, and three linear scans are both
	// faster and easier to reason about.
	//
	// The Simplified Chinese alternatives are escaped because implementation
	// source is ASCII-only; they read "credential", "secret", "never", "does
	// not", "none of them", "removed", "enter the database", "written to disk"
	// and "storage".
	credentialNounRE = regexp.MustCompile(
		`(?i)credential|api[ -]?key|authorization|cookie|` +
			`\x{51ed}\x{8bc1}|\x{5bc6}\x{94a5}`,
	)
	credentialAbsenceRE = regexp.MustCompile(
		`(?i)\bnever\b|\bno\b|\bnone\b|\bnot\b|\bremoved\b|\bstrips?\b|` +
			`\x{6c38}\x{4e0d}|\x{4e0d}\x{4f1a}|\x{90fd}\x{4e0d}|\x{79fb}\x{9664}`,
	)
	credentialStorageRE = regexp.MustCompile(
		`(?i)stor\w*|reach\w*|writ\w*|column|database|archive|payload|` +
			`\x{5165}\x{5e93}|\x{843d}\x{76d8}|\x{5b58}\x{50a8}`,
	)
	// What makes the claim honest is naming the domain it covers. Recognition is
	// by header field name, so any of these turns an absolute statement into a
	// bounded one. The Chinese alternatives read "credential header" and
	// "field name".
	credentialScopeRE = regexp.MustCompile(
		`(?i)header|field name|by name|predicate|recognized|recognised|` +
			`identified|known|NameIsCredential|` +
			`\x{51ed}\x{8bc1}\x{5934}|\x{5b57}\x{6bb5}\x{540d}`,
	)
	// A sentence about the SecretStore boundary is a different claim, and it is
	// absolute because it is true: ProviderAccount and proxy credentials belong
	// to the host-selected SecretStore and genuinely never enter SQLite. Only
	// the evidence archive keeps wire bytes it cannot vouch for. The Chinese
	// alternative reads "secret store".
	credentialControlPlaneRE = regexp.MustCompile(
		`(?i)SecretStore|SecretRef|secret_reference|ProviderAccount|` +
			`AuxiliaryLLM|CredentialBinding|` +
			`\x{5bc6}\x{94a5}\x{5b58}\x{50a8}`,
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
	violations = append(violations, CheckDataPlaneEnvironmentBoundary(repositoryRoot)...)
	violations = append(violations, CheckRetiredProductAuthority(repositoryRoot)...)
	violations = append(violations, CheckDesktopFrontendBoundary(repositoryRoot)...)
	violations = append(violations, CheckSystemTrustBoundary(repositoryRoot)...)
	violations = append(violations, CheckSignerIdentityBoundary(repositoryRoot)...)
	violations = append(violations, CheckProductionCompositionBoundary(repositoryRoot)...)
	violations = append(violations, CheckPayloadDispatchBoundary(repositoryRoot)...)
	violations = append(violations, CheckIdentityComposition(repositoryRoot)...)
	violations = append(violations, CheckAtRestEncryptionAbsence(repositoryRoot)...)
	violations = append(violations, CheckCredentialClaimScope(repositoryRoot)...)
	violations = append(violations, CheckFlutterCopyPair(repositoryRoot)...)
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

// CheckRetiredProductAuthority prevents the deleted Access/Profile product
// model from re-entering current production source, wire contracts, UI, or
// authority documentation. Historical evidence and tests may still name the
// retired model when explaining or attacking a migration; they are
// intentionally outside this source-shape gate.
func CheckRetiredProductAuthority(repositoryRoot string) []Violation {
	const rule = "retired-product-authority"
	currentFiles := map[string]struct{}{
		"README.md":                {},
		"README.zh-CN.md":          {},
		"PLAN.md":                  {},
		"docs/m0-acceptance.md":    {},
		"docs/module-map.md":       {},
		"api/control.openapi.yaml": {},
		"locales/en-US.json":       {},
		"locales/zh-CN.json":       {},
	}
	productionRoots := []string{"cmd", "internal", "ui/flutter_app/lib"}
	var violations []Violation
	scan := func(path, relative string) error {
		if relative == "internal/repositorycheck/check.go" ||
			strings.HasSuffix(relative, "_test.go") {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			match := retiredProductAuthorityRE.FindString(scanner.Text())
			if match == "" {
				continue
			}
			violations = append(violations, Violation{
				Rule:    rule,
				Path:    filepath.FromSlash(relative),
				Line:    lineNumber,
				Message: fmt.Sprintf("retired product authority %q entered a current surface", match),
			})
		}
		return scanner.Err()
	}
	for relative := range currentFiles {
		path := filepath.Join(repositoryRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := scan(path, relative); err != nil {
			violations = append(violations, Violation{
				Rule: rule, Path: filepath.FromSlash(relative), Message: err.Error(),
			})
		}
	}
	for _, relativeRoot := range productionRoots {
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
				switch entry.Name() {
				case "generated", "node_modules", "target", "testdata", "vendor":
					return filepath.SkipDir
				default:
					return nil
				}
			}
			extension := filepath.Ext(path)
			if extension != ".dart" && extension != ".go" &&
				extension != ".json" && extension != ".sql" {
				return nil
			}
			relative, relativeErr := filepath.Rel(repositoryRoot, path)
			if relativeErr != nil {
				return relativeErr
			}
			return scan(path, filepath.ToSlash(relative))
		})
		if walkErr != nil {
			violations = append(violations, Violation{
				Rule: rule, Path: filepath.FromSlash(relativeRoot), Message: walkErr.Error(),
			})
		}
	}
	return violations
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

// CheckSystemTrustBoundary protects the system-trust operation shape:
// platform-neutral planning stays free of process execution, while the one
// explicitly named Desktop adapter may own the live runner. Mutation command
// literals remain in the bounded macOS adapter, and the exact capability
// namespace cannot appear in user-facing production surfaces.
func CheckSystemTrustBoundary(repositoryRoot string) []Violation {
	const (
		systemTrustImport     = "github.com/vibe-agi/vibermate/internal/systemtrust"
		systemTrustRoot       = "internal/systemtrust"
		productionAdapterRoot = "internal/desktoptrust"
		adapterPath           = "internal/systemtrust/macos.go"
		checkerPath           = "internal/repositorycheck/check.go"
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
		insideProductionAdapter := relative == productionAdapterRoot ||
			strings.HasPrefix(relative, productionAdapterRoot+"/")
		for _, imported := range parsed.Imports {
			importPath := strings.Trim(imported.Path.Value, `"`)
			position := fileSet.Position(imported.Pos())
			switch {
			case importPath == systemTrustImport &&
				!insideSystemTrust && !insideProductionAdapter:
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
		"ui/flutter_app/lib/",
	} {
		if strings.HasPrefix(relative, prefix) {
			return true
		}
	}
	return strings.Contains(strings.ToLower(relative), "openapi")
}

func isSystemTrustSurfaceSource(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".dart", ".js", ".json", ".mjs", ".rs", ".ts", ".tsx", ".yaml", ".yml":
		return true
	default:
		return false
	}
}

// CheckDesktopFrontendBoundary protects the production Flutter Desktop shape.
// Direct IO, control-network, and process access stay in the small bootstrap
// and API adapters; the UI cannot introduce FFI or authority-bearing local
// stores; and the non-secret preference adapter cannot gain capabilities.
func CheckDesktopFrontendBoundary(repositoryRoot string) []Violation {
	sourceRoot := filepath.Join(repositoryRoot, "ui", "flutter_app", "lib")
	if _, err := os.Stat(sourceRoot); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	allowedIO := map[string]struct{}{
		"ui/flutter_app/lib/main.dart":                               {},
		"ui/flutter_app/lib/core/api/control_api.dart":               {},
		"ui/flutter_app/lib/core/bootstrap/desktop_runtime.dart":     {},
		"ui/flutter_app/lib/core/bootstrap/platform_runtime_io.dart": {},
		"ui/flutter_app/lib/core/bootstrap/terminal_command.dart":    {},
		"ui/flutter_app/lib/core/bootstrap/terminal_command_io.dart": {},
	}
	allowedNetwork := map[string]struct{}{
		"ui/flutter_app/lib/core/api/control_api.dart":           {},
		"ui/flutter_app/lib/core/bootstrap/desktop_runtime.dart": {},
	}
	allowedProcess := map[string]struct{}{
		"ui/flutter_app/lib/core/bootstrap/desktop_runtime.dart":     {},
		"ui/flutter_app/lib/core/bootstrap/terminal_command.dart":    {},
		"ui/flutter_app/lib/core/bootstrap/terminal_command_io.dart": {},
	}
	authorityStores := []string{
		"package:flutter_secure_storage/",
		"package:hive/",
		"package:isar/",
		"package:shared_preferences/",
		"package:sqflite/",
	}
	networkCalls := []string{
		"HttpClient(",
		"RawSocket",
		"SecureSocket",
		"Socket.connect",
	}
	processCalls := []string{
		"Process.run(",
		"Process.runSync(",
		"Process.start(",
	}
	capabilityNames := []string{
		"bootstrapNonce",
		"proxyPassword",
		"readToken",
		"stateTag",
		"writeToken",
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
			return nil
		}
		if filepath.Ext(path) != ".dart" {
			return nil
		}
		relative := filepath.ToSlash(relativeDisplayPath(repositoryRoot, path))
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
			if strings.Contains(line, "import 'dart:io'") {
				if _, allowed := allowedIO[relative]; !allowed {
					violations = append(violations, Violation{
						Rule:    "desktop-io-boundary",
						Path:    relative,
						Line:    lineNumber,
						Message: "direct dart:io access must stay in a bounded Desktop adapter",
					})
				}
			}
			if strings.Contains(line, "import 'dart:ffi'") {
				violations = append(violations, Violation{
					Rule:    "desktop-ffi-boundary",
					Path:    relative,
					Line:    lineNumber,
					Message: "Flutter production UI cannot acquire direct FFI authority",
				})
			}
			for _, packagePrefix := range authorityStores {
				if !strings.Contains(line, packagePrefix) {
					continue
				}
				violations = append(violations, Violation{
					Rule:    "desktop-authority-storage",
					Path:    relative,
					Line:    lineNumber,
					Message: "Flutter UI cannot introduce an authority-bearing local store",
				})
			}
			if _, allowed := allowedNetwork[relative]; !allowed {
				for _, call := range networkCalls {
					if !strings.Contains(line, call) {
						continue
					}
					violations = append(violations, Violation{
						Rule:    "desktop-control-network-boundary",
						Path:    relative,
						Line:    lineNumber,
						Message: "direct network access must stay in the control or bootstrap adapter",
					})
					break
				}
			}
			if _, allowed := allowedProcess[relative]; !allowed {
				for _, call := range processCalls {
					if !strings.Contains(line, call) {
						continue
					}
					violations = append(violations, Violation{
						Rule:    "desktop-process-boundary",
						Path:    relative,
						Line:    lineNumber,
						Message: "process execution must stay in a bounded Desktop adapter",
					})
					break
				}
			}
			if strings.HasPrefix(relative, "ui/flutter_app/lib/core/preferences/") {
				for _, capabilityName := range capabilityNames {
					if !strings.Contains(line, capabilityName) {
						continue
					}
					violations = append(violations, Violation{
						Rule:    "desktop-capability-storage",
						Path:    relative,
						Line:    lineNumber,
						Message: "Desktop preferences must remain non-secret and non-authoritative",
					})
					break
				}
			}
		}
		return scanner.Err()
	})
	if walkErr != nil {
		violations = append(violations, Violation{
			Rule:    "desktop-io-boundary",
			Path:    filepath.Join("ui", "flutter_app", "lib"),
			Message: walkErr.Error(),
		})
	}
	return violations
}

// CheckDataPlaneEnvironmentBoundary keeps the Exchange hot path on immutable
// request-plan contracts. Configuration mutation, projection ownership, and
// persistence remain outside the package that executes provider requests.
func CheckDataPlaneEnvironmentBoundary(repositoryRoot string) []Violation {
	sourceRoot := filepath.Join(repositoryRoot, "internal", "exchange")
	if _, err := os.Stat(sourceRoot); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	const environmentImport = "github.com/vibe-agi/vibermate/internal/environment"
	const persistenceImport = "github.com/vibe-agi/vibermate/internal/runtimepersistence"
	allowedEnvironmentSymbols := map[string]struct{}{
		"CandidateDigest":              {},
		"ClientEndpointID":             {},
		"ClientProtocolPlanID":         {},
		"CompiledAccountPolicy":        {},
		"CompiledAccountReference":     {},
		"ContentRecordingFull":         {},
		"ContentRecordingMetadataOnly": {},
		"ContentRecordingOff":          {},
		"ContentRecordingPolicy":       {},
		"EnvironmentID":                {},
		"AccountSelectionFixed":        {},
		"AccountSelectionJavaScript":   {},
		"MaxRevision":                  {},
		"NewClientEndpointID":          {},
		"NewClientProtocolPlanID":      {},
		"NewEnvironmentID":             {},
		"NewUpstreamRouteID":           {},
		"ParseCandidateDigest":         {},
		"PolicySet":                    {},
		"RequestPlan":                  {},
		"Revision":                     {},
		"UpstreamRouteID":              {},
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
		environmentNames := make(map[string]struct{})
		for _, imported := range parsed.Imports {
			importPath := strings.Trim(imported.Path.Value, `"`)
			if importPath == persistenceImport {
				position := fileSet.Position(imported.Pos())
				violations = append(violations, Violation{
					Rule:    "data-plane-environment-boundary",
					Path:    relative,
					Line:    position.Line,
					Message: "Exchange hot path cannot import runtime persistence",
				})
			}
			if importPath != environmentImport {
				continue
			}
			name := "environment"
			if imported.Name != nil {
				if imported.Name.Name == "." || imported.Name.Name == "_" {
					position := fileSet.Position(imported.Pos())
					violations = append(violations, Violation{
						Rule:    "data-plane-environment-boundary",
						Path:    relative,
						Line:    position.Line,
						Message: "Environment contracts must use a named import",
					})
					continue
				}
				name = imported.Name.Name
			}
			environmentNames[name] = struct{}{}
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
			if _, imported := environmentNames[identifier.Name]; !imported {
				return true
			}
			if _, allowed := allowedEnvironmentSymbols[selector.Sel.Name]; allowed {
				return true
			}
			position := fileSet.Position(selector.Pos())
			violations = append(violations, Violation{
				Rule: "data-plane-environment-boundary",
				Path: relative,
				Line: position.Line,
				Message: fmt.Sprintf(
					"Exchange hot path cannot use Environment authority symbol %s.%s",
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
			Rule:    "data-plane-environment-boundary",
			Path:    filepath.Join("internal", "exchange"),
			Message: walkErr.Error(),
		})
	}
	return violations
}

// CheckExternalEgressGate rejects new raw outbound-client construction outside
// the small set of transport modules that own an external-network boundary.
func CheckExternalEgressGate(repositoryRoot string) []Violation {
	allowedFiles := map[string]struct{}{
		"internal/providertransport/transport.go": {},
		"internal/providertransport/loopback.go":  {},
		"internal/providertransport/probe.go":     {},
		"internal/originaltransport/transport.go": {},
		"internal/originaltransport/probe.go":     {},
		"internal/loopbackclient/client.go":       {},
		"internal/blindtunnel/dialer.go":          {},
		// egressnetwork is the single typed compiler for a frozen per-flow
		// direct/SOCKS/DoH policy. Only these implementation files may construct
		// its base dialer and the private HTTP client used for DNS-over-HTTPS;
		// callers receive ContextDialer and Resolver interfaces instead.
		"internal/egressnetwork/dialer.go": {},
		"internal/egressnetwork/doh.go":    {},
		// Runtime Server clients have one typed transport adapter. Callers may
		// issue HTTP requests and open the proxy stream through its interface,
		// but cannot construct another outbound client or dialer themselves.
		"internal/servertransport/transport.go": {},
		// The third egress kind's typed probe, alongside the two above it.
		//
		// A probe cannot reach its target through the gated dialer that sits
		// beside it: that dialer acquires an egress lease first, and the probe
		// is what decides whether the gate may release leases at all. It would
		// be asking permission from the gate it is trying to open.
		//
		// What keeps this narrow is the same thing that keeps the other two
		// narrow: the file connects and immediately closes. It writes nothing
		// and reads nothing, because a tunnel forwards bytes this product
		// never interprets.
		"internal/blindtunnel/probe.go": {},
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

// CheckSignerIdentityBoundary keeps a code-signing requirement something this
// product generates rather than something a catalog writes.
//
// The requirement used to be a string a signer entry carried, checked with
// `strings.Contains`. A single literal could satisfy every check and mean
// something else — `identifier "anchor apple subject.OU"` contains all three
// words — so the guarantee was a comment rather than a property. It is now
// generated from two validated fields, and this stops the string form growing
// back: nothing outside the one generator may write requirement syntax, and
// nothing may reach `codesign` except through `codesignature`.
func CheckSignerIdentityBoundary(repositoryRoot string) []Violation {
	const (
		generatorPath   = "internal/codesignature/identity.go"
		signaturePath   = "internal/codesignature/signature.go"
		checkerPath     = "internal/repositorycheck/check.go"
		codesignProgram = "/usr/bin/codesign"
	)
	// Fragments of the requirement language. Anywhere but the generator, one
	// of these in a literal is a requirement being written by hand.
	requirementSyntax := []string{
		"anchor apple",
		"subject.OU",
		"certificate leaf[",
		"1.2.840.113635.100.6",
	}

	var violations []Violation
	fileSet := token.NewFileSet()
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
			if !strings.HasSuffix(entry.Name(), ".go") {
				return nil
			}
			relative, err := filepath.Rel(repositoryRoot, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if relative == generatorPath || relative == checkerPath ||
				strings.HasSuffix(relative, "_test.go") {
				return nil
			}
			parsed, err := parser.ParseFile(fileSet, path, nil, 0)
			if err != nil {
				return nil
			}
			ast.Inspect(parsed, func(node ast.Node) bool {
				literal, ok := node.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				value := literal.Value
				position := fileSet.Position(literal.Pos())
				for _, fragment := range requirementSyntax {
					if !strings.Contains(value, fragment) {
						continue
					}
					violations = append(violations, Violation{
						Rule:    "signer-requirement-literal",
						Path:    relative,
						Line:    position.Line,
						Message: "a code-signing requirement is generated from validated fields, not written as a literal",
					})
					break
				}
				if strings.Contains(value, codesignProgram) &&
					relative != signaturePath {
					violations = append(violations, Violation{
						Rule:    "signer-verification-boundary",
						Path:    relative,
						Line:    position.Line,
						Message: "signature verification goes through internal/codesignature",
					})
				}
				return true
			})
			return nil
		},
	)
	if err != nil {
		violations = append(violations, Violation{
			Rule:    "signer-identity-boundary",
			Path:    ".",
			Message: "signer identity boundary could not be inspected",
		})
	}
	return violations
}

// CheckAtRestEncryptionAbsence keeps application-layer field encryption out of
// the local archive and out of every surface that describes it.
//
// `INV-STORE-DISCLOSED` is a release gate in design 06 §8.1: the product must
// disclose that the runtime database is not encrypted at rest, and must not
// display "content is encrypted" or "rotate the data key". An implementation
// that seals a payload with a key stored beside it buys nothing against the
// threat model it appears to address, and it did real harm here — it made
// retaining a live `Authorization` header feel acceptable, and it made the
// stored bytes incompressible and undedupable. This rule is what stops that
// construct from returning.
func CheckAtRestEncryptionAbsence(repositoryRoot string) []Violation {
	const rule = "at-rest-encryption"
	surfaceFiles := map[string]struct{}{
		"README.md":          {},
		"README.zh-CN.md":    {},
		"PLAN.md":            {},
		"locales/en-US.json": {},
		"locales/zh-CN.json": {},
	}
	productionRoots := []string{"cmd", "internal", "ui/flutter_app/lib", "api"}
	// Packages that own stored evidence. Elsewhere "encrypted" legitimately
	// describes provider-encrypted reasoning blocks and TLS tunnelling.
	storageRoots := []string{
		"internal/rawevidence/", "internal/runtimepersistence/",
	}
	// Every other surface is checked for the narrower claim about the archive.
	claimSuffixes := []string{".dart", ".json", ".md", ".yaml", ".yml"}
	var violations []Violation
	scan := func(path, relative string) error {
		if relative == "internal/repositorycheck/check.go" ||
			strings.HasSuffix(relative, "_test.go") {
			return nil
		}
		storage := false
		for _, root := range storageRoots {
			if strings.HasPrefix(relative, root) {
				storage = true
			}
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
		lineNumber := 0
		sentence := ""
		sentenceLine := 0
		for scanner.Scan() {
			lineNumber++
			line := scanner.Text()
			if match := atRestEncryptionRE.FindString(line); match != "" {
				violations = append(violations, Violation{
					Rule: rule,
					Path: filepath.FromSlash(relative),
					Line: lineNumber,
					Message: fmt.Sprintf(
						"at-rest archive encryption construct %q entered a current surface",
						match,
					),
				})
				continue
			}
			if !storage {
				checked := false
				for _, suffix := range claimSuffixes {
					if strings.HasSuffix(relative, suffix) {
						checked = true
					}
				}
				if checked && archiveEncryptionClaimRE.MatchString(line) &&
					!storageEncryptionAbsenceRE.MatchString(line) {
					violations = append(violations, Violation{
						Rule: rule,
						Path: filepath.FromSlash(relative),
						Line: lineNumber,
						Message: "this surface tells the user the archive is " +
							"encrypted or that a data key rotates; neither is true",
					})
				}
				continue
			}
			// Prose wraps, so the claim is judged one sentence at a time: a line
			// containing "encryption" is often the continuation of a sentence
			// whose subject is the absence of it.
			if sentence == "" {
				sentenceLine = lineNumber
			}
			sentence += " " + line
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasSuffix(trimmed, ".") {
				continue
			}
			if storageEncryptionProseRE.MatchString(sentence) &&
				!storageEncryptionAbsenceRE.MatchString(sentence) {
				violations = append(violations, Violation{
					Rule: rule,
					Path: filepath.FromSlash(relative),
					Line: sentenceLine,
					Message: "stored evidence is not encrypted; " +
						"this sentence still says it is",
				})
			}
			sentence = ""
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		// A file whose last claim never reached a full stop must still be judged.
		if storage && sentence != "" &&
			storageEncryptionProseRE.MatchString(sentence) &&
			!storageEncryptionAbsenceRE.MatchString(sentence) {
			violations = append(violations, Violation{
				Rule: rule,
				Path: filepath.FromSlash(relative),
				Line: sentenceLine,
				Message: "stored evidence is not encrypted; " +
					"this sentence still says it is",
			})
		}
		return nil
	}
	for relative := range surfaceFiles {
		path := filepath.Join(repositoryRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := scan(path, relative); err != nil {
			violations = append(violations, Violation{
				Rule: rule, Path: filepath.FromSlash(relative), Message: err.Error(),
			})
		}
	}
	for _, relativeRoot := range productionRoots {
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
				switch entry.Name() {
				case "generated", "node_modules", "target", "testdata", "vendor":
					return filepath.SkipDir
				default:
					return nil
				}
			}
			switch filepath.Ext(path) {
			case ".dart", ".go", ".json", ".sql", ".yaml", ".yml":
			default:
				return nil
			}
			relative, relativeErr := filepath.Rel(repositoryRoot, path)
			if relativeErr != nil {
				return relativeErr
			}
			return scan(path, filepath.ToSlash(relative))
		})
		if walkErr != nil {
			violations = append(violations, Violation{
				Rule: rule, Path: filepath.FromSlash(relativeRoot), Message: walkErr.Error(),
			})
		}
	}
	return violations
}

// prose reports whether a trimmed source line carries human-readable text: a
// comment in any of the languages this repository uses, or a single-quoted
// string, which is the shape a Dart copy entry takes. Double quotes are left
// out on purpose: in Go they open a map-literal key, not a sentence.
func prose(trimmed string) bool {
	switch {
	case strings.HasPrefix(trimmed, "//"),
		strings.HasPrefix(trimmed, "--"),
		strings.HasPrefix(trimmed, "*"),
		strings.HasPrefix(trimmed, "'"):
		return true
	default:
		return false
	}
}

// CheckCredentialClaimScope keeps every statement about credential absence
// bounded to what the product actually does.
//
// The archive removes the values of headers whose names match the credential
// predicate. It does not scan bodies, tool arguments or query strings, and it
// must not, because rewriting observed bytes is how a forensic archive stops
// being evidence. An unbounded sentence — "no credential value is stored" —
// therefore tells a user something false in the one direction that costs them
// something: it reads as permission to paste a key into a prompt.
//
// The scope was corrected by hand once, across four surfaces, and the sweep
// missed several more. This rule is what makes the claim checkable instead of
// remembered.
func CheckCredentialClaimScope(repositoryRoot string) []Violation {
	const rule = "credential-claim-scope"
	// Packages that own stored evidence. Elsewhere a credential absence claim is
	// about the control plane or a DTO, where it is both true and unrelated.
	storageRoots := []string{
		"internal/rawevidence/", "internal/runtimepersistence/",
	}
	// The shipping surfaces that describe the archive to a user.
	surfaceFiles := []string{
		"ui/flutter_app/lib/core/i18n/app_copy.dart",
		"ui/flutter_app/lib/features/workbench/settings_view.dart",
		"ui/flutter_app/lib/features/workbench/conversation_timeline.dart",
		"ui/flutter_app/lib/preview/preview_control_api.dart",
	}
	var violations []Violation
	judgeSentence := func(relative, sentence string, line int) {
		if !credentialStorageRE.MatchString(sentence) ||
			credentialScopeRE.MatchString(sentence) ||
			credentialControlPlaneRE.MatchString(sentence) {
			return
		}
		// The negation has to be about the credential, not merely nearby. In
		// "a query-string API key lands in a plaintext column, and no dialect
		// this product supports uses it", every signal is present and no claim
		// about credential absence is made. Requiring the noun and the negation
		// to share a clause is what tells the two apart.
		claimed := false
		for _, clause := range strings.FieldsFunc(sentence, func(r rune) bool {
			return r == ',' || r == ';' || r == ':'
		}) {
			if credentialNounRE.MatchString(clause) &&
				credentialAbsenceRE.MatchString(clause) {
				claimed = true
				break
			}
		}
		if !claimed {
			return
		}
		violations = append(violations, Violation{
			Rule: rule,
			Path: filepath.FromSlash(relative),
			Line: line,
			Message: "this sentence claims credential absence without naming " +
				"its scope; only recognized credential header values are " +
				"removed, and bodies, tool arguments and query strings are not",
		})
	}
	// A flushed block is split on its own full stops before judging. A line
	// often ends one sentence and starts another, and a later sentence that
	// names the scope must not launder an earlier one that made the claim
	// without it.
	judge := func(relative, block string, line int) {
		for _, sentence := range strings.SplitAfter(block, ". ") {
			judgeSentence(relative, sentence, line)
		}
	}
	scan := func(path, relative string) error {
		if relative == "internal/repositorycheck/check.go" {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
		lineNumber := 0
		block := ""
		blockLine := 0
		for scanner.Scan() {
			lineNumber++
			trimmed := strings.TrimSpace(scanner.Text())
			// The rule judges prose, and code is not prose. Accumulating code
			// lines lets a parameter named `credential`, an unrelated `never`
			// and the word `database` combine across a whole function into a
			// claim that no line actually makes.
			if !prose(trimmed) {
				if block != "" {
					judge(relative, block, blockLine)
					block = ""
				}
				continue
			}
			if block == "" {
				blockLine = lineNumber
			}
			block += " " + trimmed
			// Prose wraps, so a claim is judged one sentence at a time. A Dart
			// copy entry ends on a comma rather than a full stop, so both close
			// a block here.
			if !strings.HasSuffix(trimmed, ".") &&
				!strings.HasSuffix(trimmed, "',") {
				continue
			}
			judge(relative, block, blockLine)
			block = ""
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		// A file whose last claim never reached a full stop must still be judged.
		if block != "" {
			judge(relative, block, blockLine)
		}
		return nil
	}
	paths := make(map[string]struct{}, len(surfaceFiles))
	for _, relative := range surfaceFiles {
		paths[relative] = struct{}{}
	}
	for _, relativeRoot := range storageRoots {
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
				switch entry.Name() {
				case "generated", "node_modules", "target", "testdata", "vendor":
					return filepath.SkipDir
				default:
					return nil
				}
			}
			switch filepath.Ext(path) {
			case ".go", ".sql":
			default:
				return nil
			}
			relative, relativeErr := filepath.Rel(repositoryRoot, path)
			if relativeErr != nil {
				return relativeErr
			}
			paths[filepath.ToSlash(relative)] = struct{}{}
			return nil
		})
		if walkErr != nil {
			violations = append(violations, Violation{
				Rule: rule, Path: filepath.FromSlash(relativeRoot),
				Message: walkErr.Error(),
			})
		}
	}
	ordered := make([]string, 0, len(paths))
	for relative := range paths {
		ordered = append(ordered, relative)
	}
	sort.Strings(ordered)
	for _, relative := range ordered {
		path := filepath.Join(repositoryRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := scan(path, relative); err != nil {
			violations = append(violations, Violation{
				Rule: rule, Path: filepath.FromSlash(relative), Message: err.Error(),
			})
		}
	}
	return violations
}

// CheckFlutterCopyPair keeps the Dart copy table's two language maps in step.
//
// AppCopy resolves a missing key to the key itself, so an untranslated string
// reaches the user as `exchange.system_parameter` and no widget test notices. The
// JSON locale catalogs already have a parity gate; this is the same gate for the
// table the Flutter workbench actually reads.
func CheckFlutterCopyPair(repositoryRoot string) []Violation {
	const rule = "flutter-copy-parity"
	relative := "ui/flutter_app/lib/core/i18n/app_copy.dart"
	path := filepath.Join(repositoryRoot, filepath.FromSlash(relative))
	contents, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return []Violation{{
			Rule: rule, Path: filepath.FromSlash(relative), Message: err.Error(),
		}}
	}
	english, englishErr := flutterCopyKeys(string(contents), "_en")
	chinese, chineseErr := flutterCopyKeys(string(contents), "_zh")
	for _, mapErr := range []error{englishErr, chineseErr} {
		if mapErr != nil {
			return []Violation{{
				Rule: rule, Path: filepath.FromSlash(relative),
				Message: mapErr.Error(),
			}}
		}
	}
	var violations []Violation
	report := func(missingFrom string, keys map[string]int, other map[string]int) {
		names := make([]string, 0)
		for key := range keys {
			if _, ok := other[key]; !ok {
				names = append(names, key)
			}
		}
		sort.Strings(names)
		for _, key := range names {
			violations = append(violations, Violation{
				Rule: rule,
				Path: filepath.FromSlash(relative),
				Line: keys[key],
				Message: fmt.Sprintf(
					"copy key %q is missing from %s, so it would render as its own key",
					key, missingFrom,
				),
			})
		}
	}
	report("_zh", english, chinese)
	report("_en", chinese, english)
	return violations
}

// flutterCopyKeys returns each key in one `static const <name> = <String,
// String>{ ... }` literal, mapped to the line it is declared on. A key is a
// quoted string at the start of a line followed by a colon, which excludes the
// wrapped continuation lines of a long value.
func flutterCopyKeys(contents, name string) (map[string]int, error) {
	opening := "static const " + name + " = <String, String>{"
	start := strings.Index(contents, opening)
	if start < 0 {
		return nil, fmt.Errorf("copy table %s was not found", name)
	}
	line := 1 + strings.Count(contents[:start], "\n")
	keys := make(map[string]int)
	for _, text := range strings.Split(contents[start:], "\n")[1:] {
		line++
		if strings.TrimSpace(text) == "};" {
			return keys, nil
		}
		match := flutterCopyKeyRE.FindStringSubmatch(text)
		if match == nil {
			continue
		}
		if _, duplicate := keys[match[1]]; duplicate {
			return nil, fmt.Errorf("copy key %q is declared twice in %s", match[1], name)
		}
		keys[match[1]] = line
	}
	return nil, fmt.Errorf("copy table %s was not terminated", name)
}
