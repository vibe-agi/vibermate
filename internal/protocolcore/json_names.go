package protocolcore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode"
	"unicode/utf8"
)

// ValidateJSONNames rejects a JSON document in which one object names the same
// member twice, either exactly or under encoding/json's case folding.
//
// Clients parse provider and request JSON with exact member names, while
// encoding/json binds struct fields case-insensitively and lets the last
// matching member win. If both "type" and "TYPE" were accepted, ViberMate could
// audit, approve, or route one value while the client acts on another. Every
// JSON document that crosses a protocol boundary is checked once, before any
// decoding, so later case-insensitive decoding can only see one candidate per
// field. The document must also be a single value without trailing data.
func ValidateJSONNames(document []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	if err := consumeJSONValueNames(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("JSON value has trailing data")
	}
	return nil
}

func consumeJSONValueNames(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		names := make(map[string]string)
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			folded := foldJSONName(name)
			if previous, duplicate := names[folded]; duplicate {
				if previous == name {
					return fmt.Errorf("JSON object key %q is duplicated", name)
				}
				return fmt.Errorf("JSON object keys %q and %q differ only by case", previous, name)
			}
			names[folded] = name
			if err := consumeJSONValueNames(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValueNames(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("JSON value is malformed")
	}
	// Token validates that the closing delimiter matches the opening one.
	_, err = decoder.Token()
	return err
}

// foldJSONName mirrors encoding/json's field-name folding: two names fold to
// the same string exactly when encoding/json would bind them to one field.
func foldJSONName(name string) string {
	folded := make([]byte, 0, len(name))
	for index := 0; index < len(name); {
		if character := name[index]; character < utf8.RuneSelf {
			if 'a' <= character && character <= 'z' {
				character -= 'a' - 'A'
			}
			folded = append(folded, character)
			index++
			continue
		}
		value, size := utf8.DecodeRuneInString(name[index:])
		folded = utf8.AppendRune(folded, smallestFoldRune(value))
		index += size
	}
	return string(folded)
}

func smallestFoldRune(value rune) rune {
	for {
		next := unicode.SimpleFold(value)
		if next <= value {
			return next
		}
		value = next
	}
}

// SameJSONName reports whether encoding/json would bind both member names to
// the same struct field.
func SameJSONName(left, right string) bool {
	return foldJSONName(left) == foldJSONName(right)
}
