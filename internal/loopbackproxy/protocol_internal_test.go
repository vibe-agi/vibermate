package loopbackproxy

import (
	"slices"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
)

func TestDownstreamNextProtosRequiresARealIntersection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		protocols []access.ApplicationProtocol
		offered   []string
		want      []string
		wantError bool
	}{
		{
			name: "client and Access support HTTP2 and HTTP1",
			protocols: []access.ApplicationProtocol{
				access.ApplicationProtocolHTTP2,
				access.ApplicationProtocolHTTP1,
			},
			offered: []string{"h2", "http/1.1"},
			want:    []string{"h2", "http/1.1"},
		},
		{
			name:      "Access narrows an offered HTTP2 connection to HTTP1",
			protocols: []access.ApplicationProtocol{access.ApplicationProtocolHTTP1},
			offered:   []string{"h2", "http/1.1"},
			want:      []string{"http/1.1"},
		},
		{
			name:      "HTTP2-only client cannot use an HTTP1-only Access",
			protocols: []access.ApplicationProtocol{access.ApplicationProtocolHTTP1},
			offered:   []string{"h2"},
			wantError: true,
		},
		{
			name:      "missing ALPN means HTTP1",
			protocols: []access.ApplicationProtocol{access.ApplicationProtocolHTTP1},
			want:      []string{"http/1.1"},
		},
		{
			name:      "missing ALPN cannot satisfy HTTP2",
			protocols: []access.ApplicationProtocol{access.ApplicationProtocolHTTP2},
			wantError: true,
		},
		{
			name: "duplicate authority output is rejected",
			protocols: []access.ApplicationProtocol{
				access.ApplicationProtocolHTTP1,
				access.ApplicationProtocolHTTP1,
			},
			offered:   []string{"http/1.1"},
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := downstreamNextProtos(test.protocols, test.offered)
			if test.wantError {
				if err == nil {
					t.Fatalf("downstreamNextProtos() = %v, want error", got)
				}
				return
			}
			if err != nil || !slices.Equal(got, test.want) {
				t.Fatalf(
					"downstreamNextProtos() = %v, %v; want %v",
					got,
					err,
					test.want,
				)
			}
		})
	}
}
