package messagetransform

import (
	"errors"
	"strconv"

	"github.com/dop251/goja"
)

// Inspect UTF-16 before requesting the owning Go UTF-8 conversion. Literal
// U+FFFD remains valid; lone surrogate replacement remains rejected.
func scriptStringWithin(value goja.Value, maximum int) (string, error) {
	s, ok := value.(goja.String)
	if !ok || maximum < 0 || s.Length() > maximum {
		return "", errors.New("script string exceeds its byte limit")
	}
	n := 0
	for i := 0; i < s.Length(); i++ {
		u := s.CharAt(i)
		switch {
		case u < 0x80:
			n++
		case u < 0x800:
			n += 2
		case u >= 0xd800 && u <= 0xdbff:
			i++
			if i >= s.Length() {
				return "", errors.New("script string contains a lone surrogate")
			}
			low := s.CharAt(i)
			if low < 0xdc00 || low > 0xdfff {
				return "", errors.New("script string contains a lone surrogate")
			}
			n += 4
		case u >= 0xdc00 && u <= 0xdfff:
			return "", errors.New("script string contains a lone surrogate")
		default:
			n += 3
		}
		if n > maximum {
			return "", errors.New("script string exceeds its byte limit")
		}
	}
	return value.String(), nil
}

// Only the admitted key prefix is converted to Go strings. Goja's internal
// enumeration/trap snapshots remain VM scratch, not a bounded Go result slice.
// Production supplies the intrinsic captured before the operator function;
// all execution, traps and property reads stay inside its existing deadline.
func scriptKeysWithin(runtime *goja.Runtime, object *goja.Object, maximum int, remaining *int, functions ...goja.Callable) ([]string, error) {
	var keys goja.Callable
	if len(functions) > 0 {
		keys = functions[0]
	} else {
		keys, _ = goja.AssertFunction(runtime.Get("Object").ToObject(runtime).Get("keys"))
	}
	if keys == nil {
		return nil, errors.New("object enumeration unavailable")
	}
	value, err := keys(goja.Undefined(), object)
	if err != nil {
		return nil, err
	}
	array := value.ToObject(runtime)
	length := array.Get("length").ToInteger()
	if length < 0 || length > int64(maximum) {
		return nil, errors.New("object field count exceeds limit")
	}
	result := make([]string, int(length))
	for i := range result {
		text, err := scriptStringWithin(array.Get(strconv.Itoa(i)), *remaining)
		if err != nil {
			return nil, err
		}
		*remaining -= len(text)
		result[i] = text
	}
	return result, nil
}
