// Package repositorycheck implements deterministic source and locale checks.
package repositorycheck

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var (
	ErrCheckFailed = errors.New("repository check failed")
	placeholderRE  = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
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

// CheckProtocolSDKIsolation keeps official SDKs in oracle tests rather than
// allowing them to become the production protocol hot path.
func CheckProtocolSDKIsolation(repositoryRoot string) []Violation {
	protectedRoots := []string{
		filepath.Join("internal", "anthropicchat"),
		filepath.Join("internal", "protocolcore"),
		filepath.Join("internal", "ssewire"),
	}
	blockedImports := map[string]struct{}{
		"github.com/anthropics/anthropic-sdk-go": {},
		"github.com/openai/openai-go/v3":         {},
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
					if _, blocked := blockedImports[importPath]; !blocked {
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
