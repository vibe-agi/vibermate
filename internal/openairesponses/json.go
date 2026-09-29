package openairesponses

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func decodeStrict(value []byte, destination any) error {
	if err := rejectDuplicateNames(value); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("JSON value has trailing data")
	}
	return nil
}

// rejectDuplicateNames applies the shared boundary rule: no member may be
// named twice, exactly or under encoding/json's case folding.
func rejectDuplicateNames(value []byte) error {
	return protocolcore.ValidateJSONNames(value)
}

func rawPresent(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null"))
}

func peekType(value []byte) (string, error) {
	if err := rejectDuplicateNames(value); err != nil {
		return "", err
	}
	var wire struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(value, &wire); err != nil {
		return "", err
	}
	if wire.Type == "" {
		return "", errors.New("JSON object type is missing")
	}
	return wire.Type, nil
}
