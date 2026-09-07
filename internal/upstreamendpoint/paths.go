package upstreamendpoint

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/vibe-agi/vibermate/internal/originidentity"
)

// chatGPTCatalogClientVersion is the catalog compatibility contract used by
// this adapter (also covered by the Codex control-plane fixtures), not a claim
// about the user's installed CLI version. Catalog discovery has no client run.
const chatGPTCatalogClientVersion = "0.153.4"

// IsChatGPTCodexOrigin identifies the Codex service, not every endpoint that
// happens to speak Responses. It never changes the credential's origin/realm.
func IsChatGPTCodexOrigin(origin originidentity.ProviderOrigin) bool {
	if origin.Scheme() != "https" || origin.Host() != "chatgpt.com" || origin.Port() != 443 {
		return false
	}
	switch origin.BasePath() {
	case "", "/backend-api", "/backend-api/codex":
		return true
	default:
		return false
	}
}

// ProviderRelativePath resolves a codec operation against the selected service.
// Callers must do this before scripts and request freezing, so both see the
// same destination path that is sent and recorded. The origin stays unchanged.
func ProviderRelativePath(origin originidentity.ProviderOrigin, codecPath string) string {
	if !IsChatGPTCodexOrigin(origin) || codecPath != "v1/responses" {
		return codecPath
	}
	return strings.TrimPrefix("/backend-api/codex/responses", origin.BasePath()+"/")
}

// ModelsPath is absolute, including the configured base path. Discovery is
// endpoint-owned; client request paths must not select the upstream API family.
func ModelsPath(origin originidentity.ProviderOrigin) string {
	if IsChatGPTCodexOrigin(origin) {
		return "/backend-api/codex/models"
	}
	basePath := strings.TrimSuffix(origin.BasePath(), "/")
	if strings.HasSuffix(basePath, "/v1") {
		return basePath + "/models"
	}
	return basePath + "/v1/models"
}

// ModelsQuery negotiates the Codex catalog with the account's final outbound
// headers, after authentication and account overrides. Codex sends its version
// in this query, independently of User-Agent. A fixed older query can therefore
// hide newer models even when an account explicitly selects a newer client UA.
// Only a bounded numeric version is copied; no arbitrary header values, client
// run metadata, or queries enter the discovery URL.
func ModelsQuery(origin originidentity.ProviderOrigin, headers http.Header) string {
	if !IsChatGPTCodexOrigin(origin) {
		return ""
	}
	version := catalogNumericVersion(headers.Get("Version"))
	if version == "" {
		product, _, _ := strings.Cut(headers.Get("User-Agent"), " ")
		name, value, _ := strings.Cut(product, "/")
		switch name {
		case "codex", "codex-tui", "codex_cli_rs":
			version = catalogNumericVersion(value)
		}
	}
	if version == "" {
		version = chatGPTCatalogClientVersion
	}
	return "client_version=" + version
}

func catalogNumericVersion(value string) string {
	if len(value) == 0 || len(value) > 64 {
		return ""
	}
	// Like Codex's client_version_to_whole, exclude prerelease/build labels.
	value, _, _ = strings.Cut(value, "-")
	value, _, _ = strings.Cut(value, "+")
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return ""
	}
	for index, part := range parts {
		if part == "" || strings.IndexFunc(part, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return ""
		}
		number, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return ""
		}
		parts[index] = strconv.FormatUint(number, 10)
	}
	return strings.Join(parts, ".")
}
