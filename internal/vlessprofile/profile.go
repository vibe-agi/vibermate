// Package vlessprofile owns one-time private imports and immutable core sessions.
package vlessprofile

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/vlesscore"
)

var ErrInvalidURI = errors.New("VLESS import requires raw/TCP, TLS or REALITY, and none encryption")
var ErrUnavailable = errors.New("VLESS private credential or transport is unavailable")

type ImportCommand struct {
	ID               egressprofile.ID
	ExpectedRevision egressprofile.Revision
	DisplayName      string
	URI              string                `json:"-"`
	TLS              *vlesscore.TLSOptions `json:"-"`
	Resolver         egressnetwork.ResolverPolicy
	DirectDomains    egressnetwork.DirectDomains
}

func (ImportCommand) String() string   { return "vlessprofile.ImportCommand{redacted}" }
func (ImportCommand) GoString() string { return "vlessprofile.ImportCommand{redacted}" }

type Importer interface {
	Import(context.Context, ImportCommand) (egressprofile.ProfileRevision, error)
	Describe(context.Context, egressprofile.ID, egressprofile.Revision) (Description, error)
}

// Private payload deliberately differs from vlesscore.Config (whose JSON is
// redacted). No executable, argv, filesystem CA or general engine JSON is stored.
type payload struct {
	Security    vlesscore.Security `json:"security,omitempty"`
	TLS         *storedTLS         `json:"tls,omitempty"`
	Host        string             `json:"host"`
	Port        uint16             `json:"port"`
	UUID        string             `json:"uuid"`
	SNI         string             `json:"sni"`
	Key         string             `json:"key"`
	ShortID     string             `json:"shortId"`
	Fingerprint string             `json:"fingerprint"`
	Flow        string             `json:"flow"`
}

type Options struct {
	Store          secretstore.Store
	Profiles       egressprofile.Controller
	Owner          context.Context
	Executable     string
	SystemResolver egressnetwork.Resolver
}
type session struct {
	ready  chan struct{}
	client *vlesscore.Client
	err    error
	cancel context.CancelFunc
}
type Manager struct {
	store        secretstore.Store
	profiles     egressprofile.Controller
	owner        context.Context
	cancelOwner  context.CancelFunc
	shutdownOnce sync.Once
	drained      chan struct{}
	executable   string
	resolver     egressnetwork.Resolver
	mu           sync.Mutex
	closed       bool
	sessions     map[egressnetwork.ProxyPolicy]*session
}

func (*Manager) String() string   { return "vlessprofile.Manager{redacted}" }
func (*Manager) GoString() string { return "vlessprofile.Manager{redacted}" }
func New(options Options) (*Manager, error) {
	if options.Store == nil || options.Profiles == nil || options.Owner == nil {
		return nil, ErrUnavailable
	}
	if options.SystemResolver == nil {
		options.SystemResolver = net.DefaultResolver
	}
	owner, cancelOwner := context.WithCancel(options.Owner)
	return &Manager{store: options.Store, profiles: options.Profiles, owner: owner, cancelOwner: cancelOwner, drained: make(chan struct{}), executable: options.Executable, resolver: options.SystemResolver, sessions: make(map[egressnetwork.ProxyPolicy]*session)}, nil
}

