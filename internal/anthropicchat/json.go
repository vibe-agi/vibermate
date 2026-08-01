package anthropicchat

import (
	"reflect"
	"sort"
	"strings"

	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"io"
)

func decodeStrict(value []byte, destination any) error {
	if err := rejectDuplicateJSONNames(value); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON value has trailing data")
		}
		return err
	}
	return nil
}

func rejectDuplicateJSONNames(value []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	if err := consumeUniqueJSONValue(decoder); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("JSON value has trailing data")
	}
	return nil
}

func consumeUniqueJSONValue(decoder *json.Decoder) error {
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
		names := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, duplicate := names[name]; duplicate {
				return fmt.Errorf("JSON object key %q is duplicated", name)
			}
			names[name] = struct{}{}
			if err := consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("JSON object is not terminated")
		}
	case '[':
		for decoder.More() {
			if err := consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("JSON array is not terminated")
		}
	default:
		return errors.New("JSON delimiter is invalid")
	}
	return nil
}

func rawPresent(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null"))
}

// decodeTolerant decodes a wire object, naming the fields it carried that this
// dialect does not model instead of refusing the whole request over them.
//
// Design 07 §2.3 requires the wire layer to keep unknown fields rather than let
// a typed unmarshal make them disappear. The IR cannot carry a field it has no
// concept of, so what survives is the fact that it was there: one declared
// notice per field, at its path.
func decodeTolerant(
	value []byte,
	destination any,
	path string,
) (protocolcore.TranslationReport, error) {
	if err := rejectDuplicateJSONNames(value); err != nil {
		return protocolcore.TranslationReport{}, err
	}
	var present map[string]json.RawMessage
	if err := json.Unmarshal(value, &present); err != nil {
		return protocolcore.TranslationReport{}, err
	}
	modelled := modelledFieldNames(destination)
	unknown := make([]string, 0, len(present))
	for name := range present {
		if _, known := modelled[name]; !known {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	notices := make([]protocolcore.TranslationNotice, 0, len(unknown))
	for _, name := range unknown {
		notices = append(notices, protocolcore.TranslationNotice{
			Code: protocolcore.NoticeUnknownRequestFieldNotForwarded,
			Path: path + "." + name,
		})
	}
	// Only this object is tolerant. Everything nested inside it is decoded
	// strictly, because an unknown field inside a tool definition or a content
	// block changes what that structure means, while an unknown field beside
	// them is a request-level option this dialect does not model.
	known := make(map[string]json.RawMessage, len(present))
	for name, raw := range present {
		if _, modelled := modelled[name]; modelled {
			known[name] = raw
		}
	}
	filtered, err := json.Marshal(known)
	if err != nil {
		return protocolcore.TranslationReport{}, err
	}
	if err := decodeStrict(filtered, destination); err != nil {
		return protocolcore.TranslationReport{}, err
	}
	return protocolcore.NewTranslationReport(notices...), nil
}

// modelledFieldNames is the set of wire names the destination declares.
func modelledFieldNames(destination any) map[string]struct{} {
	names := map[string]struct{}{}
	value := reflect.ValueOf(destination)
	for value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return names
	}
	structType := value.Type()
	for index := range structType.NumField() {
		tag := structType.Field(index).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name != "" {
			names[name] = struct{}{}
		}
	}
	return names
}
