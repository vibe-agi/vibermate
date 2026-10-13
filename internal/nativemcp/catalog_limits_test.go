package nativemcp

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
)

func catalogReadyFixture(t *testing.T) *nativeFixture {
	t.Helper()
	f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
	for _, body := range []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, `{"jsonrpc":"2.0","method":"notifications/initialized"}`} {
		wire := "synthetic-session"
		if strings.Contains(body, `"method":"initialize"`) {
			wire = ""
		}
		terminal, _ := f.serve(t, body, wire)
		if terminal.Outcome != OutcomeCompleted {
			t.Fatal(terminal)
		}
	}
	return f
}
func TestNativeCatalogPagesToolsAndWireBytesExactBoundaries(t *testing.T) {
	for _, test := range []struct {
		name                      string
		pages, tools, total, page int
		valid                     bool
	}{{"pages32", 32, 32, 0, 0, true}, {"pages33", 33, 33, 0, 0, false}, {"tools1024", 1, 1024, 0, 0, true}, {"tools1025", 1, 1025, 0, 0, false}, {"page1MiB", 1, 0, 0, 1 << 20, true}, {"page1MiBplus1", 1, 0, 0, (1 << 20) + 1, false}, {"total8MiB", 9, 0, 8 << 20, 0, true}, {"total8MiBplus1", 9, 0, (8 << 20) + 1, 0, false}} {
		t.Run(test.name, func(t *testing.T) {
			f := catalogReadyFixture(t)
			var requests, responses []string
			for index := 0; index < test.pages; index++ {
				params := ""
				if index > 0 {
					params = fmt.Sprintf(`,"params":{"cursor":"cursor-%d"}`, index)
				}
				requests = append(requests, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/list"%s}`, 100+index, params))
				var definitions []string
				for tool := index; tool < test.tools; tool += test.pages {
					definitions = append(definitions, fmt.Sprintf(`{"name":"tool_%04d","inputSchema":{"type":"object"}}`, tool))
				}
				cursor := ""
				if index < test.pages-1 {
					cursor = fmt.Sprintf(`,"nextCursor":"cursor-%d"`, index+1)
				}
				responses = append(responses, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"tools":[%s]%s}}`, 100+index, strings.Join(definitions, ","), cursor))
			}
			if test.page > 0 {
				responses[0] += strings.Repeat(" ", test.page-len(responses[0]))
			}
			if test.total > 0 {
				remaining := test.total
				for index := range responses {
					remaining -= len(requests[index]) + len(responses[index])
				}
				for index := range responses {
					add := remaining / (len(responses) - index)
					responses[index] += strings.Repeat(" ", add)
					remaining -= add
				}
			}
			page := 0
			f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
				response := responses[page]
				page++
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
			}
			for _, request := range requests {
				terminal, _ := f.serve(t, request, "synthetic-session")
				if terminal.Outcome != OutcomeCompleted {
					if test.valid {
						t.Fatalf("valid catalog boundary %+v page%d", terminal, page)
					}
					break
				}
			}
			e := f.executor.(*executor)
			e.mu.Lock()
			hasCatalog := false
			for _, s := range e.scopes[f.capture.Key()].sessions {
				s.mu.Lock()
				hasCatalog = hasCatalog || s.catalog != nil
				s.mu.Unlock()
			}
			e.mu.Unlock()
			if hasCatalog != test.valid {
				t.Fatalf("catalog evidence at %s: %v", test.name, hasCatalog)
			}
		})
	}
}
