package providerauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"
)

const (
	materialVersion      = 1
	maxCredentialBytes   = 32 << 10
	maxHeaderRules       = 64
	maxHeaderNameBytes   = 256
	maxHeaderValueBytes  = 16 << 10
	maxMaterialWireBytes = 64 << 10
)

var transportOwnedHeaders = map[string]struct{}{
	"connection":        {},
	"content-length":    {},
	"host":              {},
	"keep-alive":        {},
	"proxy-connection":  {},
	"te":                {},
	"trailer":           {},
	"transfer-encoding": {},
	"upgrade":           {},
}

type HeaderAssignment struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// HeaderPolicy is the canonical, exact outbound Header mutation owned by one
// Account credential epoch. Delete is applied before Set; the two collections
// cannot name the same field.
type HeaderPolicy struct {
	Set    []HeaderAssignment `json:"set"`
	Delete []string           `json:"delete"`
}

func (policy HeaderPolicy) Clone() HeaderPolicy {
	return HeaderPolicy{
		Set:    append([]HeaderAssignment(nil), policy.Set...),
		Delete: append([]string(nil), policy.Delete...),
	}
}

func (policy HeaderPolicy) Apply(header http.Header) error {
	if header == nil || policy.Validate() != nil {
		return ErrInvalidAuthentication
	}
	for _, name := range policy.Delete {
		header.Del(name)
	}
	for _, assignment := range policy.Set {
		header.Set(assignment.Name, assignment.Value)
	}
	return nil
}

func (policy HeaderPolicy) SensitiveHeaderNames() []string {
	result := make([]string, 0, len(policy.Set))
	for _, assignment := range policy.Set {
		result = append(result, assignment.Name)
	}
	return result
}

func (policy HeaderPolicy) Validate() error {
	if len(policy.Set)+len(policy.Delete) > maxHeaderRules {
		return ErrInvalidAuthentication
	}
	seen := make(map[string]struct{}, len(policy.Set)+len(policy.Delete))
	previous := ""
	for _, assignment := range policy.Set {
		canonical, err := canonicalPolicyHeaderName(assignment.Name)
		if err != nil || canonical != assignment.Name || canonical <= previous ||
			len(assignment.Value) > maxHeaderValueBytes ||
			!utf8.ValidString(assignment.Value) ||
			!httpguts.ValidHeaderFieldValue(assignment.Value) {
			return ErrInvalidAuthentication
		}
		seen[strings.ToLower(canonical)] = struct{}{}
		previous = canonical
	}
	previous = ""
	for _, name := range policy.Delete {
		canonical, err := canonicalPolicyHeaderName(name)
		key := strings.ToLower(canonical)
		if err != nil || canonical != name || canonical <= previous {
			return ErrInvalidAuthentication
		}
		if _, duplicate := seen[key]; duplicate {
			return ErrInvalidAuthentication
		}
		seen[key] = struct{}{}
		previous = canonical
	}
	return nil
}

// ValidateForDriver keeps the credential transport Header under the driver's
// sole authority. A HeaderPolicy may add another authentication scheme, but it
// cannot silently override or delete the primary Header that its Account kind
// promises to send.
func (policy HeaderPolicy) ValidateForDriver(driver DriverRef) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	var primary string
	switch driver {
	case StaticHeaderDriverRef():
		primary = "Authorization"
	case AnthropicAPIKeyDriverRef():
		primary = "X-Api-Key"
	default:
		return ErrInvalidAuthentication
	}
	for _, assignment := range policy.Set {
		if strings.EqualFold(assignment.Name, primary) {
			return ErrInvalidAuthentication
		}
	}
	for _, name := range policy.Delete {
		if strings.EqualFold(name, primary) {
			return ErrInvalidAuthentication
		}
	}
	return nil
}

type Material struct {
	credential []byte
	policy     HeaderPolicy
}

type materialWire struct {
	Version    int               `json:"version"`
	Credential string            `json:"credential"`
	SetHeaders map[string]string `json:"setHeaders"`
	DelHeaders []string          `json:"deleteHeaders"`
}

