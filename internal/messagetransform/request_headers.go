package messagetransform

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// RequestUserAgentOverride validates a completed request transform's UA edit.
// Both the sample runner and Exchange use this boundary. Only changed values
// override the wire profile; nil leaves its authority intact, while an empty
// override explicitly suppresses the default UA. Neither Header map is mutated.
func RequestUserAgentOverride(before, after http.Header) (*string, error) {
	values := userAgentValues(after)
	if slices.Equal(userAgentValues(before), values) {
		return nil, nil
	}
	if len(values) > 1 {
		return nil, fmt.Errorf("%w: request must produce at most one User-Agent", ErrInvalidOutput)
	}
	value := ""
	if len(values) == 1 {
		value = values[0]
	}
	if len(value) > 512 || strings.IndexFunc(value, func(character rune) bool {
		return character < 0x20 || character > 0x7e
	}) != -1 {
		return nil, fmt.Errorf("%w: request User-Agent is invalid", ErrInvalidOutput)
	}
	return &value, nil
}

func userAgentValues(headers http.Header) []string {
	var result []string
	for name, values := range headers {
		if strings.EqualFold(name, "User-Agent") {
			result = append(result, values...)
		}
	}
	return result
}
