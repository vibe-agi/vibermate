package systemtrust

import (
	"bytes"
	"crypto/sha1" // #nosec G505 -- macOS keys trustList by the certificate SHA-1.
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
)

const (
	maxTrustSettingsExportBytes = 1 << 20
	maxPlistDepth               = 16
	maxPlistCollectionEntries   = 2048
)

// parseMacOSExportTrustDecision reads the XML plist produced by
// `security trust-settings-export -d`. Apple keys trustList by the exact
// certificate SHA-1, so the lookup is identity-bound even when several
// certificates share the same display name. SHA-1 is used only as Apple's
// lookup key; ViberMate's public identity remains SHA-256.
func parseMacOSExportTrustDecision(
	output []byte,
	root publicRoot,
) (TrustDecision, error) {
	if len(output) == 0 || len(output) > maxTrustSettingsExportBytes ||
		!root.valid() {
		return TrustDecisionUnknown, ErrObservationUnknown
	}
	value, err := parseXMLPlist(output)
	if err != nil {
		return TrustDecisionUnknown, ErrObservationUnknown
	}
	top, ok := value.(map[string]any)
	if !ok {
		return TrustDecisionUnknown, ErrObservationUnknown
	}
	trustValue, exists := top["trustList"]
	if !exists {
		return TrustDecisionUnknown, ErrObservationUnknown
	}
	trustList, ok := trustValue.(map[string]any)
	if !ok {
		return TrustDecisionUnknown, ErrObservationUnknown
	}
	digest := sha1.Sum(root.certificateDER) // #nosec G401 -- macOS lookup key.
	expectedKey := strings.ToUpper(hex.EncodeToString(digest[:]))
	var entryValue any
	matches := 0
	for key, candidate := range trustList {
		if strings.EqualFold(key, expectedKey) {
			entryValue = candidate
			matches++
		}
	}
	if matches == 0 {
		return TrustDecisionUntrusted, nil
	}
	if matches != 1 {
		return TrustDecisionUnknown, ErrObservationUnknown
	}
	entry, ok := entryValue.(map[string]any)
	if !ok {
		return TrustDecisionUnknown, ErrObservationUnknown
	}
	settingsValue, exists := entry["trustSettings"]
	if !exists {
		// trust-settings-export also lists certificate objects that have no
		// effective trust settings. Their presence must not be shown as trust.
		return TrustDecisionUntrusted, nil
	}
	settings, ok := settingsValue.([]any)
	if !ok {
		return TrustDecisionUnknown, ErrObservationUnknown
	}
	// Apple defines a present but empty trust-settings array as unconditional
	// Root trust. This is distinct from a missing trustSettings key, which means
	// that the certificate must chain to another known anchor.
	if len(settings) == 0 {
		return TrustDecisionTrusted, nil
	}
	trusted := false
	denied := false
	for _, rawSetting := range settings {
		setting, ok := rawSetting.(map[string]any)
		if !ok {
			return TrustDecisionUnknown, ErrObservationUnknown
		}
		policyName, hasPolicyName := setting["kSecTrustSettingsPolicyName"]
		policyData, hasPolicyData := setting["kSecTrustSettingsPolicy"]
		relevant := !hasPolicyName && !hasPolicyData
		if hasPolicyName {
			name, validName := policyName.(string)
			if !validName {
				return TrustDecisionUnknown, ErrObservationUnknown
			}
			relevant = name == "sslServer"
		} else if hasPolicyData {
			// A policy OID without its exported name cannot be safely mapped to
			// server TLS without depending on an undocumented binary value.
			if _, validData := policyData.(plistData); !validData {
				return TrustDecisionUnknown, ErrObservationUnknown
			}
		}
		if !relevant {
			continue
		}
		resultValue, hasResult := setting["kSecTrustSettingsResult"]
		if !hasResult {
			// SecTrustSettings defines an omitted result as TrustRoot. Keep this
			// explicit here because Security.framework may preserve that compact
			// representation when it exports a policy-scoped setting.
			trusted = true
			continue
		}
		result, validResult := resultValue.(int64)
		if !validResult {
			return TrustDecisionUnknown, ErrObservationUnknown
		}
		switch result {
		case 1, 2: // kSecTrustSettingsResultTrustRoot / TrustAsRoot
			trusted = true
		case 3: // kSecTrustSettingsResultDeny
			denied = true
		default:
			return TrustDecisionUnknown, ErrObservationUnknown
		}
	}
	if trusted && denied {
		return TrustDecisionUnknown, ErrObservationUnknown
	}
	if trusted {
		return TrustDecisionTrusted, nil
	}
	return TrustDecisionUntrusted, nil
}

// plistData prevents policy OID bytes from being confused with a string.
// Decoding base64 is unnecessary because unknown OIDs are deliberately not
// interpreted by this bounded parser.
type plistData string

type plistParser struct {
	decoder *xml.Decoder
	nodes   int
}

