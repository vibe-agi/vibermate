// Package productbuild exposes one bounded build label safe for diagnostics.
package productbuild

import (
	"runtime/debug"
	"strings"
	"unicode"
	"unicode/utf8"
)

const development = "development"

func Label() string {
	information, ok := debug.ReadBuildInfo()
	if !ok {
		return development
	}
	return label(information)
}

func label(information *debug.BuildInfo) string {
	if information == nil {
		return development
	}
	if information.Main.Version != "" &&
		information.Main.Version != "(devel)" &&
		Valid(information.Main.Version) {
		return information.Main.Version
	}
	revision := ""
	modified := false
	for _, setting := range information.Settings {
		switch setting.Key {
		case "vcs.revision":
			if Valid(setting.Value) {
				revision = setting.Value
			}
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision == "" {
		return development
	}
	if modified && len(revision)+len("+modified") <= 128 {
		return revision + "+modified"
	}
	return revision
}

func Valid(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
