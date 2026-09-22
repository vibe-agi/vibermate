package runtimepersistence

import (
	"context"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

// GetConversationEvidence intentionally never visits the content-addressed
// transcript chain. Identity enrichment and list chrome need only the exact
// protocol identifiers already in the manifest, irrespective of history size.
func (repository *exchangeContentRepository) GetConversationEvidence(
	ctx context.Context, exchangeID string, now time.Time,
) (exchangecontent.ConversationEvidence, error) {
	operation, finish, err := repository.operations.begin(ctx)
	if err != nil {
		return exchangecontent.ConversationEvidence{}, err
	}
	defer finish()
	reference, err := loadStoredContentReference(operation, repository.database, exchangeID, now)
	if err != nil {
		return exchangecontent.ConversationEvidence{}, err
	}
	manifest := reference.manifest
	if manifest.Parent.Validate() != nil || manifest.Frozen.Validate() != nil ||
		(manifest.Mode != environment.ContentRecordingFull && manifest.Mode != environment.ContentRecordingMetadataOnly) {
		return exchangecontent.ConversationEvidence{}, exchangecontent.ErrInvalidEvidence
	}
	value := exchangecontent.ConversationEvidence{
		ExchangeID: manifest.ExchangeID, RecordedAt: manifest.RecordedAt, ExpiresAt: manifest.ExpiresAt,
		RequestProtocolEvidence: manifest.Request.ProtocolEvidence,
	}
	if manifest.Response != nil {
		value.ResponseID = manifest.Response.ID
		value.ResponseProtocolEvidence = manifest.Response.ProtocolEvidence
	}
	if err := value.Validate(); err != nil {
		return exchangecontent.ConversationEvidence{}, err
	}
	return value, nil
}
