package activity

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"time"
)

// SummaryScope selects one launch or one proven native client session. A
// session may span launches; its client is part of its identity, not a label.
type SummaryScope struct {
	CaptureRunID    string `json:"captureRunId,omitempty"`
	ManualCaptureID string `json:"manualCaptureId,omitempty"`
	Client          string `json:"client,omitempty"`
	SessionID       string `json:"sessionId,omitempty"`
}

func (scope SummaryScope) Validate() error {
	launch := scope.CaptureRunID != "" || scope.ManualCaptureID != ""
	session := scope.Client != "" || scope.SessionID != ""
	if launch == session || (PageRequest{
		Limit: 1, CaptureRunID: scope.CaptureRunID, ManualCaptureID: scope.ManualCaptureID,
	}).Validate() != nil {
		return ErrInvalidEvent
	}
	if session && (validateIdentity("client", scope.Client, false) != nil ||
		validateIdentity("session", scope.SessionID, false) != nil) {
		return ErrInvalidEvent
	}
	return nil
}

type FailureCount struct {
	ReasonCode string `json:"reasonCode"`
	Count      int    `json:"count"`
}

// CaptureObservationSummary describes retained authorization observations,
// separately from the request lifecycle counts and their timestamp extrema.
type CaptureObservationSummary struct {
	CaptureKind        captureidentity.Kind `json:"captureKind"`
	CaptureID          string               `json:"captureId"`
	ArchiveEpoch       uint64               `json:"archiveEpoch"`
	CaptureGeneration  uint64               `json:"captureGeneration"`
	CredentialRevision uint64               `json:"credentialRevision"`
	FirstObservedAt    *time.Time           `json:"firstObservedAt"`
	LastObservedAt     *time.Time           `json:"lastObservedAt"`
}

// ExchangeSummary counts the lifecycle winner once across all retained
// requests in Scope, never just a loaded page. It does not claim that a start
// without a retained terminal is still executing in the upstream provider.
type ExchangeSummary struct {
	CaptureObservation *CaptureObservationSummary `json:"captureObservation,omitempty"`
	Scope              SummaryScope               `json:"scope"`
	GeneratedAt        time.Time                  `json:"generatedAt"`
	Requests           int                        `json:"requests"`
	Succeeded          int                        `json:"succeeded"`
	Failed             int                        `json:"failed"`
	Canceled           int                        `json:"canceled"`
	Pending            int                        `json:"pending"`
	FirstObservedAt    *time.Time                 `json:"firstObservedAt"`
	LastObservedAt     *time.Time                 `json:"lastObservedAt"`
	Failures           []FailureCount             `json:"failures"`
	OtherFailures      int                        `json:"otherFailures"`
	// ContentBodiesAvailable is set only for launch scopes. It answers whether
	// any non-expired full conversation body remains for that Capture, so a
	// statistics view can skip conversations(200) identity enrichment.
	ContentBodiesAvailable *bool `json:"contentBodiesAvailable,omitempty"`
}

func (manager *Manager) SummarizeExchanges(ctx context.Context, scope SummaryScope) (ExchangeSummary, error) {
	if manager == nil || scope.Validate() != nil {
		return ExchangeSummary{}, ErrInvalidEvent
	}
	operation, finish, err := manager.begin(ctx)
	if err != nil {
		return ExchangeSummary{}, err
	}
	defer finish()
	result, err := manager.repository.SummarizeExchanges(operation, scope)
	if err == nil {
		result.GeneratedAt = manager.clock.Now().UTC()
	}
	return result, err
}
