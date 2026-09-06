package upstreamendpoint

import (
	"strings"

	"github.com/vibe-agi/vibermate/internal/originidentity"
)

// chatGPTCatalogClientVersion is the catalog compatibility contract used by
// this adapter (also covered by the Codex control-plane fixtures), not a claim
// about the user's installed CLI version. Catalog discovery has no client run.
const chatGPTCatalogClientVersion = "0.147.0"

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

// ModelsQuery negotiates the Codex catalog shape without forwarding any
// client-supplied query parameters or account metadata.
func ModelsQuery(origin originidentity.ProviderOrigin) string {
	if IsChatGPTCodexOrigin(origin) {
		return "client_version=" + chatGPTCatalogClientVersion
	}
	return ""
}
