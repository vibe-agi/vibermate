package egressnetwork

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"

	netproxy "golang.org/x/net/proxy"
)

type ContextDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type BuilderOptions struct {
	BaseDialer      ContextDialer
	SystemResolver  Resolver
	TLSClientConfig *tls.Config
}

// Builder compiles one frozen Policy into a context-aware target dialer. Its
// dependencies are immutable and safe to share, while each returned dialer
// owns only the selected path and no credential material.
type Builder struct {
	base           ContextDialer
	systemResolver Resolver
	tlsConfig      *tls.Config
}

func NewBuilder(options BuilderOptions) (*Builder, error) {
	base := options.BaseDialer
	if base == nil {
		base = &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	}
	resolver := options.SystemResolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	tlsConfig := options.TLSClientConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		tlsConfig = tlsConfig.Clone()
		if tlsConfig.MinVersion == 0 {
			tlsConfig.MinVersion = tls.VersionTLS12
		}
	}
	return &Builder{base: base, systemResolver: resolver, tlsConfig: tlsConfig}, nil
}

func (builder *Builder) Dialer(policy Policy) (ContextDialer, error) {
	if builder == nil || builder.base == nil || builder.systemResolver == nil ||
		builder.tlsConfig == nil {
		return nil, errors.New("traffic egress dialer builder is unavailable")
	}
	normalized, err := policy.Normalize()
	if err != nil {
		return nil, err
	}
	var targetResolver Resolver
	switch normalized.Resolver.Kind {
	case ResolverSystem:
		targetResolver = builder.systemResolver
	case ResolverDoH:
		resolverDialer := builder.base
		if normalized.Resolver.Transport == ResolverTransportProxy {
			resolverDialer, err = newSOCKSDialer(
				builder.base,
				normalized.Proxy.Endpoint,
			)
			if err != nil {
				return nil, fmt.Errorf("build proxied DoH transport: %w", err)
			}
		}
		targetResolver, err = newDoHResolver(
			normalized.Resolver.DoHURL,
			resolverDialer,
			builder.tlsConfig,
		)
		if err != nil {
			return nil, err
		}
	default:
		return nil, ErrInvalidPolicy
	}
	var proxyDialer ContextDialer
	if normalized.Proxy.Kind != ProxyDirect {
		proxyDialer, err = newSOCKSDialer(builder.base, normalized.Proxy.Endpoint)
		if err != nil {
			return nil, err
		}
	}
	return &policyDialer{
		policy:   normalized,
		base:     builder.base,
		resolver: targetResolver,
		proxy:    proxyDialer,
	}, nil
}

type policyDialer struct {
	policy   Policy
	base     ContextDialer
	resolver Resolver
	proxy    ContextDialer
}

func (dialer *policyDialer) DialContext(
	ctx context.Context,
	network string,
	address string,
) (net.Conn, error) {
	if dialer == nil || ctx == nil || dialer.base == nil {
		return nil, errors.New("traffic egress dial request is incomplete")
	}
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, errors.New("traffic egress supports only TCP")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port == "" {
		return nil, errors.New("traffic egress target authority is invalid")
	}
	parsedPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsedPort == 0 {
		return nil, errors.New("traffic egress target port is invalid")
	}
	host, err = normalizeHost(host)
	if err != nil {
		return nil, fmt.Errorf("traffic egress target host: %w", err)
	}
	canonical := net.JoinHostPort(host, port)
	switch dialer.policy.Proxy.Kind {
	case ProxyDirect:
		if dialer.policy.Resolver.Kind == ResolverSystem {
			return dialer.base.DialContext(ctx, network, canonical)
		}
		return dialer.resolveAndDial(ctx, network, host, port, dialer.base)
	case ProxySOCKS5:
		if dialer.proxy == nil {
			return nil, errors.New("SOCKS5 target dialer is unavailable")
		}
		return dialer.resolveAndDial(ctx, network, host, port, dialer.proxy)
	default:
		return nil, ErrInvalidPolicy
	}
}

func (dialer *policyDialer) resolveAndDial(
	ctx context.Context,
	network string,
	host string,
	port string,
	target ContextDialer,
) (net.Conn, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		if !addressMatchesNetwork(address, network) {
			return nil, errors.New("traffic egress target address family is incompatible")
		}
		return target.DialContext(ctx, network, net.JoinHostPort(address.String(), port))
	}
	if dialer.resolver == nil {
		return nil, errors.New("traffic egress target resolver is unavailable")
	}
	addresses, err := dialer.resolver.LookupNetIP(ctx, lookupNetwork(network), host)
	if err != nil {
		return nil, fmt.Errorf("resolve traffic egress target: %w", err)
	}
	var failures []error
	for _, address := range addresses {
		address = address.Unmap()
		if !address.IsValid() || !addressMatchesNetwork(address, network) {
			continue
		}
		connection, dialErr := target.DialContext(
			ctx,
			network,
			net.JoinHostPort(address.String(), port),
		)
		if dialErr == nil {
			return connection, nil
		}
		failures = append(failures, dialErr)
		if ctx.Err() != nil {
			break
		}
	}
	if len(failures) == 0 {
		return nil, errors.New("traffic egress resolver returned no compatible address")
	}
	return nil, fmt.Errorf("dial resolved traffic egress target: %w", errors.Join(failures...))
}

func lookupNetwork(network string) string {
	switch network {
	case "tcp4":
		return "ip4"
	case "tcp6":
		return "ip6"
	default:
		return "ip"
	}
}

func addressMatchesNetwork(address netip.Addr, network string) bool {
	switch network {
	case "tcp4":
		return address.Is4()
	case "tcp6":
		return address.Is6()
	default:
		return address.Is4() || address.Is6()
	}
}

type forwardDialer struct{ ContextDialer }

func (dialer forwardDialer) Dial(network, address string) (net.Conn, error) {
	return dialer.DialContext(context.Background(), network, address)
}

func newSOCKSDialer(
	base ContextDialer,
	endpoint string,
) (ContextDialer, error) {
	if base == nil {
		return nil, errors.New("SOCKS forward dialer is unavailable")
	}
	proxyDialer, err := netproxy.SOCKS5(
		"tcp",
		endpoint,
		nil,
		forwardDialer{ContextDialer: base},
	)
	if err != nil {
		return nil, fmt.Errorf("build SOCKS5 dialer: %w", err)
	}
	contextual, ok := proxyDialer.(netproxy.ContextDialer)
	if !ok {
		return nil, errors.New("SOCKS5 dialer does not preserve cancellation")
	}
	return contextual, nil
}
