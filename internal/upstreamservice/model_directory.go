package upstreamservice

import (
	"encoding/json"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// WithRawQuery permits only the bounded native catalog-version parameter. It
// cannot change an account read's URL, identity, method, headers or payload.
func (read Read) WithRawQuery(raw string) (Read, error) {
	if read.Validate() != nil || validateReadQuery(read.id, raw) != nil {
		return Read{}, ErrUnsupported
	}
	read.rawQuery = ""
	if raw != "" {
		values, _ := url.ParseQuery(raw)
		read.rawQuery = values.Encode()
	}
	return read, nil
}

func validateReadQuery(operation, raw string) error {
	if raw == "" {
		return nil
	}
	if operation != CodexModels || len(raw) > 128 {
		return ErrUnsupported
	}
	values, err := url.ParseQuery(raw)
	versions, ok := values["client_version"]
	if err != nil || len(values) != 1 || !ok || len(versions) != 1 {
		return ErrUnsupported
	}
	parts := strings.Split(versions[0], ".")
	if len(parts) != 3 {
		return ErrUnsupported
	}
	for _, part := range parts {
		if len(part) == 0 || len(part) > 6 {
			return ErrUnsupported
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return ErrUnsupported
			}
		}
	}
	return nil
}

// Model fields belong to Codex, including fields unknown to ViberMate. Check
// only the bounded envelope here; the complete original bytes reach the client.
func projectCodexModels(body []byte) (Facts, error) {
	if len(body) == 0 || len(body) > MaxCodexModelsBodyBytes || !utf8.Valid(body) {
		return Facts{}, ErrInvalidResponse
	}
	var wire struct {
		Models []json.RawMessage `json:"models"`
	}
	if protocolcore.ValidateJSONNames(body) != nil || json.Unmarshal(body, &wire) != nil || wire.Models == nil || len(wire.Models) > 16<<10 {
		return Facts{}, ErrInvalidResponse
	}
	for _, entry := range wire.Models {
		var model map[string]json.RawMessage
		if json.Unmarshal(entry, &model) != nil || model == nil {
			return Facts{}, ErrInvalidResponse
		}
	}
	return Facts{Limits: []QuotaLimit{}}, nil
}