func parseURI(raw string) (payload, error) {
	invalid := func() (payload, error) { return payload{}, ErrInvalidURI }
	if len(raw) == 0 || len(raw) > 16384 || strings.ContainsAny(raw, "\r\n\x00") {
		return invalid()
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "vless" || u.User == nil || u.Opaque != "" || u.Path != "" || u.Host == "" {
		return invalid()
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		return invalid()
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return invalid()
	}
	for name, values := range q {
		if len(values) != 1 {
			return invalid()
		}
		switch name {
		case "type", "security", "encryption", "flow", "sni", "serverName", "pbk", "publicKey", "password", "sid", "shortId", "fp", "fingerprint", "spx":
		default:
			return invalid()
		}
	}
	security := vlesscore.Security(q.Get("security"))
	if (q.Get("type") != "" && q.Get("type") != "tcp" && q.Get("type") != "raw") || (security != vlesscore.SecurityREALITY && security != vlesscore.SecurityTLS) || q.Get("encryption") != "none" {
		return invalid()
	}
	if security == vlesscore.SecurityTLS {
		for _, name := range []string{"pbk", "publicKey", "password", "sid", "shortId", "spx"} {
			if _, supplied := q[name]; supplied {
				return invalid()
			}
		}
	}
	alias := func(names ...string) (string, bool) {
		value := ""
		for _, name := range names {
			if next := q.Get(name); next != "" {
				if value != "" && next != value {
					return "", false
				}
				value = next
			}
		}
		return value, true
	}
	key, ok := alias("pbk", "publicKey", "password")
	if !ok {
		return invalid()
	}
	sni, ok := alias("sni", "serverName")
	if !ok {
		return invalid()
	}
	sid, ok := alias("sid", "shortId")
	if !ok {
		return invalid()
	}
	fp, ok := alias("fp", "fingerprint")
	if !ok {
		return invalid()
	}
	host, err := egressnetwork.CanonicalHost(u.Hostname())
	if err != nil {
		return invalid()
	}
	if security == vlesscore.SecurityTLS && sni == "" {
		sni = host
	}
	sni, err = egressnetwork.CanonicalHost(sni)
	if err != nil {
		return invalid()
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil || port == 0 {
		return invalid()
	}
	uuid := strings.ToLower(u.User.Username())
	if len(uuid) != 36 || uuid[8] != '-' || uuid[13] != '-' || uuid[18] != '-' || uuid[23] != '-' {
		return invalid()
	}
	if b, err := hex.DecodeString(strings.ReplaceAll(uuid, "-", "")); err != nil || len(b) != 16 {
		return invalid()
	}
	if security == vlesscore.SecurityREALITY {
		if b, err := base64.RawURLEncoding.DecodeString(key); err != nil || len(b) != 32 {
			return invalid()
		}
	}
	if b, err := hex.DecodeString(sid); err != nil || len(b) > 8 {
		return invalid()
	}
	if fp == "" {
		fp = "chrome"
	}
	switch fp {
	case "chrome", "firefox", "safari", "ios", "android", "edge", "360", "qq", "random", "randomized":
	default:
		return invalid()
	}
	flow := q.Get("flow")
	if flow == "" && security == vlesscore.SecurityREALITY {
		flow = "xtls-rprx-vision"
	}
	if flow != "" && flow != "xtls-rprx-vision" && flow != "xtls-rprx-vision-udp443" {
		return invalid()
	}
	return payload{Security: security, Host: host, Port: uint16(port), UUID: uuid, SNI: sni, Key: key, ShortID: strings.ToLower(sid), Fingerprint: fp, Flow: flow}, nil
}

func (m *Manager) Import(ctx context.Context, command ImportCommand) (egressprofile.ProfileRevision, error) {
	if m == nil || ctx == nil {
		return egressprofile.ProfileRevision{}, ErrUnavailable
	}
	private, err := m.prepare(ctx, command)
	command.URI = ""
	command.TLS = nil
	if err != nil {
		return egressprofile.ProfileRevision{}, err
	}
	defer func() { private = payload{} }()
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return egressprofile.ProfileRevision{}, ErrUnavailable
	}
	ref, _ := secretstore.ParseReference("secret://egress-vless/" + hex.EncodeToString(random))
	clear(random)
	policy, err := (egressnetwork.Policy{Proxy: egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxyVLESS, CredentialRef: ref.String(), CredentialRevision: 1}, Resolver: command.Resolver, DirectDomains: command.DirectDomains}).Normalize()
	if err != nil {
		return egressprofile.ProfileRevision{}, egressprofile.ErrInvalidProfile
	}
	// Validate public metadata before writing any secret.
	if _, err := egressprofile.NewID(command.ID.String()); err != nil || command.ID == egressprofile.DirectID || command.ExpectedRevision >= egressprofile.MaxRevision || strings.TrimSpace(command.DisplayName) == "" {
		return egressprofile.ProfileRevision{}, egressprofile.ErrInvalidProfile
	}
	if (egressprofile.ProfileRevision{ID: command.ID, Revision: command.ExpectedRevision + 1, DisplayName: command.DisplayName, Policy: policy, PublishedAt: time.Now().UTC()}).Validate() != nil {
		return egressprofile.ProfileRevision{}, egressprofile.ErrInvalidProfile
	}
	data, err := json.Marshal(private)
	if err != nil {
		return egressprofile.ProfileRevision{}, ErrInvalidURI
	}
	defer clear(data)
	value, err := secretstore.NewValue(data)
	if err != nil {
		return egressprofile.ProfileRevision{}, ErrInvalidURI
	}
	defer value.Destroy()
	metadata, err := m.store.Replace(ctx, secretstore.ReplaceCommand{Reference: ref, ExpectedRevision: 0, Value: value})
	if err != nil || metadata.Validate() != nil || metadata.State != secretstore.StateConfigured {
		return egressprofile.ProfileRevision{}, ErrUnavailable
	}
	policy.Proxy.CredentialRevision = uint64(metadata.Revision)
	// A failed/indeterminate public commit may already reference this immutable
	// secret. Keep it for later cleanup; never delete potentially committed data.
	return m.profiles.Publish(ctx, egressprofile.PublishCommand{ID: command.ID, ExpectedRevision: command.ExpectedRevision, DisplayName: command.DisplayName, Policy: policy})
}

func (m *Manager) DialContext(ctx context.Context, policy egressnetwork.ProxyPolicy, network, address string) (net.Conn, error) {
	client, err := m.client(ctx, policy)
	if err != nil {
		return nil, err
	}
	return client.DialContext(ctx, network, address)
}

