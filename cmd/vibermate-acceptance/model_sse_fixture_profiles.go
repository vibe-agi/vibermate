package main

import (
	"net/http"
	"net/url"
	"sync"
)

// Private acceptance-only exact denial forms. No request is authorized to a
// public endpoint and no plugin/model directory is ever returned.
func nativeModelAuxiliaryClass(request *http.Request) string {
	if request == nil || request.URL == nil {
		return ""
	}
	u := request.URL
	if request.Method != "GET" || u.Scheme != "https" || (u.Host != "chatgpt.com" && u.Host != "chatgpt.com:443") || u.User != nil || u.RawPath != "" || u.Fragment != "" || u.ForceQuery || (request.Host != "" && request.Host != u.Host) || request.ContentLength != 0 || len(request.TransferEncoding) != 0 || (request.Body != nil && request.Body != http.NoBody) || len(request.Header.Values("Authorization")) != 1 || request.Header.Get("Authorization") != "Bearer synthetic-original-login" {
		return ""
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return ""
	}
	exact := func(want map[string]string) bool {
		if len(query) != len(want) {
			return false
		}
		for key, value := range want {
			if len(query[key]) != 1 || query.Get(key) != value {
				return false
			}
		}
		return true
	}
	switch u.Path {
	case "/backend-api/ps/plugins/list":
		if exact(map[string]string{"scope": "GLOBAL", "limit": "200"}) {
			return "list"
		}
	case "/backend-api/ps/plugins/installed":
		if exact(map[string]string{"limit": "200"}) {
			return "installed_metadata"
		}
		if exact(map[string]string{"limit": "200", "includeDownloadUrls": "true"}) {
			return "installed_bundle_metadata"
		}
	case "/backend-api/plugins/featured":
		if exact(map[string]string{"platform": "codex"}) {
			return "featured"
		}
	case "/backend-api/ps/plugins/suggested/codex":
		if exact(map[string]string{"scope": "GLOBAL"}) {
			return "suggested"
		}
	case "/backend-api/codex/models":
		if exact(map[string]string{"client_version": "0.160.0"}) {
			return "models"
		}
	case "/backend-api/wham/settings/user":
		if u.RawQuery == "" {
			return "settings"
		}
	}
	return ""
}

type nativeModelAuxiliaryGuard struct {
	mu     sync.Mutex
	counts map[string]int
	total  int
	failed bool
}

func (g *nativeModelAuxiliaryGuard) deny(class string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch class {
	case "list", "installed_metadata", "installed_bundle_metadata", "featured", "suggested", "models", "settings":
	default:
		g.failed = true
		return false
	}
	if g.counts == nil {
		g.counts = map[string]int{}
	}
	if g.failed || g.total >= 32 || g.counts[class] >= 8 {
		g.failed = true
		return false
	}
	g.total++
	g.counts[class]++
	return true
}
func (g *nativeModelAuxiliaryGuard) snapshot() (map[string]int, int, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	result := map[string]int{}
	for k, v := range g.counts {
		result[k] = v
	}
	return result, g.total, g.failed
}
