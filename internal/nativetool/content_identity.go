package nativetool

import (
	"context"
	"math"
	"time"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/originidentity"
)

type ContentWriter interface {
	PutNativeContent(context.Context, ContentObservation) error
}

func (i ContentIdentity) InspectionIdentity() InspectionIdentity {
	return InspectionIdentity{OperationID: i.OperationID, Capture: i.Scope.Capture, EnvironmentID: i.EnvironmentID, EnvironmentRevision: i.EnvironmentRevision, EnvironmentDigest: i.EnvironmentDigest, Kind: i.Kind, TargetOrigin: i.TargetOrigin, Recording: i.Recording, CreatedAt: i.CreatedAt}
}
func (i ContentIdentity) Validate() error {
	if _, err := NewID(string(i.OperationID)); err != nil {
		return ErrInvalid
	}
	if i.Scope.Fence.ArchiveEpoch > math.MaxInt64 || i.Scope.Fence.CaptureGeneration > math.MaxInt64 || i.Scope.Validate() != nil || i.Scope.Capture.Validate() != nil || i.EnvironmentRevision == 0 || i.EnvironmentRevision > math.MaxInt64 || i.EnvironmentDigest == (environment.CandidateDigest{}) || i.Kind != RequestToolsCall || i.Recording.Validate() != nil || i.Recording.Mode != environment.ContentRecordingFull || i.Recording.RetentionDays == 0 {
		return ErrInvalid
	}
	if _, err := environment.NewEnvironmentID(string(i.EnvironmentID)); err != nil {
		return ErrInvalid
	}
	for _, field := range []struct {
		value string
		limit int
	}{{i.ToolName, 256}, {i.TargetOrigin, 1024}, {i.CredentialOwner, 128}} {
		if len(field.value) == 0 || len(field.value) > field.limit || !utf8.ValidString(field.value) {
			return ErrInvalid
		}
	}
	origin, err := originidentity.ParseProviderOrigin(i.TargetOrigin)
	if err != nil || origin.Scheme() != "https" || origin.BasePath() != "" {
		return ErrInvalid
	}
	if i.CreatedAt.UnixMilli() <= 0 || !i.CreatedAt.Equal(time.UnixMilli(i.CreatedAt.UnixMilli())) || !i.ExpiresAt.Equal(i.CreatedAt.Add(time.Duration(i.Recording.RetentionDays)*24*time.Hour)) {
		return ErrInvalid
	}
	return nil
}
