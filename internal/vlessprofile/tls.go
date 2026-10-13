package vlessprofile

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/vlesscore"
)

type Description struct {
	Security  vlesscore.Security  `json:"security"`
	TrustMode vlesscore.TrustMode `json:"trustMode,omitempty"`
}
type storedTLS struct {
	Mode  vlesscore.TrustMode `json:"mode"`
	CAPEM string              `json:"caPem,omitempty"`
}

func (storedTLS) String() string   { return "vlessprofile.storedTLS{redacted}" }
func (storedTLS) GoString() string { return "vlessprofile.storedTLS{redacted}" }
func (payload) String() string     { return "vlessprofile.payload{redacted}" }
func (payload) GoString() string   { return "vlessprofile.payload{redacted}" }

// coreConfig is the shared validation/conversion boundary for import and startup.
// Executable and the resolved, pinned address belong to startup, not the secret.
func (p payload) coreConfig() (vlesscore.Config, error) {
	invalid := func() (vlesscore.Config, error) { return vlesscore.Config{}, ErrInvalidURI }
	host, err := egressnetwork.CanonicalHost(p.Host)
	if err != nil || host != p.Host || p.Port == 0 {
		return invalid()
	}
	sni, err := egressnetwork.CanonicalHost(p.SNI)
	if err != nil || sni != p.SNI {
		return invalid()
	}
	if len(p.UUID) != 36 || p.UUID[8] != '-' || p.UUID[13] != '-' || p.UUID[18] != '-' || p.UUID[23] != '-' {
		return invalid()
	}
	if b, err := hex.DecodeString(strings.ReplaceAll(p.UUID, "-", "")); err != nil || len(b) != 16 {
		return invalid()
	}
	switch p.Fingerprint {
	case "", "chrome", "firefox", "safari", "ios", "android", "edge", "360", "qq", "random", "randomized":
	default:
		return invalid()
	}
	if p.Flow != "" && p.Flow != "xtls-rprx-vision" && p.Flow != "xtls-rprx-vision-udp443" {
		return invalid()
	}
	security := p.Security
	if security == "" {
		security = vlesscore.SecurityREALITY
	}
	c := vlesscore.Config{UUID: p.UUID, ServerName: p.SNI, Password: p.Key, ShortID: p.ShortID, Fingerprint: p.Fingerprint, Flow: p.Flow, Security: security}
	switch security {
	case vlesscore.SecurityREALITY:
		if p.TLS != nil {
			return invalid()
		}
		if b, err := base64.RawURLEncoding.DecodeString(p.Key); err != nil || len(b) != 32 {
			return invalid()
		}
		if b, err := hex.DecodeString(p.ShortID); err != nil || len(b) > 8 {
			return invalid()
		}
	case vlesscore.SecurityTLS:
		if p.Key != "" || p.ShortID != "" || p.TLS == nil {
			return invalid()
		}
		options := vlesscore.TLSOptions{Mode: p.TLS.Mode, CAPEM: p.TLS.CAPEM}
		if options.Validate() != nil {
			return invalid()
		}
		c.TLS = &options
	default:
		return invalid()
	}
	return c, nil
}

func (m *Manager) prepare(ctx context.Context, c ImportCommand) (payload, error) {
	var p payload
	var err error
	if c.URI != "" {
		p, err = parseURI(c.URI)
	} else {
		if c.ExpectedRevision == 0 || c.ExpectedRevision >= egressprofile.MaxRevision {
			return payload{}, egressprofile.ErrInvalidProfile
		}
		prior, loadErr := m.profiles.GetRevision(ctx, c.ID, c.ExpectedRevision)
		if loadErr != nil {
			return payload{}, loadErr
		}
		if prior.Policy.Proxy.Kind != egressnetwork.ProxyVLESS {
			return payload{}, egressprofile.ErrInvalidProfile
		}
		p, err = m.loadPolicy(ctx, prior.Policy.Proxy)
	}
	if err != nil {
		return payload{}, err
	}
	security := p.Security
	if security == "" {
		security = vlesscore.SecurityREALITY
	}
	if c.TLS != nil {
		if security != vlesscore.SecurityTLS || c.TLS.Validate() != nil {
			return payload{}, ErrInvalidURI
		}
		p.TLS = &storedTLS{Mode: c.TLS.Mode, CAPEM: c.TLS.CAPEM}
	} else if c.URI != "" && security == vlesscore.SecurityTLS {
		p.TLS = &storedTLS{Mode: vlesscore.TrustSystem}
	}
	if _, err := p.coreConfig(); err != nil {
		return payload{}, err
	}
	return p, nil
}

func (m *Manager) loadPolicy(ctx context.Context, policy egressnetwork.ProxyPolicy) (payload, error) {
	ref, err := secretstore.ParseReference(policy.CredentialRef)
	if err != nil || ref.Namespace() != "egress-vless" || policy.Kind != egressnetwork.ProxyVLESS || policy.CredentialRevision == 0 || policy.CredentialRevision > uint64(secretstore.MaxRevision) {
		return payload{}, ErrUnavailable
	}
	return m.load(ctx, ref, secretstore.Revision(policy.CredentialRevision))
}
func (m *Manager) load(ctx context.Context, ref secretstore.Reference, revision secretstore.Revision) (payload, error) {
	value, err := secretstore.ValidateReaderResult(m.store.ReadAtRevision(ctx, ref, revision))
	if err != nil {
		return payload{}, ErrUnavailable
	}
	defer value.Destroy()
	data, err := value.CopyBytes()
	if err != nil {
		return payload{}, ErrUnavailable
	}
	defer clear(data)
	var p payload
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&p) != nil {
		return payload{}, ErrUnavailable
	}
	if decoder.Decode(new(any)) != io.EOF {
		return payload{}, ErrUnavailable
	}
	if _, err := p.coreConfig(); err != nil {
		return payload{}, ErrUnavailable
	}
	return p, nil
}
func (m *Manager) Describe(ctx context.Context, id egressprofile.ID, revision egressprofile.Revision) (Description, error) {
	if m == nil || ctx == nil {
		return Description{}, ErrUnavailable
	}
	prior, err := m.profiles.GetRevision(ctx, id, revision)
	if err != nil {
		return Description{}, err
	}
	if prior.Policy.Proxy.Kind != egressnetwork.ProxyVLESS {
		return Description{}, egressprofile.ErrInvalidProfile
	}
	p, err := m.loadPolicy(ctx, prior.Policy.Proxy)
	if err != nil {
		return Description{}, err
	}
	defer func() { p = payload{} }()
	config, err := p.coreConfig()
	if err != nil {
		return Description{}, ErrUnavailable
	}
	description := Description{Security: config.Security}
	if config.TLS != nil {
		description.TrustMode = config.TLS.Mode
	}
	return description, nil
}
