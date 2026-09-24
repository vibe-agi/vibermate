package upstreamservice

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrInvalidResponse = errors.New("service account facts are invalid")

// Facts is a strict projection: unrecognized response fields, arbitrary UI
// payloads, and credential material have no place in this representation.
type Facts struct {
	PlanType          string        `json:"planType,omitempty"`
	UpstreamAccountID string        `json:"upstreamAccountId,omitempty"`
	UpstreamUserID    string        `json:"upstreamUserId,omitempty"`
	Limits            []QuotaLimit  `json:"limits"`
	Credits           *Credits      `json:"credits,omitempty"`
	RateLimitResets   *ResetCredits `json:"rateLimitResets,omitempty"`
	History           *UsageHistory `json:"history,omitempty"`
}

// ResetCredits describes banked limit resets, not spendable usage credits.
// Missing and zero are different upstream observations.
type ResetCredits struct {
	AvailableCount           int64         `json:"availableCount"`
	ApplicableAvailableCount *int64        `json:"applicableAvailableCount,omitempty"`
	Details                  []ResetCredit `json:"details"`
}

type ResetCredit struct {
	ID          string `json:"id"`
	ResetType   string `json:"resetType"`
	Status      string `json:"status"`
	GrantedAt   string `json:"grantedAt"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

type UsageHistory struct {
	LifetimeTokens    *int64       `json:"lifetimeTokens,omitempty"`
	PeakDailyTokens   *int64       `json:"peakDailyTokens,omitempty"`
	CurrentStreakDays *int64       `json:"currentStreakDays,omitempty"`
	Daily             []DailyUsage `json:"daily"`
	AsOf              string       `json:"asOf,omitempty"`
	Partial           bool         `json:"partial"`
}
type DailyUsage struct {
	Date   string `json:"date"`
	Tokens int64  `json:"tokens"`
}
type QuotaLimit struct {
	ID           string       `json:"id"`
	Name         string       `json:"name,omitempty"`
	Model        string       `json:"model,omitempty"`
	Allowed      *bool        `json:"allowed,omitempty"`
	LimitReached *bool        `json:"limitReached,omitempty"`
	Primary      *QuotaWindow `json:"primary,omitempty"`
	Secondary    *QuotaWindow `json:"secondary,omitempty"`
}
type QuotaWindow struct {
	UsedPercent       int32 `json:"usedPercent"`
	WindowSeconds     int32 `json:"windowSeconds"`
	ResetAfterSeconds int32 `json:"resetAfterSeconds"`
	ResetAt           int32 `json:"resetAt"`
}
type Credits struct {
	HasCredits bool    `json:"hasCredits"`
	Unlimited  bool    `json:"unlimited"`
	Balance    *string `json:"balance,omitempty"`
}

type codexWindow struct {
	UsedPercent       *int32 `json:"used_percent"`
	WindowSeconds     *int32 `json:"limit_window_seconds"`
	ResetAfterSeconds *int32 `json:"reset_after_seconds"`
	ResetAt           *int32 `json:"reset_at"`
}
type codexLimit struct {
	Allowed      *bool        `json:"allowed"`
	LimitReached *bool        `json:"limit_reached"`
	Primary      *codexWindow `json:"primary_window"`
	Secondary    *codexWindow `json:"secondary_window"`
}

func (read Read) Project(body []byte) (Facts, error) {
	if read.Validate() != nil {
		return Facts{}, ErrUnsupported
	}
	if read.ID() == CodexUsageHistory {
		return projectCodexHistory(body)
	}
	if read.ID() == CodexResetCreditDetails {
		return projectCodexResetCredits(body)
	}
	var payload struct {
		PlanType   *string     `json:"plan_type"`
		AccountID  string      `json:"account_id"`
		UserID     string      `json:"user_id"`
		Limit      *codexLimit `json:"rate_limit"`
		Additional []struct {
			ID    string      `json:"metered_feature"`
			Name  string      `json:"limit_name"`
			Model string      `json:"normal_model_slug"`
			Limit *codexLimit `json:"rate_limit"`
		} `json:"additional_rate_limits"`
		Credits *struct {
			HasCredits *bool   `json:"has_credits"`
			Unlimited  *bool   `json:"unlimited"`
			Balance    *string `json:"balance"`
		} `json:"credits"`
		RateLimitResetCredits *struct {
			AvailableCount           *int64 `json:"available_count"`
			ApplicableAvailableCount *int64 `json:"applicable_available_count"`
		} `json:"rate_limit_reset_credits"`
	}
	if len(body) > 2<<20 || json.Unmarshal(body, &payload) != nil || payload.PlanType == nil ||
		!factText(*payload.PlanType, 128) || *payload.PlanType == "" || !factText(payload.AccountID, 256) || !factText(payload.UserID, 256) || len(payload.Additional) > 64 {
		return Facts{}, ErrInvalidResponse
	}
	facts := Facts{PlanType: *payload.PlanType, UpstreamAccountID: payload.AccountID, UpstreamUserID: payload.UserID, Limits: []QuotaLimit{}}
	if payload.Limit != nil {
		limit, err := projectCodexLimit("codex", "", "", payload.Limit)
		if err != nil {
			return Facts{}, err
		}
		facts.Limits = append(facts.Limits, limit)
	}
	seen := map[string]bool{"codex": true}
	for _, additional := range payload.Additional {
		if additional.ID == "" || additional.Name == "" || seen[additional.ID] {
			return Facts{}, ErrInvalidResponse
		}
		seen[additional.ID] = true
		limit, err := projectCodexLimit(additional.ID, additional.Name, additional.Model, additional.Limit)
		if err != nil {
			return Facts{}, err
		}
		facts.Limits = append(facts.Limits, limit)
	}
	if value := payload.Credits; value != nil {
		if value.HasCredits == nil || value.Unlimited == nil || value.Balance != nil && !factText(*value.Balance, 128) {
			return Facts{}, ErrInvalidResponse
		}
		facts.Credits = &Credits{HasCredits: *value.HasCredits, Unlimited: *value.Unlimited, Balance: value.Balance}
	}
	if resets := payload.RateLimitResetCredits; resets != nil {
		if resets.AvailableCount == nil || *resets.AvailableCount < 0 ||
			resets.ApplicableAvailableCount != nil &&
				(*resets.ApplicableAvailableCount < 0 || *resets.ApplicableAvailableCount > *resets.AvailableCount) {
			return Facts{}, ErrInvalidResponse
		}
		facts.RateLimitResets = &ResetCredits{
			AvailableCount:           *resets.AvailableCount,
			ApplicableAvailableCount: resets.ApplicableAvailableCount,
		}
	}
	return facts, nil
}

func projectCodexResetCredits(body []byte) (Facts, error) {
	var payload struct {
		AvailableCount *int64 `json:"available_count"`
		Credits        []struct {
			ID          string  `json:"id"`
			ResetType   string  `json:"reset_type"`
			Status      string  `json:"status"`
			GrantedAt   string  `json:"granted_at"`
			ExpiresAt   *string `json:"expires_at"`
			Title       *string `json:"title"`
			Description *string `json:"description"`
		} `json:"credits"`
	}
	if len(body) > 2<<20 || json.Unmarshal(body, &payload) != nil ||
		payload.AvailableCount == nil || *payload.AvailableCount < 0 ||
		len(payload.Credits) > 64 {
		return Facts{}, ErrInvalidResponse
	}
	resets := &ResetCredits{AvailableCount: *payload.AvailableCount, Details: make([]ResetCredit, 0, len(payload.Credits))}
	seen := make(map[string]bool, len(payload.Credits))
	for _, credit := range payload.Credits {
		if !factText(credit.ID, 256) || credit.ID == "" || seen[credit.ID] ||
			!factText(credit.ResetType, 64) || !factText(credit.Status, 64) ||
			!factText(credit.GrantedAt, 64) || credit.GrantedAt == "" ||
			!factOptionalText(credit.ExpiresAt, 64) ||
			!factOptionalText(credit.Title, 256) || !factOptionalText(credit.Description, 512) {
			return Facts{}, ErrInvalidResponse
		}
		if _, err := time.Parse(time.RFC3339, credit.GrantedAt); err != nil {
			return Facts{}, ErrInvalidResponse
		}
		if credit.ExpiresAt != nil && *credit.ExpiresAt != "" {
			if _, err := time.Parse(time.RFC3339, *credit.ExpiresAt); err != nil {
				return Facts{}, ErrInvalidResponse
			}
		}
		seen[credit.ID] = true
		resets.Details = append(resets.Details, ResetCredit{
			ID: credit.ID, ResetType: credit.ResetType, Status: credit.Status,
			GrantedAt: credit.GrantedAt, ExpiresAt: optionalFact(credit.ExpiresAt),
			Title: optionalFact(credit.Title), Description: optionalFact(credit.Description),
		})
	}
	return Facts{Limits: []QuotaLimit{}, RateLimitResets: resets}, nil
}

func factOptionalText(value *string, maximum int) bool {
	return value == nil || factText(*value, maximum)
}

func optionalFact(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func projectCodexLimit(id, name, model string, value *codexLimit) (QuotaLimit, error) {
	if !factText(id, 128) || !factText(name, 256) || !factText(model, 128) {
		return QuotaLimit{}, ErrInvalidResponse
	}
	limit := QuotaLimit{ID: id, Name: name, Model: model}
	if value == nil {
		return limit, nil
	}
	if value.Allowed == nil || value.LimitReached == nil {
		return QuotaLimit{}, ErrInvalidResponse
	}
	limit.Allowed, limit.LimitReached = value.Allowed, value.LimitReached
	var err error
	limit.Primary, err = projectCodexWindow(value.Primary)
	if err != nil {
		return QuotaLimit{}, err
	}
	limit.Secondary, err = projectCodexWindow(value.Secondary)
	if err != nil {
		return QuotaLimit{}, err
	}
	return limit, nil
}

func projectCodexWindow(value *codexWindow) (*QuotaWindow, error) {
	if value == nil {
		return nil, nil
	}
	if value.UsedPercent == nil || value.WindowSeconds == nil || value.ResetAfterSeconds == nil || value.ResetAt == nil ||
		*value.UsedPercent < 0 || *value.WindowSeconds < 0 || *value.ResetAfterSeconds < 0 || *value.ResetAt < 0 {
		return nil, ErrInvalidResponse
	}
	return &QuotaWindow{UsedPercent: *value.UsedPercent, WindowSeconds: *value.WindowSeconds, ResetAfterSeconds: *value.ResetAfterSeconds, ResetAt: *value.ResetAt}, nil
}

func factText(value string, maximum int) bool {
	return len(value) <= maximum && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func projectCodexHistory(body []byte) (Facts, error) {
	var payload struct {
		Stats *struct {
			LifetimeTokens    *int64 `json:"lifetime_tokens"`
			PeakDailyTokens   *int64 `json:"peak_daily_tokens"`
			CurrentStreakDays *int64 `json:"current_streak_days"`
			Daily             []struct {
				Date   string `json:"start_date"`
				Tokens *int64 `json:"tokens"`
			} `json:"daily_usage_buckets"`
		} `json:"stats"`
		Metadata *struct {
			AsOf  string `json:"stats_as_of"`
			Error string `json:"stats_error"`
		} `json:"metadata"`
	}
	if len(body) > 2<<20 || json.Unmarshal(body, &payload) != nil || payload.Stats == nil || len(payload.Stats.Daily) > 3660 {
		return Facts{}, ErrInvalidResponse
	}
	stats := payload.Stats
	for _, value := range []*int64{stats.LifetimeTokens, stats.PeakDailyTokens, stats.CurrentStreakDays} {
		if value != nil && *value < 0 {
			return Facts{}, ErrInvalidResponse
		}
	}
	history := &UsageHistory{LifetimeTokens: stats.LifetimeTokens, PeakDailyTokens: stats.PeakDailyTokens, CurrentStreakDays: stats.CurrentStreakDays, Daily: []DailyUsage{}}
	if payload.Metadata != nil {
		if !factText(payload.Metadata.AsOf, 128) {
			return Facts{}, ErrInvalidResponse
		}
		history.AsOf, history.Partial = payload.Metadata.AsOf, payload.Metadata.Error != ""
	}
	seen := make(map[string]bool)
	for _, bucket := range stats.Daily {
		if _, err := time.Parse("2006-01-02", bucket.Date); err != nil || seen[bucket.Date] || bucket.Tokens == nil || *bucket.Tokens < 0 {
			return Facts{}, ErrInvalidResponse
		}
		seen[bucket.Date] = true
		history.Daily = append(history.Daily, DailyUsage{Date: bucket.Date, Tokens: *bucket.Tokens})
	}
	return Facts{Limits: []QuotaLimit{}, History: history}, nil
}
