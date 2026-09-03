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
