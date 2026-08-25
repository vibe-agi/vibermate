package runlauncher

import (
	"errors"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

const clientCredentialPlaceholder = "vibermate-local-proxy"

const claudeDisableNonStreamingFallback = "CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK"

var managedEnvironment = map[string]struct{}{
	"HTTP_PROXY":                   {},
	"HTTPS_PROXY":                  {},
	"ALL_PROXY":                    {},
	"NO_PROXY":                     {},
	"VIBERMATE_CAPTURE_RUN_ID":     {},
	"VIBERMATE_CONNECTION":         {},
	"VIBERMATE_CREDENTIAL_FILE":    {},
	"VIBERMATE_TOKEN":              {},
	"VIBERMATE_CONTROL_CREDENTIAL": {},
	"VIBERMATE_ENROLLMENT_TOKEN":   {},
	"VIBERMATE_ADMIN_TOKEN":        {},
	"VIBERMATE_DISCOVERY_PATH":     {},
	"NODE_EXTRA_CA_CERTS":          {},
	"NODE_USE_ENV_PROXY":           {},
}

var codexManagedEnvironment = map[string]struct{}{
	"SSL_CERT_FILE":       {},
	"REQUESTS_CA_BUNDLE":  {},
	"CURL_CA_BUNDLE":      {},
	"OPENAI_BASE_URL":     {},
	"CODEX_BASE_URL":      {},
	"OPENAI_API_KEY":      {},
	"CODEX_API_KEY":       {},
	"OPENAI_ORGANIZATION": {},
	"OPENAI_PROJECT":      {},
	"OPENAI_ORG_ID":       {},
	"OPENAI_PROJECT_ID":   {},
}

var claudeManagedEnvironment = map[string]struct{}{
	"ANTHROPIC_API_KEY":          {},
	"ANTHROPIC_AUTH_TOKEN":       {},
	"ANTHROPIC_BASE_URL":         {},
	"ANTHROPIC_BEDROCK_BASE_URL": {},
	"ANTHROPIC_CUSTOM_HEADERS":   {},
	"ANTHROPIC_FOUNDRY_BASE_URL": {},
	"ANTHROPIC_VERTEX_BASE_URL":  {},
	"CLAUDE_CODE_OAUTH_TOKEN":    {},
	"CLAUDE_CODE_USE_BEDROCK":    {},
	"CLAUDE_CODE_USE_FOUNDRY":    {},
	"CLAUDE_CODE_USE_VERTEX":     {},
}

func buildEnvironment(
	base []string,
	grant capturecontrol.LaunchGrant,
) ([]string, error) {
	if err := validateGrant(grant); err != nil {
		return nil, err
	}
	proxy, err := authenticatedProxyURL(
		grant.ProxyAddress,
		grant.ProxyToken,
	)
	if err != nil {
		return nil, err
	}
	managedClientCredential := usesManagedClientCredential(base, grant)
	preserved := make(map[string]string)
	deletedByEnvironment := make(
		map[string]struct{},
		len(grant.LaunchEnvironment.DeleteEnv),
	)
	for _, key := range grant.LaunchEnvironment.DeleteEnv {
		deletedByEnvironment[key] = struct{}{}
	}
	var noProxyValues []string
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		if strings.EqualFold(key, "NO_PROXY") {
			noProxyValues = append(noProxyValues, value)
			continue
		}
		if _, deleted := deletedByEnvironment[key]; deleted {
			continue
		}
		if _, replaced := grant.LaunchEnvironment.SetEnv[key]; replaced {
			continue
		}
		if environmentManaged(
			key,
			grant.LaunchRecipe,
			managedClientCredential,
		) || launchPolicyManagesEnvironment(key, grant) {
			continue
		}
		preserved[key] = value
	}
	for key, value := range grant.LaunchEnvironment.SetEnv {
		preserved[key] = value
	}
	noProxy := safeNoProxy(noProxyValues, grant.ProtectedAuthorities)
	preserved["HTTP_PROXY"] = proxy
	preserved["HTTPS_PROXY"] = proxy
	preserved["http_proxy"] = proxy
	preserved["https_proxy"] = proxy
	preserved["NO_PROXY"] = noProxy
	preserved["no_proxy"] = noProxy
	preserved["VIBERMATE_CAPTURE_RUN_ID"] = grant.Run.ID
	if grant.Adapter != nil &&
		grant.Adapter.StreamingFallbackPolicy ==
			clientadapter.StreamingFallbackCoreOwned {
		preserved[claudeDisableNonStreamingFallback] = "1"
	}
	switch grant.LaunchRecipe {
	case clientadapter.LaunchGeneric:
	case clientadapter.LaunchNodeEnvProxy:
		preserved["NODE_EXTRA_CA_CERTS"] = grant.RootPEMPath
		preserved["NODE_USE_ENV_PROXY"] = "1"
		if managedClientCredential {
			// Claude Code treats ANTHROPIC_API_KEY as an explicit user
			// credential and enters its custom-key onboarding flow. A managed
			// Environment must instead use the documented gateway-token input:
			// the placeholder only makes the client emit an authenticated
			// request, then Core removes it and applies the selected account at
			// the final provider boundary.
			preserved["ANTHROPIC_AUTH_TOKEN"] = clientCredentialPlaceholder
		}
	case clientadapter.LaunchCodexResponsesHTTP:
		preserved["SSL_CERT_FILE"] = grant.RootPEMPath
		if managedClientCredential {
			preserved["CODEX_API_KEY"] = clientCredentialPlaceholder
		}
	default:
		return nil, errors.New("CaptureRun launch recipe is unsupported")
	}
	keys := make([]string, 0, len(preserved))
	for key := range preserved {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+preserved[key])
	}
	return result, nil
}

