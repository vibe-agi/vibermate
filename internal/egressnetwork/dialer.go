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

// Bound resolution and the complete proxy handshake, not only the first TCP
// socket to the proxy. This budget never owns an established Agent stream.
const connectionEstablishmentTimeout = 30 * time.Second

// VLESSConnector is trusted host composition; it only receives frozen public
// identity and admitted literal targets, never owner-supplied executable paths.
type VLESSConnector interface {
	DialContext(context.Context, ProxyPolicy, string, string) (net.Conn, error)
}

type BuilderOptions struct {
	BaseDialer      ContextDialer
	SystemResolver  Resolver
	TLSClientConfig *tls.Config
	VLESS           VLESSConnector
}

// Builder compiles one frozen Policy into a context-aware target dialer. Its
// dependencies are immutable and safe to share, while each returned dialer
// owns only the selected path and no credential material.
type Builder struct {
	base           ContextDialer
	systemResolver Resolver
	tlsConfig      *tls.Config
	vless          VLESSConnector
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
	return &Builder{base: base, systemResolver: resolver, tlsConfig: tlsConfig, vless: options.VLESS}, nil
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
	proxyDialer, err := builder.proxyDialer(normalized.Proxy)
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
			resolverDialer = proxyDialer
		}
		// Infrastructure bootstrap uses explicit Runtime system resolution,
		// independent of the target's configured DoH. The owner configured
		// this resolver endpoint; it does not grant private target authority.
		resolverDialer = bootstrapDialer{dialer: &policyDialer{policy: Policy{Proxy: ProxyPolicy{Kind: ProxySOCKS5}}, base: builder.base, proxy: resolverDialer, resolver: builder.systemResolver}}
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
	return &policyDialer{
		policy:   normalized,
		base:     builder.base,
		resolver: targetResolver,
		proxy:    proxyDialer,
		system:   builder.systemResolver,
	}, nil
}

type policyDialer struct {
	policy   Policy
	base     ContextDialer
	resolver Resolver
	proxy    ContextDialer
	system   Resolver
}

func (dialer *policyDialer) DialContext(
	ctx context.Context,
	network string,
	address string,
) (net.Conn, error) {
	if dialer == nil || ctx == nil || dialer.base == nil {
		return nil, errors.New("traffic egress dial request is incomplete")
	}
	ctx, cancel := context.WithTimeout(ctx, connectionEstablishmentTimeout)
	defer cancel()
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
	if dialer.policy.DirectDomains.Matches(host) {
		direct := &policyDialer{base: dialer.base, resolver: dialer.system}
		return direct.resolveAndDial(ctx, network, host, port, dialer.base)
	}
	switch dialer.policy.Proxy.Kind {
	case ProxyDirect:
		if dialer.policy.Resolver.Kind == ResolverSystem && !publicTargetsOnly(ctx) {
			return dialer.base.DialContext(ctx, network, canonical)
		}
		return dialer.resolveAndDial(ctx, network, host, port, dialer.base)
	case ProxySOCKS5, ProxyVLESS:
		if dialer.proxy == nil {
			return nil, errors.New("SOCKS5 target dialer is unavailable")
		}
		return dialer.resolveAndDial(ctx, network, host, port, dialer.proxy)
	default:
		return nil, ErrInvalidPolicy
	}
}

type vlessDialer struct {
	connector VLESSConnector
	policy    ProxyPolicy
}

type bootstrapDialer struct{ dialer *policyDialer }

func (d bootstrapDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.dialer.DialContext(WithTargetAccess(ctx, TargetAccessOwnerConfigured), network, address)
}

func (d vlessDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if d.connector == nil {
		return nil, errors.New("VLESS transport is unavailable")
	}
	return d.connector.DialContext(ctx, d.policy, network, address)
}
func (builder *Builder) proxyDialer(policy ProxyPolicy) (ContextDialer, error) {
	switch policy.Kind {
	case ProxyDirect:
		return nil, nil
	case ProxySOCKS5:
		return newSOCKSDialer(builder.base, policy.Endpoint)
	case ProxyVLESS:
		return vlessDialer{connector: builder.vless, policy: policy}, nil
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
		if publicTargetsOnly(ctx) && !publicAddress(address) {
			return nil, ErrPrivateTarget
		}
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
	// Validate the whole answer before dialing, then dial only these literal
	// addresses. A later DNS lookup cannot rebind an admitted name to the LAN.
	if publicTargetsOnly(ctx) {
		for _, address := range addresses {
			if !publicAddress(address) {
				return nil, ErrPrivateTarget
			}
		}
	}
	// Filter only after checking the complete DNS answer for private targets.
	// Incompatible families must not consume another candidate's dial budget.
	candidates := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if address.IsValid() && addressMatchesNetwork(address, network) {
			candidates = append(candidates, address)
		}
	}
	var failures []error
	for index, address := range candidates {
		// A stale address must not consume the whole operation deadline before
		// another answer for this same destination can be tried. All attempts
		// retain the selected proxy and the already validated DNS answer set.
		attempt := ctx
		cancel := func() {}
		if deadline, bounded := ctx.Deadline(); bounded {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				failures = append(failures, context.DeadlineExceeded)
				break
			}
			attempt, cancel = context.WithTimeout(ctx, remaining/time.Duration(len(candidates)-index))
		}
		connection, dialErr := target.DialContext(
			attempt,
			network,
			net.JoinHostPort(address.String(), port),
		)
		cancel()
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
