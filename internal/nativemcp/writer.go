package nativemcp

import (
	"encoding/json"
	"net/http"
	"strings"
)

type nativeWriter struct {
	http.ResponseWriter
	headers, sse, terminal, failure bool
	correlation                     []byte
	maximum                         int64
}

func (w *nativeWriter) WriteHeader(code int) {
	if w.headers {
		return
	}
	w.headers = true
	w.sse = strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream")
	w.ResponseWriter.WriteHeader(code)
}
func (w *nativeWriter) Write(body []byte) (int, error) {
	if !w.headers {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}
func (w *nativeWriter) Flush() {
	if !w.headers {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Once SSE headers exist HTTP status is immutable. Correlate only an actual
// outstanding request; a GET or completed terminal is closed without a fake ID.
func (w *nativeWriter) failAfterHeaders() bool {
	if !w.headers {
		return false
	}
	if !w.sse || w.terminal || w.failure || len(w.correlation) == 0 {
		return true
	}
	w.failure = true
	encoded, _ := json.Marshal(struct {
		Version string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Version: "2.0", ID: w.correlation, Error: struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}{-32004, "execution_uncertain"}})
	frame := append([]byte("event: message\ndata: "), encoded...)
	frame = append(frame, []byte("\n\n")...)
	if int64(len(frame)) <= w.maximum {
		_, _ = w.Write(frame)
		w.Flush()
	}
	return true
}
