package servercontrol

import (
	"encoding/json"
	"net/http"
	"strings"
)

func writeProblem(writer http.ResponseWriter, status int, code string) {
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"type":   "urn:vibermate:error:" + strings.ReplaceAll(code, "_", "-"),
		"title":  http.StatusText(status),
		"status": status,
		"code":   code,
	})
}

// writeDetailedProblem adds machine-readable fields that let the UI point at
// what to fix, such as the index of an invalid entry.
func writeDetailedProblem(writer http.ResponseWriter, status int, code string, details map[string]any) {
	body := map[string]any{
		"type":   "urn:vibermate:error:" + strings.ReplaceAll(code, "_", "-"),
		"title":  http.StatusText(status),
		"status": status,
		"code":   code,
	}
	for name, value := range details {
		if _, reserved := body[name]; !reserved {
			body[name] = value
		}
	}
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}
