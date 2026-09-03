package serverhost

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRouterBoundsOrdinaryRequestsButLeavesProxyStreamsLongLived(t *testing.T) {
	t.Parallel()
	application := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	handler := router{
		scheme:       "http",
		managementUI: application,
		proxy:        application,
	}

	ordinary := &deadlineResponseWriter{ResponseRecorder: httptest.NewRecorder()}
	handler.ServeHTTP(
		ordinary,
		httptest.NewRequest(http.MethodGet, "/", nil),
	)
	if len(ordinary.readDeadlines) != 2 || ordinary.readDeadlines[0].IsZero() ||
		!ordinary.readDeadlines[1].IsZero() {
		t.Fatalf("ordinary read deadlines = %v", ordinary.readDeadlines)
	}
	if len(ordinary.writeDeadlines) != 2 || ordinary.writeDeadlines[0].IsZero() ||
		!ordinary.writeDeadlines[1].IsZero() {
		t.Fatalf("ordinary write deadlines = %v", ordinary.writeDeadlines)
	}

	proxy := &deadlineResponseWriter{ResponseRecorder: httptest.NewRecorder()}
	request := httptest.NewRequest(http.MethodConnect, "http://provider.test:443", nil)
	handler.ServeHTTP(proxy, request)
	if len(proxy.readDeadlines) != 0 || len(proxy.writeDeadlines) != 0 {
		t.Fatalf(
			"proxy deadlines read=%v write=%v",
			proxy.readDeadlines,
			proxy.writeDeadlines,
		)
	}
}

type deadlineResponseWriter struct {
	*httptest.ResponseRecorder
	readDeadlines  []time.Time
	writeDeadlines []time.Time
}

func (writer *deadlineResponseWriter) SetReadDeadline(deadline time.Time) error {
	writer.readDeadlines = append(writer.readDeadlines, deadline)
	return nil
}

func (writer *deadlineResponseWriter) SetWriteDeadline(deadline time.Time) error {
	writer.writeDeadlines = append(writer.writeDeadlines, deadline)
	return nil
}
