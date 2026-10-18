package accountoperation

import (
	"encoding/json"
	"sort"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/modelcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/upstreamservice"
)

// Captured native discovery follows the same exact route aliases as inference.
// Capabilities come exclusively from the original target entry: aliases never
// borrow their old model's metadata or chain through another configured alias.
func transformCapturedModelCatalog(body []byte, route environment.CompiledRoutePlan) ([]byte, error) {
	aliases := route.ModelAliases()
	if len(aliases) == 0 {
		return body, nil
	}
	if len(body) == 0 || len(body) > upstreamservice.MaxCodexModelsBodyBytes || !utf8.Valid(body) {
		return nil, ErrInvalidResponse
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil || root == nil || protocolcore.ValidateJSONNames(body) != nil {
		return nil, ErrInvalidResponse
	}
	var models []json.RawMessage
	if json.Unmarshal(root["models"], &models) != nil || models == nil || len(models) > modelcatalog.MaxCatalogModels {
		return nil, ErrInvalidResponse
	}
	slugs := make([]string, len(models))
	source := make(map[string]map[string]json.RawMessage, len(models))
	for index, raw := range models {
		var model map[string]json.RawMessage
		var slug string
		if json.Unmarshal(raw, &model) != nil || model == nil || json.Unmarshal(model["slug"], &slug) != nil || !validCatalogModelSlug(slug) {
			return nil, ErrInvalidResponse
		}
		if _, duplicate := source[slug]; duplicate {
			return nil, ErrInvalidResponse
		}
		slugs[index], source[slug] = slug, model
	}
	sort.Slice(aliases, func(left, right int) bool { return aliases[left].RequestedModel < aliases[right].RequestedModel })
	byAlias := make(map[string]string, len(aliases))
	for _, alias := range aliases {
		byAlias[alias.RequestedModel] = alias.UpstreamModel
	}
	output := make([]json.RawMessage, 0, len(models))
	outputBytes := 0
	appendModel := func(raw json.RawMessage) error {
		outputBytes += len(raw)
		if len(output) >= modelcatalog.MaxCatalogModels || outputBytes > upstreamservice.MaxCodexModelsBodyBytes {
			return ErrInvalidResponse
		}
		output = append(output, raw)
		return nil
	}
	aliasEntry := func(alias, target string) (json.RawMessage, bool) {
		model, exists := source[target]
		if !exists {
			return nil, false
		}
		entry := make(map[string]json.RawMessage, len(model))
		for key, value := range model {
			entry[key] = value
		}
		identity, _ := json.Marshal(alias)
		entry["slug"] = identity
		for _, field := range []string{"id", "display_name"} {
			if _, present := entry[field]; present {
				entry[field] = identity
			}
		}
		raw, _ := json.Marshal(entry)
		return raw, true
	}
	modified := false
	for index, raw := range models {
		target, mapped := byAlias[slugs[index]]
		if mapped && target != slugs[index] {
			modified = true
			var available bool
			raw, available = aliasEntry(slugs[index], target)
			if !available {
				// A configured mapping to an absent target cannot advertise the
				// unrelated original alias's capabilities as usable.
				continue
			}
		}
		if err := appendModel(raw); err != nil {
			return nil, err
		}
	}
	for _, alias := range aliases {
		if _, exists := source[alias.RequestedModel]; exists {
			continue
		}
		if raw, available := aliasEntry(alias.RequestedModel, alias.UpstreamModel); available {
			if err := appendModel(raw); err != nil {
				return nil, err
			}
			modified = true
		}
	}
	if !modified {
		return body, nil
	}
	root["models"], _ = json.Marshal(output)
	encoded, err := json.Marshal(root)
	if err != nil || len(encoded) > upstreamservice.MaxCodexModelsBodyBytes {
		return nil, ErrInvalidResponse
	}
	return encoded, nil
}

func validCatalogModelSlug(slug string) bool {
	if slug == "" || len(slug) > modelcatalog.MaxModelIDBytes || !utf8.ValidString(slug) {
		return false
	}
	for _, value := range slug {
		if unicode.IsControl(value) || value == '\uFEFF' {
			return false
		}
	}
	return true
}