func launchPolicyManagesEnvironment(
	key string,
	grant capturecontrol.LaunchGrant,
) bool {
	return grant.Adapter != nil &&
		grant.Adapter.StreamingFallbackPolicy ==
			clientadapter.StreamingFallbackCoreOwned &&
		strings.EqualFold(key, claudeDisableNonStreamingFallback)
}

func environmentManaged(
	key string,
	recipe clientadapter.LaunchRecipe,
	managedClientCredential bool,
) bool {
	normalizedKey := strings.ToUpper(key)
	if _, managed := managedEnvironment[normalizedKey]; managed {
		return true
	}
	if !managedClientCredential {
		return false
	}
	if recipe == clientadapter.LaunchCodexResponsesHTTP {
		_, managed := codexManagedEnvironment[normalizedKey]
		return managed
	}
	if recipe == clientadapter.LaunchNodeEnvProxy {
		_, managed := claudeManagedEnvironment[normalizedKey]
		return managed
	}
	return false
}

func usesManagedClientCredential(
	base []string,
	grant capturecontrol.LaunchGrant,
) bool {
	if len(grant.ManagedCredentialAuthorities) == 0 {
		return false
	}
	clientID := ""
	if grant.Adapter != nil {
		clientID = grant.Adapter.ID
	} else if grant.Signer != nil {
		clientID = grant.Signer.ID
	}
	authority, ok := clientTargetAuthority(base, clientID)
	if !ok {
		return false
	}
	for _, managed := range grant.ManagedCredentialAuthorities {
		if strings.EqualFold(managed, authority) {
			return true
		}
	}
	return false
}

func clientTargetAuthority(base []string, clientID string) (string, bool) {
	rawOrigin := ""
	switch clientID {
	case "claude-code":
		rawOrigin = environmentValue(base, "ANTHROPIC_BASE_URL")
		if rawOrigin == "" {
			rawOrigin = "https://api.anthropic.com"
		}
	case "codex-cli":
		// The same answer the launch recipe writes into the child's provider
		// configuration, so the managed-credential decision cannot be made for a
		// host the child never contacts.
		origin, err := codexOrigin(base)
		if err != nil {
			return "", false
		}
		rawOrigin = origin
	default:
		return "", false
	}
	parsed, err := url.Parse(rawOrigin)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	port := parsed.Port()
	switch parsed.Scheme {
	case "https":
		if port == "" {
			port = "443"
		}
	case "http":
		if port == "" {
			port = "80"
		}
	default:
		return "", false
	}
	return net.JoinHostPort(strings.ToLower(parsed.Hostname()), port), true
}

func authenticatedProxyURL(origin string, capability string) (string, error) {
	proxy, err := url.Parse(origin)
	if err != nil ||
		proxy.Scheme != "http" ||
		proxy.Hostname() != "127.0.0.1" ||
		proxy.Port() == "" ||
		proxy.User != nil ||
		proxy.Path != "" ||
		proxy.RawPath != "" ||
		proxy.RawQuery != "" ||
		proxy.Fragment != "" ||
		capability == "" {
		return "", errors.New("CaptureRun proxy origin or capability is invalid")
	}
	proxy.User = url.UserPassword("capture", capability)
	return proxy.String(), nil
}

func safeNoProxy(values []string, protected []string) string {
	protectedEndpoints := make([]protectedEndpoint, 0, len(protected))
	for _, authority := range protected {
		host, port, err := netSplitAuthority(authority)
		if err == nil {
			protectedEndpoints = append(
				protectedEndpoints,
				protectedEndpoint{host: host, port: port},
			)
		}
	}
	var safe []string
	seen := make(map[string]struct{})
	for _, value := range values {
		for _, raw := range strings.Split(value, ",") {
			entry := strings.TrimSpace(raw)
			if entry == "" || noProxyCanCover(entry, protectedEndpoints) {
				continue
			}
			if _, duplicate := seen[entry]; duplicate {
				continue
			}
			seen[entry] = struct{}{}
			safe = append(safe, entry)
		}
	}
	return strings.Join(safe, ",")
}

type protectedEndpoint struct {
	host string
	port uint16
}

func noProxyCanCover(entry string, protected []protectedEndpoint) bool {
	lower := strings.ToLower(strings.TrimSpace(entry))
	if lower == "" || lower == "*" {
		return true
	}
	if strings.ContainsAny(lower, " \t\r\n/") {
		return true
	}
	entryHost := lower
	var entryPort uint16
	if host, portText, err := net.SplitHostPort(lower); err == nil {
		entryHost = strings.Trim(host, "[]")
		port, parseErr := strconv.ParseUint(portText, 10, 16)
		if parseErr != nil || port == 0 {
			return true
		}
		entryPort = uint16(port)
	} else {
		entryHost = strings.Trim(entryHost, "[]")
	}
	entryHost = strings.TrimPrefix(entryHost, "*")
	entryHost = strings.TrimPrefix(entryHost, ".")
	if entryHost == "" {
		return true
	}
	for _, endpoint := range protected {
		if entryPort != 0 && entryPort != endpoint.port {
			continue
		}
		if endpoint.host == entryHost ||
			strings.HasSuffix(endpoint.host, "."+entryHost) {
			return true
		}
	}
	return false
}
