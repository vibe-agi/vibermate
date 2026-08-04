package releasemanifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

func Decode(reader io.Reader) (Manifest, error) {
	if reader == nil {
		return Manifest{}, invalid("document reader is nil")
	}
	payload, err := io.ReadAll(io.LimitReader(reader, MaxDocumentBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("read release manifest: %w", err)
	}
	return DecodeBytes(payload)
}

func DecodeBytes(payload []byte) (Manifest, error) {
	if len(payload) == 0 {
		return Manifest{}, invalid("document is empty")
	}
	if len(payload) > MaxDocumentBytes {
		return Manifest{}, invalid("document exceeds the %d-byte limit", MaxDocumentBytes)
	}
	if !utf8.Valid(payload) {
		return Manifest{}, invalid("document is not valid UTF-8")
	}
	if err := rejectDuplicateMembers(payload); err != nil {
		return Manifest{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, invalid("decode document: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Manifest{}, invalid("document contains trailing JSON")
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func Marshal(manifest Manifest) ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal release manifest: %w", err)
	}
	payload = append(payload, '\n')
	if len(payload) > MaxDocumentBytes {
		return nil, invalid("encoded document exceeds the %d-byte limit", MaxDocumentBytes)
	}
	return payload, nil
}

func rejectDuplicateMembers(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder); err != nil {
		return invalid("decode document: %v", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return invalid("document contains trailing JSON")
		}
		return invalid("decode document: %v", err)
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		members := make(map[string]string)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object member name is not a string")
			}
			folded := foldedMemberName(key)
			if previous, duplicate := members[folded]; duplicate {
				if previous == key {
					return fmt.Errorf("object contains duplicate member %q", key)
				}
				return fmt.Errorf(
					"object contains case-insensitive member aliases %q and %q",
					previous,
					key,
				)
			}
			members[folded] = key
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("object is not closed")
		}
		return nil
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("array is not closed")
		}
		return nil
	default:
		return fmt.Errorf("unexpected delimiter %q", delimiter)
	}
}

// foldedMemberName mirrors encoding/json's field-name folding. The standard
// decoder prefers an exact struct-field match, then accepts an EqualFold
// match. Rejecting aliases before decoding prevents two differently-cased JSON
// members from silently assigning the same Go field, including in nested
// objects.
func foldedMemberName(value string) string {
	var folded strings.Builder
	folded.Grow(len(value))
	for _, character := range value {
		if character >= 'a' && character <= 'z' {
			character -= 'a' - 'A'
		} else if character >= utf8.RuneSelf {
			for {
				next := unicode.SimpleFold(character)
				if next <= character {
					character = next
					break
				}
				character = next
			}
		}
		folded.WriteRune(character)
	}
	return folded.String()
}
