package rawevidence

import (
	"context"
	"crypto/hmac"
	"net/http"
	"strings"

	"github.com/vibe-agi/vibermate/internal/providerauth"
	"golang.org/x/net/http/httpguts"
)

type HeaderRevealRequest struct {
	RevealRequest
	Name string
}

func (request HeaderRevealRequest) Validate() error {
	if request.RevealRequest.Validate() != nil || len(request.Name) > 256 || !httpguts.ValidHeaderFieldName(request.Name) {
		return ErrInvalidReveal
	}
	return nil
}

type RevealedHeader struct {
	EnvelopeID string `json:"envelopeId"`
	Name       string `json:"name"`
	Value      string `json:"value"`
}

type HeaderRevealer interface {
	RevealHeader(context.Context, HeaderRevealRequest, providerauth.HeaderReader) (RevealedHeader, error)
}

// RevealHeader never stores plaintext or treats the current account settings
// as historical evidence. Exact epoch + fingerprint proof + durable audit are
// required on every explicit reveal. Ordinary Reveal and diagnostics stay redacted.
func (manager *Manager) RevealHeader(ctx context.Context, request HeaderRevealRequest, source providerauth.HeaderReader) (RevealedHeader, error) {
	if manager == nil || ctx == nil || source == nil {
		return RevealedHeader{}, ErrPayloadUnavailable
	}
	if err := request.Validate(); err != nil {
		return RevealedHeader{}, err
	}
	record, err := manager.repository.GetEnvelope(ctx, request.EnvelopeID)
	if err != nil {
		return RevealedHeader{}, err
	}
	result, err := manager.readHistoricalHeader(ctx, record, request.Name, source)
	outcome := RevealSucceeded
	if err != nil {
		outcome = RevealUnavailable
	}
	if auditErr := manager.repository.AppendRevealAudit(ctx, RevealAudit{
		EnvelopeID: record.EnvelopeID, ExchangeID: record.ExchangeID,
		ActorID: request.ActorID, Outcome: outcome, OccurredAt: manager.clock.Now().UTC(),
	}); auditErr != nil {
		return RevealedHeader{}, ErrPayloadUnavailable
	}
	if err != nil {
		return RevealedHeader{}, err
	}
	return result, nil
}

func (manager *Manager) readHistoricalHeader(ctx context.Context, record StoredEnvelope, name string, source providerauth.HeaderReader) (RevealedHeader, error) {
	if record.Layer != LayerProviderEgress || record.AccountID == "" || record.CredentialEpoch == 0 ||
		!record.ExpiresAt.After(manager.clock.Now()) {
		return RevealedHeader{}, ErrPayloadUnavailable
	}
	payload, err := manager.ReadPayload(record)
	if err != nil {
		return RevealedHeader{}, ErrPayloadUnavailable
	}
	defer clear(payload.Body)
	for _, field := range payload.Headers {
		if !strings.EqualFold(field.Name, name) {
			continue
		}
		if len(field.Redacted) != 1 || len(field.Values) != 0 {
			break
		}
		value, err := source.ReadOverwriteHeader(ctx, providerauth.HeaderLookup{
			AccountID: record.AccountID, AccountRevision: record.AccountRevision, CredentialEpoch: record.CredentialEpoch,
			UpstreamEndpointID: record.UpstreamEndpointID, UpstreamEndpointRevision: record.UpstreamEndpointRevision,
			Name: field.Name,
		})
		if err != nil {
			break
		}
		actual := manager.redactor.protectedField(field.Name, []string{value}, true).Redacted[0]
		expected := field.Redacted[0]
		if len(value) > 16<<10 || actual.Bytes != expected.Bytes || !hmac.Equal([]byte(actual.Digest), []byte(expected.Digest)) {
			break
		}
		return RevealedHeader{EnvelopeID: record.EnvelopeID, Name: http.CanonicalHeaderKey(field.Name), Value: value}, nil
	}
	return RevealedHeader{}, ErrPayloadUnavailable
}
