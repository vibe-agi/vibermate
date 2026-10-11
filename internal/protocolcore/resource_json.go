package protocolcore

import (
	"encoding/json"
	"errors"
	"math"
	"unsafe"
)

// JSON lexical costs reserve one maximum semantic cell per value/name (even
// empty strings), plus two string headers and a RawMessage per object member.
// This conservative preparation allowance bounds typed wire decoding, neutral
// node construction and notice/path cells before those allocations. It is not
// a production capacity or an assertion about Go map buckets/RSS.
const jsonValueCellBytes = uint64(unsafe.Sizeof(ContentBlock{}))
const jsonMemberCellBytes = uint64(2*unsafe.Sizeof("") + unsafe.Sizeof(json.RawMessage{}))

// scanJSONResources uses constant lexical state and no decoded strings, maps
// or recursive representation. A finite-budget scan stops as soon as the next
// lexical occurrence cannot be reserved. Actual grammar is then checked by
// encoding/json, retaining its depth, escape, surrogate and UTF-8 behavior.
func scanJSONResources(document []byte, budget *ResourceBudget) (ResourceCost, error) {
	var meter resourceMeter
	add := func(payload, structure uint64) error {
		meter.add(payload, structure)
		if meter.err != nil {
			return meter.err
		}
		if budget != nil {
			return budget.Reserve(ResourceCost{payload, structure})
		}
		return nil
	}
	if err := add(uint64(len(document)), 0); err != nil {
		return ResourceCost{}, err
	}
	for index := 0; index < len(document); {
		switch document[index] {
		case ' ', '\t', '\r', '\n', ',', ']', '}':
			index++
		case ':':
			if err := add(0, jsonMemberCellBytes); err != nil {
				return ResourceCost{}, err
			}
			index++
		case '[', '{':
			if err := add(0, jsonValueCellBytes); err != nil {
				return ResourceCost{}, err
			}
			index++
		case '"':
			start := index + 1
			index++
			for index < len(document) && document[index] != '"' {
				if document[index] == '\\' {
					index++
				}
				index++
			}
			if index >= len(document) {
				return ResourceCost{}, errors.New("JSON string is unterminated")
			}
			// Invalid UTF-8 can expand to replacement runes under json.Unmarshal.
			// Two owned decoded/folded occurrences each need at most 3x raw bytes.
			length := uint64(index - start)
			if length > math.MaxUint64/6 {
				return ResourceCost{}, errors.New("JSON resource accounting overflow")
			}
			if err := add(length*6, jsonValueCellBytes); err != nil {
				return ResourceCost{}, err
			}
			index++
		default:
			if err := add(0, jsonValueCellBytes); err != nil {
				return ResourceCost{}, err
			}
			for index < len(document) && document[index] != ',' && document[index] != ']' && document[index] != '}' && document[index] != ':' && document[index] != '"' && document[index] != '[' && document[index] != '{' && document[index] != ' ' && document[index] != '\t' && document[index] != '\r' && document[index] != '\n' {
				index++
			}
		}
	}
	if !json.Valid(document) {
		return ResourceCost{}, errors.New("JSON value is malformed")
	}
	return meter.cost, nil
}

func MeasureJSON(document []byte) (ResourceCost, error) { return scanJSONResources(document, nil) }

// ReserveJSON reserves the lexical construction allowance atomically. Caller
// remains responsible for the existing duplicate-name/semantic authority.
func (budget *ResourceBudget) ReserveJSON(document []byte) error {
	if budget == nil {
		return nil
	}
	trial := *budget
	if _, err := scanJSONResources(document, &trial); err != nil {
		return err
	}
	*budget = trial
	return nil
}

// ValidateJSONWithin performs resource preflight before the existing semantic
// names validator allocates decoded tokens and duplicate-name maps.
func ValidateJSONWithin(document []byte, limit ResourceCost) error {
	budget, err := NewResourceBudget(limit)
	if err != nil {
		return err
	}
	if _, err := scanJSONResources(document, budget); err != nil {
		return err
	}
	return ValidateJSONNames(document)
}
