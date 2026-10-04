package nativemcp

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNativeApprovalErrorFramingNeedsCorrelationAndHonorsBound(t *testing.T) {
	for _, reason := range []Reason{ReasonApprovalRejected, ReasonApprovalExpired} {
		recorder := httptest.NewRecorder()
		writer := &nativeWriter{ResponseWriter: recorder, correlation: []byte(`"typed-string-id"`), maximum: 512}
		reject(writer, "operation", reason)
		if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), `"id":"typed-string-id"`) || !strings.Contains(recorder.Body.String(), `"code":-32003`) {
			t.Fatalf("correlated approval %d %s", recorder.Code, recorder.Body.String())
		}
		recorder = httptest.NewRecorder()
		writer = &nativeWriter{ResponseWriter: recorder, maximum: 512}
		reject(writer, "operation", reason)
		if recorder.Code == 200 || strings.Contains(recorder.Body.String(), `"id"`) {
			t.Fatal("invented correlation without parsed RPC")
		}
		recorder = httptest.NewRecorder()
		writer = &nativeWriter{ResponseWriter: recorder, correlation: []byte(`"typed-string-id"`), maximum: 8}
		reject(writer, "operation", reason)
		if recorder.Body.Len() > 8 || strings.Contains(recorder.Body.String(), `"id"`) || strings.Contains(recorder.Body.String(), `-32003`) {
			t.Fatal("approval error exceeded event bound")
		}
		recorder = httptest.NewRecorder()
		writer = &nativeWriter{ResponseWriter: recorder, correlation: []byte(`3`), maximum: 512, terminal: true}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(200)
		reject(writer, "operation", reason)
		if recorder.Body.Len() != 0 {
			t.Fatal("second terminal after completed SSE")
		}
	}
}
