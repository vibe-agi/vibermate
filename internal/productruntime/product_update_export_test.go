package productruntime

import (
	"context"
	"crypto/x509"
	"errors"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"testing"
)

func ProductUpdateTestOptions(t *testing.T, gate offlinehold.RuntimeCoordinator) Options {
	return testOptions(t, hostcontract.Desktop(), gate)
}

// Test utility only: cross-package real control tests cannot import desktopcontrol
// from package productruntime without a Go test import cycle. No production API.
func StartProductUpdateTestRuntime(ctx context.Context, options Options, transport providertransport.Transport, builder *egressnetwork.Builder, roots *x509.CertPool, probes ...*EgressOfferProbe) (*Runtime, error) {
	if transport == nil || builder == nil {
		return nil, errors.New("contained update test dependencies missing")
	}
	overrides := runtimeBuildOverrides{
		originalProber: func() (*originaltransport.TLSProber, error) {
			return originaltransport.NewTLSProberWithBuilder(builder, roots)
		},
		providerProber: func() (*providertransport.ProviderProber, error) {
			return providertransport.NewProviderProberWithBuilder(builder, roots)
		},
		provider: func(request providerBuildRequest) (providerRuntime, error) {
			bearer, err := providertransport.NewStaticBearerAuthenticator(request.secrets)
			if err != nil {
				return nil, err
			}
			anthropic, err := providertransport.NewAnthropicAPIKeyAuthenticator(request.secrets)
			if err != nil {
				return nil, err
			}
			codex, err := providertransport.NewCodexOAuthAuthenticator(request.secrets)
			if err != nil {
				return nil, err
			}
			return providertransport.NewClient(providertransport.ClientOptions{Coordinator: request.coordinator, Authenticators: []providertransport.Authenticator{bearer, anthropic, codex}, Transport: transport, InstanceIDs: request.instanceIDs, Audit: request.audit, RawEvidence: request.rawEvidence, EgressDefaults: request.egressDefaults})
		},
	}
	if len(probes) > 0 {
		overrides.egressObserver = probes[0].wrap
	}
	return startWithBuilders(ctx, options, overrides)
}
