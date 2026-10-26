package processlineage

import (
	"bytes"
	"encoding/binary"
	"errors"
)

var errMalformedArguments = errors.New("process argument area is malformed")

// procargsEnvironment extracts the environment from a macOS kern.procargs2
// buffer. It is platform-neutral so the parser is tested everywhere.
func procargsEnvironment(data []byte) ([]string, error) {
	if len(data) < 4 {
		return nil, errMalformedArguments
	}
	argc := int(int32(binary.LittleEndian.Uint32(data[:4])))
	rest := data[4:]
	// The executable path, then NUL padding up to the first argument.
	end := bytes.IndexByte(rest, 0)
	if argc < 0 || end < 0 {
		return nil, errMalformedArguments
	}
	rest = rest[end:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	for range argc {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			return nil, errMalformedArguments
		}
		rest = rest[end+1:]
	}
	var environment []string
	for len(rest) > 0 {
		end := bytes.IndexByte(rest, 0)
		if end <= 0 {
			break // An empty string ends the environment.
		}
		environment = append(environment, string(rest[:end]))
		rest = rest[end+1:]
	}
	return environment, nil
}

func splitNUL(data []byte) []string {
	var entries []string
	for _, entry := range bytes.Split(data, []byte{0}) {
		if len(entry) != 0 {
			entries = append(entries, string(entry))
		}
	}
	return entries
}
