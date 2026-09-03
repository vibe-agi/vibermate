package egressnetwork_test

import (
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
)

func TestPolicyNormalizesOnlyExecutableNetworkPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      egressnetwork.Policy
		want       egressnetwork.Policy
		wantReject bool
	}{
		{
			name:  "zero value is explicit direct system DNS",
			input: egressnetwork.Policy{},
			want:  egressnetwork.DefaultPolicy(),
		},
		{
			name: "SOCKS5 resolves the AI target with system DNS",
			input: egressnetwork.Policy{
				Proxy: egressnetwork.ProxyPolicy{
					Kind:     egressnetwork.ProxySOCKS5,
					Endpoint: "PROXY.Example.:1080",
				},
				Resolver: egressnetwork.ResolverPolicy{
					Kind: egressnetwork.ResolverSystem,
				},
			},
			want: egressnetwork.Policy{
				Proxy: egressnetwork.ProxyPolicy{
					Kind:     egressnetwork.ProxySOCKS5,
					Endpoint: "proxy.example:1080",
				},
				Resolver: egressnetwork.ResolverPolicy{
					Kind:      egressnetwork.ResolverSystem,
					Transport: egressnetwork.ResolverTransportDirect,
				},
			},
		},
		{
			name: "DoH can travel through the configured SOCKS5 proxy",
			input: egressnetwork.Policy{
				Proxy: egressnetwork.ProxyPolicy{
					Kind:     egressnetwork.ProxySOCKS5,
					Endpoint: "127.0.0.1:1080",
				},
				Resolver: egressnetwork.ResolverPolicy{
					Kind:      egressnetwork.ResolverDoH,
					DoHURL:    "https://DNS.Example.:443/dns-query",
					Transport: egressnetwork.ResolverTransportProxy,
				},
			},
			want: egressnetwork.Policy{
				Proxy: egressnetwork.ProxyPolicy{
					Kind:     egressnetwork.ProxySOCKS5,
					Endpoint: "127.0.0.1:1080",
				},
				Resolver: egressnetwork.ResolverPolicy{
					Kind:      egressnetwork.ResolverDoH,
					DoHURL:    "https://dns.example/dns-query",
					Transport: egressnetwork.ResolverTransportProxy,
				},
			},
		},
		{
			name: "SOCKS5H is not an executable network path",
			input: egressnetwork.Policy{
				Proxy: egressnetwork.ProxyPolicy{
					Kind:     egressnetwork.ProxyKind("socks5h"),
					Endpoint: "[2001:db8::1]:1080",
				},
				Resolver: egressnetwork.ResolverPolicy{
					Kind: egressnetwork.ResolverKind("proxy"),
				},
			},
			wantReject: true,
		},
		{
			name: "removed SOCKS5H cannot silently ignore a DoH target resolver",
			input: egressnetwork.Policy{
				Proxy: egressnetwork.ProxyPolicy{
					Kind:     egressnetwork.ProxyKind("socks5h"),
					Endpoint: "proxy.example:1080",
				},
				Resolver: egressnetwork.ResolverPolicy{
					Kind:   egressnetwork.ResolverDoH,
					DoHURL: "https://dns.example/dns-query",
				},
			},
			wantReject: true,
		},
		{
			name: "direct traffic cannot claim proxy DNS",
			input: egressnetwork.Policy{
				Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxyDirect},
				Resolver: egressnetwork.ResolverPolicy{
					Kind: egressnetwork.ResolverKind("proxy"),
				},
			},
			wantReject: true,
		},
		{
			name: "DoH through proxy requires an actual proxy",
			input: egressnetwork.Policy{
				Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxyDirect},
				Resolver: egressnetwork.ResolverPolicy{
					Kind:      egressnetwork.ResolverDoH,
					DoHURL:    "https://dns.example/dns-query",
					Transport: egressnetwork.ResolverTransportProxy,
				},
			},
			wantReject: true,
		},
		{
			name: "DoH endpoint never accepts plaintext or credentials",
			input: egressnetwork.Policy{
				Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxyDirect},
				Resolver: egressnetwork.ResolverPolicy{
					Kind:   egressnetwork.ResolverDoH,
					DoHURL: "http://user:password@dns.example/dns-query",
				},
			},
			wantReject: true,
		},
		{
			name: "DoH endpoint requires its configured HTTP path",
			input: egressnetwork.Policy{
				Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxyDirect},
				Resolver: egressnetwork.ResolverPolicy{
					Kind:   egressnetwork.ResolverDoH,
					DoHURL: "https://8.8.8.8",
				},
			},
			wantReject: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			normalized, err := test.input.Normalize()
			if test.wantReject {
				if err == nil {
					t.Fatalf("Normalize() unexpectedly accepted %#v", normalized)
				}
				return
			}
			if err != nil {
				t.Fatalf("Normalize(): %v", err)
			}
			if normalized != test.want {
				t.Fatalf("Normalize() = %#v, want %#v", normalized, test.want)
			}
			if err := normalized.Validate(); err != nil {
				t.Fatalf("normalized policy is invalid: %v", err)
			}
		})
	}
}
