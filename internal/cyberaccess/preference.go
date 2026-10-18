package cyberaccess

import "errors"

// Preference is the account's authoritative mode, independent of the client
// login. The zero value deliberately selects Standard.
type Preference string

const (
	Standard Preference = ""
	Blue     Preference = "daybreak_blue"
	Red      Preference = "daybreak_red"
)

var ErrInvalidPreference = errors.New("cyber access mode is invalid")
var ErrProgramUnavailable = errors.New("the selected cyber access mode is unavailable for this account and model")

func (value Preference) Valid() bool { return value == Standard || value == Blue || value == Red }

func (value Preference) WireValue() string {
	if value == Standard {
		return "standard"
	}
	return string(value)
}

func ParsePreference(value string) (Preference, error) {
	switch value {
	case "standard":
		return Standard, nil
	case "daybreak_blue":
		return Blue, nil
	case "daybreak_red":
		return Red, nil
	default:
		return Standard, ErrInvalidPreference
	}
}
