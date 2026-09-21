package serveridentity

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/vibe-agi/vibermate/internal/certidentity"
	"go.uber.org/zap"
)

type AutomaticChallenge string

const (
	AutomaticChallengeHTTP01    AutomaticChallenge = "http_01"
	AutomaticChallengeTLSALPN01 AutomaticChallenge = "tls_alpn_01"
)

type AutomaticState string

const (
	AutomaticStatePending       AutomaticState = "pending"
	AutomaticStateReady         AutomaticState = "ready"
	AutomaticStateRenewing      AutomaticState = "renewing"
	AutomaticStateRenewalFailed AutomaticState = "renewal_failed"
)

// AutomaticPolicy is deployment-owned configuration. TermsAgreed is explicit
// because starting certificate management publishes the configured name to an
// external CA and accepts that issuer's subscriber agreement.
type AutomaticPolicy struct {
	ServerName        string
	ContactEmail      string
	TermsAgreed       bool
	Challenge         AutomaticChallenge
	HTTPChallengePort int
	CA                string
}

func (policy AutomaticPolicy) Validate() error {
	if policy.ServerName == "" || policy.ServerName != strings.ToLower(policy.ServerName) ||
		strings.Contains(policy.ServerName, "*") ||
		net.ParseIP(policy.ServerName) != nil ||
		!strings.Contains(policy.ServerName, ".") ||
		!certmagic.SubjectQualifiesForPublicCert(policy.ServerName) {
		return errors.New("automatic HTTPS requires one public DNS name")
	}
	if _, err := certidentity.NewDNSName(policy.ServerName); err != nil {
		return errors.New("automatic HTTPS Server name is invalid")
	}
	for _, suffix := range []string{".test", ".example", ".invalid", ".lan"} {
		if strings.HasSuffix(policy.ServerName, suffix) {
			return errors.New("automatic HTTPS Server name is not publicly issuable")
		}
	}
	if !policy.TermsAgreed {
		return errors.New("automatic HTTPS requires explicit issuer terms acceptance")
	}
	if policy.ContactEmail != "" {
		address, err := mail.ParseAddress(policy.ContactEmail)
		if err != nil || address.Address != policy.ContactEmail ||
			address.Name != "" || len(policy.ContactEmail) > 320 {
			return errors.New("automatic HTTPS contact email is invalid")
		}
	}
	switch policy.Challenge {
	case AutomaticChallengeHTTP01:
		if policy.HTTPChallengePort < 1 || policy.HTTPChallengePort > 65535 {
			return errors.New("automatic HTTPS HTTP challenge port is invalid")
		}
	case AutomaticChallengeTLSALPN01:
		if policy.HTTPChallengePort != 0 {
			return errors.New("automatic HTTPS TLS-ALPN mode cannot use an HTTP challenge port")
		}
	default:
		return errors.New("automatic HTTPS challenge is invalid")
	}
	if policy.CA != "" {
		endpoint, err := url.Parse(policy.CA)
		if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" ||
			endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return errors.New("automatic HTTPS CA endpoint is invalid")
		}
	}
	return nil
}

type AutomaticOptions struct {
	Policy               AutomaticPolicy
	DataDirectory        string
	ListenHost           string
	TLSALPNChallengePort int
}

type AutomaticStatus struct {
	State       AutomaticState
	ServerName  string
	Challenge   AutomaticChallenge
	Fingerprint string
	Issuer      string
	NotBefore   time.Time
	NotAfter    time.Time
	LastError   string
}

type automaticManager interface {
	Manage(context.Context, string, bool) error
	TLSConfig() *tls.Config
	Current(string) (tls.Certificate, error)
	EventStatus() (AutomaticState, string)
	Close()
}

type AutomaticIdentity struct {
	manager   automaticManager
	policy    AutomaticPolicy
	tlsConfig *tls.Config
	cancel    context.CancelFunc
	closeOnce sync.Once
}

