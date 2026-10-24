// Package hopbyhop removes connection-specific header fields before a message
// is forwarded.
package hopbyhop

import (
	"net/http"
	"strings"
)

// RemoveConnectionNamed deletes every field named by the Connection header.
// Connection may appear on several lines (RFC 9110 §7.6.1), so every line is
// read, not just the first. The Connection field itself and the fixed
// hop-by-hop names stay the caller's choice.
func RemoveConnectionNamed(header http.Header) {
	for _, line := range header.Values("Connection") {
		for _, token := range strings.Split(line, ",") {
			if name := strings.TrimSpace(token); name != "" {
				header.Del(name)
			}
		}
	}
}
