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

const codexClientPlaceholder = "vibermate-local-proxy"

var managedEnvironment = map[string]struct{}{
	"HTTP_PROXY":               {},
	"HTTPS_PROXY":              {},
	"http_proxy":               {},
	"https_proxy":              {},
	"ALL_PROXY":                {},
	"all_proxy":                {},
	"NO_PROXY":                 {},
	"no_proxy":                 {},
	"VIBERMATE_CAPTURE_RUN_ID": {},
	"NODE_EXTRA_CA_CERTS":      {},
	"NODE_USE_ENV_PROXY":       {},
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

func buildEnvironment(
	base []string,
	grant capturecontrol.LaunchGrant,
) ([]string, error) {
	if err := validateGrant(grant); err != nil {
		return nil, err
	}
	proxy, err := authenticatedProxyURL(
		grant.ProxyOrigin,
		grant.ProxyCapability,
	)
	if err != nil {
		return nil, err
	}
	preserved := make(map[string]string)
	var noProxyValues []string
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		if key == "NO_PROXY" || key == "no_proxy" {
			noProxyValues = append(noProxyValues, value)
			continue
		}
		if environmentManaged(key, grant.LaunchRecipe) {
			continue
		}
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
	switch grant.LaunchRecipe {
	case clientadapter.LaunchGeneric:
	case clientadapter.LaunchNodeEnvProxy:
		preserved["NODE_EXTRA_CA_CERTS"] = grant.RootPEMPath
		preserved["NODE_USE_ENV_PROXY"] = "1"
	case clientadapter.LaunchSSLCertFile:
		preserved["SSL_CERT_FILE"] = grant.RootPEMPath
		preserved["CODEX_API_KEY"] = codexClientPlaceholder
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

func environmentManaged(
	key string,
	recipe clientadapter.LaunchRecipe,
) bool {
	if _, managed := managedEnvironment[key]; managed {
		return true
	}
	if recipe == clientadapter.LaunchSSLCertFile {
		_, managed := codexManagedEnvironment[key]
		return managed
	}
	return false
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
