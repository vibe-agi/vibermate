// Package locales provides the two production message catalogs through an
// explicit immutable constructor.
package locales

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type Locale string

const (
	EnglishUS         Locale = "en-US"
	SimplifiedChinese Locale = "zh-CN"
)

var placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

//go:embed en-US.json zh-CN.json
var files embed.FS

type Catalogs struct {
	values map[Locale]map[string]string
}

func New() (*Catalogs, error) {
	values := make(map[Locale]map[string]string, 2)
	supported := []Locale{EnglishUS, SimplifiedChinese}
	for _, locale := range supported {
		payload, err := files.ReadFile(string(locale) + ".json")
		if err != nil {
			return nil, err
		}
		var catalog map[string]string
		if err := json.Unmarshal(payload, &catalog); err != nil {
			return nil, err
		}
		if len(catalog) == 0 {
			return nil, errors.New("locale catalog is empty")
		}
		for key, value := range catalog {
			if key == "" || strings.TrimSpace(value) == "" {
				return nil, errors.New("locale catalog contains an empty key or value")
			}
		}
		values[locale] = catalog
	}
	english := values[EnglishUS]
	chinese := values[SimplifiedChinese]
	if len(english) != len(chinese) {
		return nil, errors.New("production locale keys do not match")
	}
	for key, englishTemplate := range english {
		chineseTemplate, exists := chinese[key]
		if !exists {
			return nil, errors.New("production locale keys do not match")
		}
		if !equalStrings(
			parameterNames(englishTemplate),
			parameterNames(chineseTemplate),
		) {
			return nil, fmt.Errorf(
				"production locale parameters do not match for key %q",
				key,
			)
		}
	}
	return &Catalogs{values: values}, nil
}

func (catalogs *Catalogs) Render(
	locale Locale,
	key string,
	parameters map[string]string,
) (string, error) {
	if catalogs == nil {
		return "", errors.New("locale catalogs are unavailable")
	}
	catalog := catalogs.values[locale]
	if catalog == nil {
		return "", errors.New("locale is unsupported")
	}
	template, exists := catalog[key]
	if !exists || template == "" {
		return "", fmt.Errorf("locale key %q is unavailable", key)
	}
	names := parameterNames(template)
	expected := make(map[string]struct{}, len(names))
	for _, name := range names {
		expected[name] = struct{}{}
	}
	if len(expected) != len(parameters) {
		return "", errors.New("locale parameters do not match the template")
	}
	for name := range parameters {
		if _, exists := expected[name]; !exists {
			return "", errors.New("locale parameter is unexpected")
		}
	}
	return placeholder.ReplaceAllStringFunc(template, func(match string) string {
		name := placeholder.FindStringSubmatch(match)[1]
		return parameters[name]
	}), nil
}

// Parameters returns the stable sorted parameter names for one production
// message key. New guarantees the same names for both locales.
func (catalogs *Catalogs) Parameters(key string) ([]string, error) {
	if catalogs == nil {
		return nil, errors.New("locale catalogs are unavailable")
	}
	template, exists := catalogs.values[EnglishUS][key]
	if !exists {
		return nil, fmt.Errorf("locale key %q is unavailable", key)
	}
	return parameterNames(template), nil
}

func Detect(environment []string) Locale {
	values := make(map[string]string)
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if found {
			values[key] = value
		}
	}
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		raw := strings.ToLower(values[name])
		if strings.HasPrefix(raw, "zh_cn") ||
			strings.HasPrefix(raw, "zh-cn") ||
			strings.HasPrefix(raw, "zh_hans") {
			return SimplifiedChinese
		}
		if raw != "" {
			return EnglishUS
		}
	}
	return EnglishUS
}

func (catalogs *Catalogs) Keys(locale Locale) []string {
	if catalogs == nil {
		return nil
	}
	catalog := catalogs.values[locale]
	keys := make([]string, 0, len(catalog))
	for key := range catalog {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func parameterNames(template string) []string {
	matches := placeholder.FindAllStringSubmatch(template, -1)
	unique := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		unique[match[1]] = struct{}{}
	}
	names := make([]string, 0, len(unique))
	for name := range unique {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func equalStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
