// Package cyberaccess preserves server-advertised model programs and applies an
// explicit account preference. A preference never establishes model entitlement.
package cyberaccess

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"unicode"
	"unicode/utf8"
)

const DaybreakBlue = "daybreak_blue"
const MaxCatalogBytes = 4 << 20

var ErrInvalidCatalog = errors.New("cyber access program catalog is invalid")

// Catalog keys are exact upstream model IDs. A missing or nil list is unknown;
// an empty non-nil list is an explicit, empty server advertisement.
type Catalog map[string][]string

func (catalog Catalog) Clone() Catalog {
	result := make(Catalog, len(catalog))
	for model, programs := range catalog {
		result[model] = slices.Clone(programs)
	}
	return result
}

// ParsePrograms reads available_access_programs without inventing grants from
// account names, plan names, or an omitted field. Unknown future programs remain
// visible, but only the exact daybreak_blue value can select Blue.
func ParsePrograms(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	if len(raw) > 8<<10 {
		return nil, ErrInvalidCatalog
	}
	var value struct {
		Cyber []string `json:"cyber"`
	}
	if err := json.Unmarshal(raw, &value); err != nil || len(value.Cyber) > 32 {
		return nil, ErrInvalidCatalog
	}
	seen := make(map[string]bool, len(value.Cyber))
	for _, program := range value.Cyber {
		if program == "" || len(program) > 64 || seen[program] {
			return nil, ErrInvalidCatalog
		}
		for _, c := range program {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
				return nil, ErrInvalidCatalog
			}
		}
		seen[program] = true
	}
	return value.Cyber, nil
}

// ParseCatalog accepts the native ChatGPT model directory only. Callers own the
// authenticated origin and response-size boundary.
func ParseCatalog(body []byte) (Catalog, error) {
	if len(body) == 0 || len(body) > MaxCatalogBytes || !utf8.Valid(body) {
		return nil, ErrInvalidCatalog
	}
	var wire struct {
		Models []struct {
			Slug     string          `json:"slug"`
			ID       string          `json:"id"`
			Programs json.RawMessage `json:"available_access_programs"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &wire); err != nil || wire.Models == nil || len(wire.Models) > 16<<10 {
		return nil, ErrInvalidCatalog
	}
	catalog := make(Catalog, len(wire.Models))
	for _, model := range wire.Models {
		id := model.ID
		if id == "" {
			id = model.Slug
		}
		if id == "" || len(id) > 256 {
			return nil, ErrInvalidCatalog
		}
		for _, c := range id {
			if unicode.IsControl(c) || c == '\uFEFF' {
				return nil, ErrInvalidCatalog
			}
		}
		if _, duplicate := catalog[id]; duplicate {
			return nil, ErrInvalidCatalog
		}
		programs, err := ParsePrograms(model.Programs)
		if err != nil {
			return nil, err
		}
		catalog[id] = programs
	}
	return catalog, nil
}

// PreferBlue changes only an absent or standard cyber choice for a model whose
// authenticated catalog explicitly advertises Blue. An explicit Red choice and
// malformed/unknown program values retain their original upstream semantics.
func PreferBlue(body []byte, catalog Catalog) ([]byte, bool, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil || root == nil {
		return nil, false, errors.New("Responses request is invalid")
	}
	var model string
	if json.Unmarshal(root["model"], &model) != nil || !slices.Contains(catalog[model], DaybreakBlue) {
		return body, false, nil
	}
	programs := make(map[string]json.RawMessage)
	if raw, exists := root["access_programs"]; exists {
		if json.Unmarshal(raw, &programs) != nil || programs == nil {
			return body, false, nil
		}
	}
	if raw, exists := programs["cyber"]; exists {
		var current string
		if json.Unmarshal(raw, &current) != nil || current != "standard" {
			return body, false, nil
		}
	}
	programs["cyber"] = json.RawMessage(`"daybreak_blue"`)
	encoded, err := json.Marshal(programs)
	if err != nil {
		return nil, false, err
	}
	root["access_programs"] = encoded
	encoded, err = json.Marshal(root)
	return encoded, err == nil, err
}