func parseXMLPlist(input []byte) (any, error) {
	parser := plistParser{decoder: xml.NewDecoder(bytes.NewReader(input))}
	token, err := parser.nextSignificant()
	if err != nil {
		return nil, err
	}
	start, ok := token.(xml.StartElement)
	if !ok || start.Name.Space != "" || start.Name.Local != "plist" ||
		!plistVersionOne(start.Attr) {
		return nil, ErrObservationUnknown
	}
	token, err = parser.nextSignificant()
	if err != nil {
		return nil, err
	}
	valueStart, ok := token.(xml.StartElement)
	if !ok {
		return nil, ErrObservationUnknown
	}
	value, err := parser.value(valueStart, 0)
	if err != nil {
		return nil, err
	}
	token, err = parser.nextSignificant()
	end, ok := token.(xml.EndElement)
	if err != nil || !ok || end.Name != start.Name {
		return nil, ErrObservationUnknown
	}
	if _, err := parser.nextSignificant(); !errors.Is(err, io.EOF) {
		return nil, ErrObservationUnknown
	}
	return value, nil
}

func plistVersionOne(attributes []xml.Attr) bool {
	return len(attributes) == 1 && attributes[0].Name.Space == "" &&
		attributes[0].Name.Local == "version" && attributes[0].Value == "1.0"
}

func (parser *plistParser) value(start xml.StartElement, depth int) (any, error) {
	parser.nodes++
	if depth > maxPlistDepth || parser.nodes > maxPlistCollectionEntries*4 ||
		start.Name.Space != "" || len(start.Attr) != 0 {
		return nil, ErrObservationUnknown
	}
	switch start.Name.Local {
	case "dict":
		return parser.dictionary(start, depth+1)
	case "array":
		return parser.array(start, depth+1)
	case "string", "date", "real":
		return parser.text(start)
	case "data":
		value, err := parser.text(start)
		return plistData(value), err
	case "integer":
		value, err := parser.text(start)
		if err != nil {
			return nil, err
		}
		integer, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, ErrObservationUnknown
		}
		return integer, nil
	case "true", "false":
		if err := parser.empty(start); err != nil {
			return nil, err
		}
		return start.Name.Local == "true", nil
	default:
		return nil, ErrObservationUnknown
	}
}

func (parser *plistParser) dictionary(
	start xml.StartElement,
	depth int,
) (map[string]any, error) {
	result := make(map[string]any)
	for {
		token, err := parser.nextSignificant()
		if err != nil {
			return nil, ErrObservationUnknown
		}
		if end, ok := token.(xml.EndElement); ok {
			if end.Name != start.Name {
				return nil, ErrObservationUnknown
			}
			return result, nil
		}
		if len(result) >= maxPlistCollectionEntries {
			return nil, ErrObservationUnknown
		}
		keyStart, ok := token.(xml.StartElement)
		if !ok || keyStart.Name.Space != "" || keyStart.Name.Local != "key" ||
			len(keyStart.Attr) != 0 {
			return nil, ErrObservationUnknown
		}
		key, err := parser.text(keyStart)
		if err != nil || key == "" {
			return nil, ErrObservationUnknown
		}
		if _, duplicate := result[key]; duplicate {
			return nil, ErrObservationUnknown
		}
		token, err = parser.nextSignificant()
		valueStart, ok := token.(xml.StartElement)
		if err != nil || !ok {
			return nil, ErrObservationUnknown
		}
		value, err := parser.value(valueStart, depth)
		if err != nil {
			return nil, err
		}
		result[key] = value
	}
}

func (parser *plistParser) array(
	start xml.StartElement,
	depth int,
) ([]any, error) {
	result := make([]any, 0)
	for {
		token, err := parser.nextSignificant()
		if err != nil {
			return nil, ErrObservationUnknown
		}
		if end, ok := token.(xml.EndElement); ok {
			if end.Name != start.Name {
				return nil, ErrObservationUnknown
			}
			return result, nil
		}
		if len(result) >= maxPlistCollectionEntries {
			return nil, ErrObservationUnknown
		}
		valueStart, ok := token.(xml.StartElement)
		if !ok {
			return nil, ErrObservationUnknown
		}
		value, err := parser.value(valueStart, depth)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
}

func (parser *plistParser) text(start xml.StartElement) (string, error) {
	var value strings.Builder
	for {
		token, err := parser.decoder.Token()
		if err != nil {
			return "", ErrObservationUnknown
		}
		switch typed := token.(type) {
		case xml.CharData:
			value.Write([]byte(typed))
		case xml.Comment:
		case xml.EndElement:
			if typed.Name != start.Name {
				return "", ErrObservationUnknown
			}
			return strings.TrimSpace(value.String()), nil
		default:
			return "", ErrObservationUnknown
		}
	}
}

func (parser *plistParser) empty(start xml.StartElement) error {
	token, err := parser.nextSignificant()
	end, ok := token.(xml.EndElement)
	if err != nil || !ok || end.Name != start.Name {
		return ErrObservationUnknown
	}
	return nil
}

func (parser *plistParser) nextSignificant() (xml.Token, error) {
	for {
		token, err := parser.decoder.Token()
		if err != nil {
			return nil, err
		}
		switch typed := token.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(typed)) != "" {
				return nil, ErrObservationUnknown
			}
		case xml.Comment, xml.Directive, xml.ProcInst:
		default:
			return token, nil
		}
	}
}
