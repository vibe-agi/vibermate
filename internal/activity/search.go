package activity

import (
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/environment"
)

const MaxSearchTextBytes = 256

type SearchQuery struct {
	BeforeSequence    int64
	Limit             int
	Text              string
	EnvironmentID     string
	AccountID         string
	Model             string
	Tool              string
	Status            Status
	Reason            string
	OccurredAtOrAfter time.Time
	OccurredBefore    time.Time
}

func (query SearchQuery) Validate() error {
	if query.BeforeSequence < 0 || query.Limit <= 0 || query.Limit > MaxPageSize ||
		query.OccurredAtOrAfter.IsZero() != query.OccurredBefore.IsZero() ||
		!validSearchText(query.Text, true) ||
		!validSearchText(query.Model, true) ||
		!validSearchText(query.Tool, true) ||
		!validSearchText(query.Reason, true) {
		return ErrInvalidEvent
	}
	if query.EnvironmentID != "" {
		if _, err := environment.NewEnvironmentID(query.EnvironmentID); err != nil {
			return ErrInvalidEvent
		}
	}
	if query.AccountID != "" && !validAccountID(query.AccountID) {
		return ErrInvalidEvent
	}
	switch query.Status {
	case "", StatusSucceeded, StatusPending, StatusFailed, StatusCanceled:
	default:
		return ErrInvalidEvent
	}
	if !query.OccurredAtOrAfter.IsZero() &&
		(!query.OccurredAtOrAfter.Equal(query.OccurredAtOrAfter.UTC().Truncate(time.Millisecond)) ||
			!query.OccurredBefore.Equal(query.OccurredBefore.UTC().Truncate(time.Millisecond)) ||
			!query.OccurredBefore.After(query.OccurredAtOrAfter)) {
		return ErrInvalidEvent
	}
	if query.Text == "" && query.EnvironmentID == "" && query.AccountID == "" &&
		query.Model == "" && query.Tool == "" && query.Status == "" &&
		query.Reason == "" && query.OccurredAtOrAfter.IsZero() {
		return ErrInvalidEvent
	}
	return nil
}

type SearchRequest struct {
	Query              SearchQuery
	ContentAvailableAt time.Time
}

func (request SearchRequest) Validate() error {
	if request.Query.Validate() != nil || request.ContentAvailableAt.IsZero() ||
		!request.ContentAvailableAt.Equal(
			request.ContentAvailableAt.UTC().Truncate(time.Millisecond),
		) {
		return ErrInvalidEvent
	}
	return nil
}

type SearchContext struct {
	WorkspaceID      string   `json:"workspaceId,omitempty"`
	WorkspaceLabel   string   `json:"workspaceLabel,omitempty"`
	CaptureLabel     string   `json:"captureLabel"`
	RequestedModel   string   `json:"requestedModel,omitempty"`
	EffectiveModel   string   `json:"effectiveModel,omitempty"`
	ReportedModel    string   `json:"reportedModel,omitempty"`
	ToolNames        []string `json:"toolNames"`
	ContentAvailable bool     `json:"contentAvailable"`
}

func (context SearchContext) Validate() error {
	if !validSearchResultText(context.WorkspaceID, 128) ||
		!validSearchResultText(context.WorkspaceLabel, 120) ||
		context.CaptureLabel == "" ||
		!validSearchResultText(context.CaptureLabel, 256) ||
		!validSearchResultText(context.RequestedModel, 512) ||
		!validSearchResultText(context.EffectiveModel, 512) ||
		!validSearchResultText(context.ReportedModel, 512) ||
		len(context.ToolNames) > 256 {
		return ErrInvalidEvent
	}
	seenTools := make(map[string]struct{}, len(context.ToolNames))
	for _, name := range context.ToolNames {
		if !validSearchResultText(name, MaxSearchTextBytes) || name == "" {
			return ErrInvalidEvent
		}
		if _, duplicate := seenTools[name]; duplicate {
			return ErrInvalidEvent
		}
		seenTools[name] = struct{}{}
	}
	if !context.ContentAvailable &&
		(context.RequestedModel != "" || context.EffectiveModel != "" ||
			context.ReportedModel != "" || len(context.ToolNames) != 0) {
		return ErrInvalidEvent
	}
	return nil
}

type SearchHit struct {
	Record  Record        `json:"record"`
	Context SearchContext `json:"context"`
	Matches []string      `json:"matches"`
}

func (hit SearchHit) Validate() error {
	if hit.Record.Validate() != nil || hit.Context.Validate() != nil ||
		len(hit.Matches) == 0 || len(hit.Matches) > 12 {
		return ErrInvalidEvent
	}
	seen := make(map[string]struct{}, len(hit.Matches))
	for _, match := range hit.Matches {
		switch match {
		case "workspace", "capture", "conversation", "environment", "account",
			"model", "tool", "status", "error", "source", "exchange", "time":
		default:
			return ErrInvalidEvent
		}
		if _, duplicate := seen[match]; duplicate {
			return ErrInvalidEvent
		}
		seen[match] = struct{}{}
	}
	return nil
}

func (hit SearchHit) Clone() SearchHit {
	cloned := hit
	cloned.Context.ToolNames = slices.Clone(hit.Context.ToolNames)
	cloned.Matches = slices.Clone(hit.Matches)
	if hit.Record.Conversation != nil {
		conversation := *hit.Record.Conversation
		cloned.Record.Conversation = &conversation
	}
	if hit.Record.Transport != nil {
		transport := hit.Record.Transport.Clone()
		cloned.Record.Transport = &transport
	}
	return cloned
}

type SearchPage struct {
	Items              []SearchHit `json:"items"`
	NextBeforeSequence int64       `json:"nextBeforeSequence,omitempty"`
}

func validSearchText(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	if len(value) > MaxSearchTextBytes || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validSearchResultText(value string, maximumBytes int) bool {
	if len(value) > maximumBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
