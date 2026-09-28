package activity

import (
	"context"
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

// ExchangeSummary counts the lifecycle winner once across all retained
// requests in Scope, never just a loaded page. It does not claim that a start
// without a retained terminal is still executing in the upstream provider.
type ExchangeSummary struct {
	Scope           SummaryScope   `json:"scope"`
	GeneratedAt     time.Time      `json:"generatedAt"`
	Requests        int            `json:"requests"`
	Succeeded       int            `json:"succeeded"`
	Failed          int            `json:"failed"`
	Canceled        int            `json:"canceled"`
	Pending         int            `json:"pending"`
	FirstObservedAt *time.Time     `json:"firstObservedAt"`
	LastObservedAt  *time.Time     `json:"lastObservedAt"`
	Failures        []FailureCount `json:"failures"`
	OtherFailures   int            `json:"otherFailures"`
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
