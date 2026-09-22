package exchangecontent

import (
	"context"
	"time"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// ConversationEvidence is the retention-bound, body-free identity projection
// of one Exchange. Reading it validates the manifest, not transcript blocks;
// it must never stand in for Get/GetProjection when displaying content.
type ConversationEvidence struct {
	ExchangeID               string
	RecordedAt               time.Time
	ExpiresAt                time.Time
	ResponseID               string
	RequestProtocolEvidence  []protocolcore.ProtocolEvidenceValue
	ResponseProtocolEvidence []protocolcore.ProtocolEvidenceValue
}

func (value ConversationEvidence) Validate() error {
	if !validIdentity(value.ExchangeID, MaxExchangeIDBytes) || value.RecordedAt.IsZero() ||
		!value.ExpiresAt.After(value.RecordedAt) ||
		(value.ResponseID != "" && !validIdentity(value.ResponseID, 512)) ||
		protocolcore.ValidateProtocolEvidence(value.RequestProtocolEvidence) != nil ||
		protocolcore.ValidateProtocolEvidence(value.ResponseProtocolEvidence) != nil {
		return ErrInvalidEvidence
	}
	return nil
}

func (value ConversationEvidence) Clone() ConversationEvidence {
	value.RequestProtocolEvidence = append([]protocolcore.ProtocolEvidenceValue(nil), value.RequestProtocolEvidence...)
	value.ResponseProtocolEvidence = append([]protocolcore.ProtocolEvidenceValue(nil), value.ResponseProtocolEvidence...)
	return value
}

func (record Record) ConversationEvidence() ConversationEvidence {
	value := ConversationEvidence{
		ExchangeID: record.ExchangeID, RecordedAt: record.RecordedAt, ExpiresAt: record.ExpiresAt,
		RequestProtocolEvidence: record.Request.ProtocolEvidence,
	}
	if record.Response != nil {
		value.ResponseID = record.Response.ID
		value.ResponseProtocolEvidence = record.Response.ProtocolEvidence
	}
	return value.Clone()
}

func (manager *Manager) GetConversationEvidence(ctx context.Context, exchangeID string) (ConversationEvidence, error) {
	if !validIdentity(exchangeID, MaxExchangeIDBytes) {
		return ConversationEvidence{}, ErrInvalidEvidence
	}
	operation, finish, err := manager.begin(ctx)
	if err != nil {
		return ConversationEvidence{}, err
	}
	defer finish()
	now := manager.clock.Now().UTC()
	value, err := manager.repository.GetConversationEvidence(operation, exchangeID, now)
	if err != nil {
		return ConversationEvidence{}, err
	}
	if value.ExchangeID != exchangeID || value.Validate() != nil || !value.ExpiresAt.After(now) {
		return ConversationEvidence{}, ErrInvalidEvidence
	}
	return value.Clone(), nil
}