func OpenAutomatic(
	ctx context.Context,
	options AutomaticOptions,
) (*AutomaticIdentity, error) {
	if ctx == nil || options.Policy.Validate() != nil ||
		!validManagedPath(options.DataDirectory) ||
		(options.ListenHost != "" && net.ParseIP(options.ListenHost) == nil) ||
		(options.Policy.Challenge == AutomaticChallengeTLSALPN01 &&
			(options.TLSALPNChallengePort < 1 || options.TLSALPNChallengePort > 65535)) {
		return nil, errors.New("automatic HTTPS configuration is invalid")
	}
	manager, err := newCertMagicManager(options)
	if err != nil {
		return nil, err
	}
	return openAutomaticWithManager(ctx, options, manager)
}

func openAutomaticWithManager(
	ctx context.Context,
	options AutomaticOptions,
	manager automaticManager,
) (*AutomaticIdentity, error) {
	if ctx == nil || manager == nil || options.Policy.Validate() != nil ||
		!validManagedPath(options.DataDirectory) {
		return nil, errors.New("automatic HTTPS lifecycle is invalid")
	}
	lifecycleContext, cancel := context.WithCancel(ctx)
	asynchronous := options.Policy.Challenge == AutomaticChallengeTLSALPN01
	if err := manager.Manage(
		lifecycleContext,
		options.Policy.ServerName,
		asynchronous,
	); err != nil {
		cancel()
		manager.Close()
		return nil, fmt.Errorf("manage automatic HTTPS certificate: %w", err)
	}
	configuration := manager.TLSConfig()
	if configuration == nil || configuration.GetCertificate == nil {
		cancel()
		manager.Close()
		return nil, errors.New("automatic HTTPS manager returned an invalid TLS configuration")
	}
	configuration = configuration.Clone()
	configuration.MinVersion = tls.VersionTLS13
	configuration.NextProtos = []string{"http/1.1"}
	if options.Policy.Challenge == AutomaticChallengeTLSALPN01 {
		configuration.NextProtos = append(configuration.NextProtos, "acme-tls/1")
	}
	return &AutomaticIdentity{
		manager: manager, policy: options.Policy,
		tlsConfig: configuration, cancel: cancel,
	}, nil
}

func (identity *AutomaticIdentity) TLSConfig() *tls.Config {
	if identity == nil || identity.tlsConfig == nil {
		return nil
	}
	return identity.tlsConfig.Clone()
}

func (identity *AutomaticIdentity) Status() AutomaticStatus {
	if identity == nil || identity.manager == nil {
		return AutomaticStatus{}
	}
	state, lastError := identity.manager.EventStatus()
	status := AutomaticStatus{
		State: state, ServerName: identity.policy.ServerName,
		Challenge: identity.policy.Challenge, LastError: lastError,
	}
	certificate, err := identity.manager.Current(identity.policy.ServerName)
	if err != nil || len(certificate.Certificate) == 0 {
		if status.State == "" || status.State == AutomaticStateRenewing {
			status.State = AutomaticStatePending
		}
		return status
	}
	leaf := certificate.Leaf
	if leaf == nil {
		leaf, err = x509.ParseCertificate(certificate.Certificate[0])
		if err != nil {
			status.State = AutomaticStateRenewalFailed
			status.LastError = "active certificate could not be inspected"
			return status
		}
	}
	digest := sha256.Sum256(certificate.Certificate[0])
	status.Fingerprint = hex.EncodeToString(digest[:])
	status.Issuer = leaf.Issuer.String()
	status.NotBefore = leaf.NotBefore.UTC()
	status.NotAfter = leaf.NotAfter.UTC()
	if status.State == "" || status.State == AutomaticStatePending {
		status.State = AutomaticStateReady
	}
	return status
}

func (identity *AutomaticIdentity) Close() {
	if identity == nil {
		return
	}
	identity.closeOnce.Do(func() {
		identity.cancel()
		identity.manager.Close()
	})
}