// DialUDP reuses the same immutable managed session as TCP and proxied DoH.
func (m *Manager) DialUDP(ctx context.Context, policy egressnetwork.ProxyPolicy, peer netip.AddrPort) (egressnetwork.DatagramConn, error) {
	client, err := m.client(ctx, policy)
	if err != nil {
		return nil, err
	}
	return client.DialUDP(ctx, peer)
}
func (m *Manager) client(ctx context.Context, policy egressnetwork.ProxyPolicy) (*vlesscore.Client, error) {
	if m == nil || ctx == nil || m.executable == "" {
		return nil, ErrUnavailable
	}
	ref, err := secretstore.ParseReference(policy.CredentialRef)
	if err != nil || ref.Namespace() != "egress-vless" || policy.Kind != egressnetwork.ProxyVLESS || policy.CredentialRevision == 0 || policy.CredentialRevision > uint64(secretstore.MaxRevision) {
		return nil, ErrUnavailable
	}
	var entry *session
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, ErrUnavailable
		}
		var exists bool
		entry, exists = m.sessions[policy]
		if !exists {
			entry = &session{ready: make(chan struct{})}
			m.sessions[policy] = entry
		}
		m.mu.Unlock()
		if !exists {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-entry.ready:
		}
		if entry.err != nil {
			return nil, entry.err
		}
		select {
		case <-entry.client.Done():
			// Only a confirmed exited and drained process loses its cache
			// entry. Concurrent later operations share one fresh startup;
			// request errors never retire a live core or replay its traffic.
			m.mu.Lock()
			if m.sessions[policy] == entry {
				delete(m.sessions, policy)
			}
			m.mu.Unlock()
			entry.cancel()
		default:
			return entry.client, nil
		}
	}
	// Startup belongs to the manager, while caller cancellation still aborts
	// this initial attempt. Detach only that caller callback after startup so
	// a reused immutable client outlives its original request.
	startup, cancelStartup := context.WithCancel(m.owner)
	stopCaller := context.AfterFunc(ctx, cancelStartup)
	client, loadErr := m.start(startup, ref, secretstore.Revision(policy.CredentialRevision))
	stopCaller()
	if ctx.Err() != nil || startup.Err() != nil {
		if client != nil {
			client.Close()
			client = nil
		}
		loadErr = ErrUnavailable
	}
	if loadErr != nil {
		cancelStartup()
	}
	m.mu.Lock()
	if m.closed && client != nil {
		m.mu.Unlock()
		client.Close()
		m.mu.Lock()
		client = nil
		loadErr = ErrUnavailable
	}
	entry.client, entry.err, entry.cancel = client, loadErr, cancelStartup
	if loadErr != nil {
		delete(m.sessions, policy)
	}
	close(entry.ready)
	m.mu.Unlock()
	return client, loadErr
}
func (m *Manager) start(ctx context.Context, ref secretstore.Reference, revision secretstore.Revision) (*vlesscore.Client, error) {
	p, err := m.load(ctx, ref, revision)
	if err != nil {
		return nil, err
	}
	defer func() { p = payload{} }()
	var addresses []netip.Addr
	if literal, err := netip.ParseAddr(p.Host); err == nil {
		addresses = []netip.Addr{literal}
	} else {
		addresses, err = m.resolver.LookupNetIP(ctx, "ip", p.Host)
		if err != nil {
			return nil, ErrUnavailable
		}
	}
	if len(addresses) == 0 {
		return nil, ErrUnavailable
	}
	for _, address := range addresses {
		if !address.IsValid() || address.IsUnspecified() || address.Zone() != "" {
			return nil, ErrUnavailable
		}
	}
	// One pinned bootstrap address, with the original SNI retained. No flow
	// replay or retry-to-Direct if this immutable core fails.
	config, err := p.coreConfig()
	if err != nil {
		return nil, ErrUnavailable
	}
	config.Executable = m.executable
	config.Server = netip.AddrPortFrom(addresses[0].Unmap(), p.Port)
	client, err := vlesscore.Start(ctx, config)
	if err != nil {
		return nil, ErrUnavailable
	}
	return client, nil
}

// Shutdown stops session admission and cancels manager-owned pending starts.
// Owned cleanup continues after a caller's wait expires; later calls can join
// the same drain, and no late startup can reopen admission.
func (m *Manager) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		return ErrUnavailable
	}
	m.shutdownOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		entries := make([]*session, 0, len(m.sessions))
		for _, entry := range m.sessions {
			entries = append(entries, entry)
		}
		m.mu.Unlock()
		m.cancelOwner()
		go func() {
			for _, entry := range entries {
				<-entry.ready
				if entry.client != nil {
					entry.client.Close()
				}
			}
			close(m.drained)
		}()
	})
	select {
	case <-m.drained:
		return nil
	default:
	}
	select {
	case <-m.drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close retains the synchronous compatibility API. Runtime uses Shutdown to
// bound its wait with the existing cleanup context.
func (m *Manager) Close() error { return m.Shutdown(context.Background()) }
