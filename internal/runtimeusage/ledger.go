package runtimeusage

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

var ErrPolicyConflict = errors.New("usage collection policy changed")

// CollectionPolicy is independent of body recording. Existing installations
// start disabled; enabling it is an explicit owner decision, not a new meaning
// for ContentRecordingOff. Disabling stops new observations, not existing history.
type CollectionPolicy struct {
	Enabled         bool       `json:"enabled"`
	RetentionDays   int        `json:"retentionDays"`
	Revision        int64      `json:"revision"`
	CollectingSince *time.Time `json:"collectingSince,omitempty"`
}

func (policy CollectionPolicy) Validate() error {
	if policy.Revision < 1 || policy.RetentionDays < 1 || policy.RetentionDays > 365 {
		return errors.New("invalid usage collection policy")
	}
	return nil
}

// Observation contains no prompt, response, tool arguments, headers, credentials
// or URLs. One Exchange, regardless of retries, has one immutable observation.
// Ownership is resolved by the repository from the admitted Capture, never from
// a client-supplied user label or the identity of the dashboard viewer.
type Observation struct {
	ExchangeID          string             `json:"exchangeId"`
	CaptureRunID        string             `json:"captureRunId,omitempty"`
	ManualCaptureID     string             `json:"manualCaptureId,omitempty"`
	UserID              runtimeuser.UserID `json:"userId,omitempty"`
	Source              string             `json:"source"`
	StartedAt           time.Time          `json:"startedAt"`
	OccurredAt          time.Time          `json:"occurredAt"`
	Status              activity.Status    `json:"status"`
	EnvironmentID       string             `json:"environmentId"`
	EnvironmentRevision uint64             `json:"environmentRevision"`
	EnvironmentName     string             `json:"environmentName"`
	AccountID           string             `json:"accountId,omitempty"`
	AccountName         string             `json:"accountName,omitempty"`
	RequestedModel      string             `json:"requestedModel,omitempty"`
	UpstreamModel       string             `json:"upstreamModel,omitempty"`
	Usage               protocolcore.Usage `json:"usage"`
	Client              string             `json:"client,omitempty"`
	SessionID           string             `json:"sessionId,omitempty"`
	Attribution         *Attribution       `json:"attribution,omitempty"`
}

// Attribution is a frozen, display-only Capture snapshot. It never grants
// visibility: member reports are still filtered by authenticated UserID first.
type Attribution struct {
	CallerID    string                  `json:"callerId"`
	CallerLabel string                  `json:"callerLabel"`
	CallerKind  string                  `json:"callerKind"`
	ProjectID   string                  `json:"projectId"`
	GitAtLaunch *capturerun.GitSnapshot `json:"gitAtLaunch,omitempty"`
}

func (observation Observation) Validate() error {
	if observation.ExchangeID == "" || observation.StartedAt.IsZero() || observation.OccurredAt.IsZero() ||
		(observation.CaptureRunID != "" && observation.ManualCaptureID != "") {
		return errors.New("invalid usage observation")
	}
	if value := observation.Attribution; value != nil {
		if value.CallerKind != "" && value.CallerKind != "member" && value.CallerKind != "local" {
			return errors.New("invalid usage caller kind")
		}
		for _, text := range []string{value.CallerID, value.CallerLabel, value.ProjectID} {
			if len(text) > 256 || !utf8.ValidString(text) || strings.IndexFunc(text, unicode.IsControl) >= 0 {
				return errors.New("invalid usage attribution")
			}
		}
		if value.GitAtLaunch != nil && value.GitAtLaunch.Validate() != nil {
			return errors.New("invalid usage project")
		}
	}
	switch observation.Status {
	case activity.StatusSucceeded, activity.StatusFailed, activity.StatusCanceled:
	default:
		return errors.New("usage observation is not terminal")
	}
	for _, value := range []string{observation.ExchangeID, observation.CaptureRunID, observation.ManualCaptureID,
		observation.EnvironmentID, observation.EnvironmentName, observation.AccountID, observation.AccountName,
		observation.RequestedModel, observation.UpstreamModel, observation.Client, observation.SessionID} {
		if len(value) > 1024 || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return errors.New("invalid usage observation field")
		}
	}
	return observation.Usage.Validate()
}

type Recorder interface {
	RecordUsage(context.Context, Observation) error
}

type Repository interface {
	Recorder
	UsagePolicy(context.Context) (CollectionPolicy, error)
	SetUsagePolicy(context.Context, CollectionPolicy, time.Time) (CollectionPolicy, error)
	ListUsage(context.Context, Query, runtimeuser.UserID, time.Time, int) ([]Observation, bool, error)
}