func NewMaterial(
	credential string,
	setHeaders map[string]string,
	deleteHeaders []string,
) (Material, error) {
	if !validCredential(credential) || len(setHeaders)+len(deleteHeaders) > maxHeaderRules {
		return Material{}, ErrInvalidAuthentication
	}
	set := make([]HeaderAssignment, 0, len(setHeaders))
	seen := make(map[string]struct{}, len(setHeaders)+len(deleteHeaders))
	for name, value := range setHeaders {
		canonical, err := canonicalPolicyHeaderName(name)
		key := strings.ToLower(canonical)
		if err != nil || len(value) > maxHeaderValueBytes || !utf8.ValidString(value) ||
			!httpguts.ValidHeaderFieldValue(value) {
			return Material{}, ErrInvalidAuthentication
		}
		if _, duplicate := seen[key]; duplicate {
			return Material{}, ErrInvalidAuthentication
		}
		seen[key] = struct{}{}
		set = append(set, HeaderAssignment{Name: canonical, Value: value})
	}
	deleted := make([]string, 0, len(deleteHeaders))
	for _, name := range deleteHeaders {
		canonical, err := canonicalPolicyHeaderName(name)
		key := strings.ToLower(canonical)
		if err != nil {
			return Material{}, ErrInvalidAuthentication
		}
		if _, duplicate := seen[key]; duplicate {
			return Material{}, ErrInvalidAuthentication
		}
		seen[key] = struct{}{}
		deleted = append(deleted, canonical)
	}
	sort.Slice(set, func(left, right int) bool { return set[left].Name < set[right].Name })
	sort.Strings(deleted)
	policy := HeaderPolicy{Set: set, Delete: deleted}
	if err := policy.Validate(); err != nil {
		return Material{}, err
	}
	return Material{credential: []byte(credential), policy: policy}, nil
}

func ParseMaterial(encoded []byte) (Material, error) {
	if len(encoded) == 0 || len(encoded) > maxMaterialWireBytes ||
		bytes.IndexByte(encoded, 0) >= 0 {
		return Material{}, ErrInvalidAuthentication
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var wire materialWire
	if err := decoder.Decode(&wire); err != nil {
		return Material{}, ErrInvalidAuthentication
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) ||
		wire.Version != materialVersion {
		return Material{}, ErrInvalidAuthentication
	}
	return NewMaterial(wire.Credential, wire.SetHeaders, wire.DelHeaders)
}

func (material Material) MarshalBinary() ([]byte, error) {
	if !validCredential(string(material.credential)) || material.policy.Validate() != nil {
		return nil, ErrInvalidAuthentication
	}
	setHeaders := make(map[string]string, len(material.policy.Set))
	for _, assignment := range material.policy.Set {
		setHeaders[assignment.Name] = assignment.Value
	}
	encoded, err := json.Marshal(materialWire{
		Version: materialVersion, Credential: string(material.credential),
		SetHeaders: setHeaders, DelHeaders: append([]string(nil), material.policy.Delete...),
	})
	if err != nil || len(encoded) > maxMaterialWireBytes {
		clear(encoded)
		return nil, ErrInvalidAuthentication
	}
	return encoded, nil
}

func (material Material) CredentialBytes() []byte {
	return bytes.Clone(material.credential)
}

func (material Material) HeaderPolicy() HeaderPolicy { return material.policy.Clone() }

func (material *Material) Destroy() {
	if material == nil {
		return
	}
	clear(material.credential)
	material.credential = nil
	for index := range material.policy.Set {
		material.policy.Set[index].Value = ""
	}
	material.policy = HeaderPolicy{}
}

func validCredential(value string) bool {
	return value != "" && len(value) <= maxCredentialBytes && utf8.ValidString(value) &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func canonicalPolicyHeaderName(name string) (string, error) {
	if name == "" || len(name) > maxHeaderNameBytes || strings.TrimSpace(name) != name ||
		!httpguts.ValidHeaderFieldName(name) {
		return "", errors.New("Header name is invalid")
	}
	canonical := http.CanonicalHeaderKey(name)
	if _, reserved := transportOwnedHeaders[strings.ToLower(canonical)]; reserved {
		return "", errors.New("Header is transport-owned")
	}
	return canonical, nil
}
