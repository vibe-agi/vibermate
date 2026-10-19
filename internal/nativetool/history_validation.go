package nativetool

import (
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"math"
	"time"
)

func historyID(id ID) bool               { _, err := NewID(string(id)); return err == nil }
func historyTimeValid(at time.Time) bool { return !at.IsZero() && at.UnixMilli() > 0 }
func (h RecordedHistory) Validate() error {
	o, p := h.Observation, h.Observation.Operation
	if h.Scope.Validate() != nil || p.Capture.Validate() != nil || h.Scope.Fence.ArchiveEpoch > math.MaxInt64 || h.Scope.Fence.CaptureGeneration > math.MaxInt64 || h.Scope != p.RecordingScope || h.Scope.Capture != p.Capture || !historyID(p.ID) || p.Revision == 0 || p.Revision > math.MaxInt64 || p.EnvironmentRevision == 0 || p.EnvironmentRevision > math.MaxInt64 || p.EnvironmentDigest == (environment.CandidateDigest{}) || p.Kind.Validate() != nil || p.Recording.Validate() != nil || p.Arguments != nil || p.Result != nil {
		return ErrInvalid
	}
	if _, err := environment.NewEnvironmentID(string(p.EnvironmentID)); err != nil {
		return ErrInvalid
	}
	origin, err := originidentity.ParseProviderOrigin(p.TargetOrigin)
	if err != nil || origin.Scheme() != "https" || origin.BasePath() != "" {
		return ErrInvalid
	}
	if len(p.TargetOrigin) > 1024 || len(p.ToolName) > 256 || len(p.CredentialOwner) > 128 || len(p.State) > 32 || len(p.DecisionSource) > 32 || len(p.Reason) > 64 || len(o.EventKind) > 32 || !historyTimeValid(p.CreatedAt) || o.EventAt.Before(p.CreatedAt) || o.MaximumEventAt.Before(o.EventAt) || !o.MetadataExpiresAt.After(p.CreatedAt) || p.MaximumEventAt != o.MaximumEventAt || p.MetadataExpiresAt != o.MetadataExpiresAt {
		return ErrInvalid
	}
	if p.BindingDigest != nil && *p.BindingDigest == (Digest{}) || p.ApprovalID != nil && !historyID(*p.ApprovalID) || p.BytesIn != nil && *p.BytesIn < 0 || p.BytesOut != nil && *p.BytesOut < 0 {
		return ErrInvalid
	}
	switch p.Reason {
	case "", "permission_off", "policy_strict", "approval_denied", "approval_expired", "revoked", "canceled", "transport_failed", "rpc_error", "execution_uncertain", "completed":
	default:
		return ErrInvalid
	}
	terminal := false
	validEvent := false
	switch p.State {
	case "received":
		validEvent = o.EventKind == "received"
	case "validated":
		validEvent = o.EventKind == "validated" || o.EventKind == "allowed"
	case "awaiting_decision":
		validEvent = o.EventKind == "awaiting_decision"
	case "started":
		validEvent = o.EventKind == "started" || o.EventKind == "progress"
	case "denied", "completed", "failed", "uncertain":
		terminal = true
		validEvent = o.EventKind == p.State
	case "canceled":
		terminal = true
		validEvent = o.EventKind == "canceled" || o.EventKind == "revoked"
	}
	if !validEvent || terminal != (p.CompletedAt != nil) || terminal != (p.Reason != "") || p.CompletedAt != nil && (p.CompletedAt.Before(p.CreatedAt) || !p.CompletedAt.Equal(o.EventAt)) || p.DecisionSource != "none" && p.DecisionSource != "policy" && p.DecisionSource != "user" {
		return ErrInvalid
	}
	return nil
}