type certMagicManager struct {
	config  *certmagic.Config
	cache   *certmagic.Cache
	tracker *automaticEventTracker
	logger  *zap.Logger
	once    sync.Once
}

func newCertMagicManager(options AutomaticOptions) (*certMagicManager, error) {
	if err := os.MkdirAll(options.DataDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("prepare automatic HTTPS storage: %w", err)
	}
	if err := os.Chmod(options.DataDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("protect automatic HTTPS storage: %w", err)
	}
	logger, err := zap.NewProduction()
	if err != nil {
		return nil, fmt.Errorf("prepare automatic HTTPS diagnostics: %w", err)
	}
	tracker := &automaticEventTracker{state: AutomaticStatePending}
	var configuration *certmagic.Config
	cache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) {
			if configuration == nil {
				return nil, errors.New("automatic HTTPS configuration is unavailable")
			}
			return configuration, nil
		},
		Logger: logger,
	})
	configuration = certmagic.New(cache, certmagic.Config{
		Storage: &certmagic.FileStorage{Path: options.DataDirectory},
		Logger:  logger, DefaultServerName: options.Policy.ServerName,
		OnEvent: tracker.observe,
	})
	ca := options.Policy.CA
	if ca == "" {
		ca = certmagic.LetsEncryptProductionCA
	}
	issuer := certmagic.NewACMEIssuer(configuration, certmagic.ACMEIssuer{
		CA: ca, TestCA: ca, Email: options.Policy.ContactEmail, Agreed: true,
		ListenHost:              options.ListenHost,
		AltHTTPPort:             options.Policy.HTTPChallengePort,
		AltTLSALPNPort:          options.TLSALPNChallengePort,
		DisableHTTPChallenge:    options.Policy.Challenge != AutomaticChallengeHTTP01,
		DisableTLSALPNChallenge: options.Policy.Challenge != AutomaticChallengeTLSALPN01,
		Logger:                  logger,
	})
	configuration.Issuers = []certmagic.Issuer{issuer}
	return &certMagicManager{
		config: configuration, cache: cache, tracker: tracker, logger: logger,
	}, nil
}

func (manager *certMagicManager) Manage(
	ctx context.Context,
	name string,
	asynchronous bool,
) error {
	if asynchronous {
		return manager.config.ManageAsync(ctx, []string{name})
	}
	return manager.config.ManageSync(ctx, []string{name})
}

func (manager *certMagicManager) TLSConfig() *tls.Config {
	return manager.config.TLSConfig()
}

func (manager *certMagicManager) Current(name string) (tls.Certificate, error) {
	certificates := manager.cache.AllMatchingCertificates(name)
	if len(certificates) == 0 {
		return tls.Certificate{}, errors.New("automatic HTTPS certificate is not available")
	}
	return certificates[0].Certificate, nil
}

func (manager *certMagicManager) EventStatus() (AutomaticState, string) {
	return manager.tracker.status()
}

func (manager *certMagicManager) Close() {
	manager.once.Do(func() {
		manager.cache.Stop()
		_ = manager.logger.Sync()
	})
}

type automaticEventTracker struct {
	mu        sync.Mutex
	state     AutomaticState
	lastError string
}

func (tracker *automaticEventTracker) observe(
	_ context.Context,
	event string,
	data map[string]any,
) error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	switch event {
	case "cert_obtaining":
		tracker.state = AutomaticStateRenewing
	case "cert_obtained":
		tracker.state = AutomaticStateReady
		tracker.lastError = ""
	case "cert_failed":
		tracker.state = AutomaticStateRenewalFailed
		if eventError, ok := data["error"].(error); ok {
			tracker.lastError = boundedAutomaticError(eventError.Error())
		} else {
			tracker.lastError = "certificate issuance or renewal failed"
		}
	}
	return nil
}

func (tracker *automaticEventTracker) status() (AutomaticState, string) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return tracker.state, tracker.lastError
}

func boundedAutomaticError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}
