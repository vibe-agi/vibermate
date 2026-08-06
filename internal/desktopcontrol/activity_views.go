package desktopcontrol

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
)

const activityCursorPrefix = "v1:activity-requests:"

var errInvalidActivityQuery = errors.New("Activity query is invalid")

// ActivitySummary is the exact additionalProperties:false representation of
// one request in the local control contract. Its ID is the Exchange identity,
// not the identity of the internal Activity audit record.
type ActivitySummary struct {
	ID         string             `json:"id"`
	OccurredAt time.Time          `json:"occurredAt"`
	Kind       string             `json:"kind"`
	Title      string             `json:"title"`
	Status     string             `json:"status"`
	Source     ActivitySourceRef  `json:"source"`
	Access     ActivityAccessRef  `json:"access"`
	ParentRefs ActivityParentRefs `json:"parentRefs"`
}

type ActivitySourceRef struct {
	Kind        string `json:"kind"`
	DisplayName string `json:"displayName"`
	Recognition string `json:"recognition"`
}

type ActivityAccessRef struct {
	ID                  string `json:"id"`
	DisplayName         string `json:"displayName"`
	ApplicationRevision uint64 `json:"applicationRevision"`
}

type ActivityParentRefs struct {
	CaptureRunID     string `json:"captureRunId,omitempty"`
	ManualCaptureID  string `json:"manualCaptureId,omitempty"`
	IngressProfileID string `json:"ingressProfileId"`
	ConnectionID     string `json:"connectionId,omitempty"`
	AccessID         string `json:"accessId"`
	ExchangeID       string `json:"exchangeId"`
}

// Validate checks the closed public relationship rather than only its JSON
// shape. CaptureRun, connection, Access, and Exchange identifiers remain
// independent fields; none is inferred by splitting another identifier.
func (summary ActivitySummary) Validate() error {
	if summary.OccurredAt.IsZero() ||
		summary.Kind != "exchange" ||
		summary.Title != summary.Source.DisplayName ||
		len(summary.Title) > access.MaxAccessNameBytes ||
		summary.ParentRefs.AccessID != summary.Access.ID ||
		summary.ParentRefs.ExchangeID != summary.ID ||
		len(summary.Access.DisplayName) > access.MaxAccessNameBytes {
		return errors.New("Activity summary relationship is invalid")
	}
	accessID, err := access.NewAccessID(summary.Access.ID)
	if err != nil {
		return errors.New("Activity summary Access is invalid")
	}
	status := activity.Status(summary.Status)
	if status != activity.StatusSucceeded &&
		status != activity.StatusFailed &&
		status != activity.StatusCanceled {
		return errors.New("Activity summary status is invalid")
	}
	return (activity.Event{
		Kind:              activity.KindExchangeCompleted,
		AccessID:          accessID,
		AccessName:        summary.Access.DisplayName,
		AccessRevision:    summary.Access.ApplicationRevision,
		SubjectID:         summary.ID,
		Status:            status,
		SourceKind:        activity.SourceKind(summary.Source.Kind),
		SourceDisplayName: summary.Source.DisplayName,
		SourceRecognition: activity.SourceRecognition(summary.Source.Recognition),
		CaptureRunID:      summary.ParentRefs.CaptureRunID,
		ManualCaptureID:   summary.ParentRefs.ManualCaptureID,
		IngressProfileID:  summary.ParentRefs.IngressProfileID,
		ConnectionID:      summary.ParentRefs.ConnectionID,
	}).Validate()
}

// ActivityPage is the exact cursor-paginated public Activity representation.
type ActivityPage struct {
	Items      []ActivitySummary `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

// ExchangeDetail is the closed public projection for one completed logical
// request. It joins only redacted identifiers already owned by Activity and
// EgressAttempt; no prompt, header, credential value, URL path, or body is
// reachable from this representation.
type ExchangeDetail struct {
	ID              string                  `json:"id"`
	AccessID        string                  `json:"accessId"`
	Status          string                  `json:"status"`
	ProcessingTrace ExchangeProcessingTrace `json:"processingTrace"`
}

type ExchangeProcessingTrace struct {
	UpstreamProfileID string   `json:"upstreamProfileId,omitempty"`
	CredentialID      string   `json:"credentialId,omitempty"`
	EgressProxyID     string   `json:"egressProxyId,omitempty"`
	PluginRunIDs      []string `json:"pluginRunIds"`
	AttemptIDs        []string `json:"attemptIds"`
	Result            string   `json:"result"`
}

type activityListQuery struct {
	beforeSequence int64
	limit          int
	captureRunID   string
	accessID       string
}

func parseActivityListQuery(rawQuery string) (activityListQuery, error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return activityListQuery{}, errInvalidActivityQuery
	}
	for name, entries := range values {
		if (name != "cursor" &&
			name != "limit" &&
			name != "captureRunId" &&
			name != "accessId" &&
			name != "kind") || len(entries) != 1 {
			return activityListQuery{}, errInvalidActivityQuery
		}
	}
	query := activityListQuery{limit: 50}
	if entries, present := values["limit"]; present {
		if entries[0] == "" {
			return activityListQuery{}, errInvalidActivityQuery
		}
		query.limit, err = strconv.Atoi(entries[0])
		if err != nil || query.limit < 1 || query.limit > activity.MaxPageSize {
			return activityListQuery{}, errInvalidActivityQuery
		}
	}
	if entries, present := values["cursor"]; present {
		query.beforeSequence, err = parseActivityCursor(entries[0])
		if err != nil {
			return activityListQuery{}, errInvalidActivityQuery
		}
	}
	if entries, present := values["captureRunId"]; present {
		query.captureRunID = entries[0]
		if query.captureRunID == "" {
			return activityListQuery{}, errInvalidActivityQuery
		}
	}
	if entries, present := values["accessId"]; present {
		query.accessID = entries[0]
		if query.accessID == "" {
			return activityListQuery{}, errInvalidActivityQuery
		}
	}
	if entries, present := values["kind"]; present && entries[0] != "exchange" {
		return activityListQuery{}, errInvalidActivityQuery
	}
	return query, nil
}

func activityPageOf(page activity.Page) (ActivityPage, error) {
	view := ActivityPage{Items: make([]ActivitySummary, 0, len(page.Items))}
	for _, record := range page.Items {
		if record.Kind != activity.KindExchangeCompleted || record.Validate() != nil {
			return ActivityPage{}, errors.New("Activity Exchange projection is invalid")
		}
		summary := ActivitySummary{
			ID:         record.SubjectID,
			OccurredAt: record.OccurredAt,
			Kind:       "exchange",
			Title:      record.SourceDisplayName,
			Status:     string(record.Status),
			Source: ActivitySourceRef{
				Kind:        string(record.SourceKind),
				DisplayName: record.SourceDisplayName,
				Recognition: string(record.SourceRecognition),
			},
			Access: ActivityAccessRef{
				ID:                  record.AccessID,
				DisplayName:         record.AccessName,
				ApplicationRevision: record.AccessRevision,
			},
			ParentRefs: ActivityParentRefs{
				CaptureRunID:     record.CaptureRunID,
				ManualCaptureID:  record.ManualCaptureID,
				IngressProfileID: record.IngressProfileID,
				ConnectionID:     record.ConnectionID,
				AccessID:         record.AccessID,
				ExchangeID:       record.SubjectID,
			},
		}
		if err := summary.Validate(); err != nil {
			return ActivityPage{}, err
		}
		view.Items = append(view.Items, summary)
	}
	if page.NextBeforeSequence != 0 {
		cursor, err := activityCursor(page.NextBeforeSequence)
		if err != nil {
			return ActivityPage{}, err
		}
		view.NextCursor = cursor
	}
	return view, nil
}

func exchangeDetailOf(
	record activity.Record,
	egressPage egressaudit.Page,
) (ExchangeDetail, error) {
	if record.Kind != activity.KindExchangeCompleted ||
		record.Validate() != nil ||
		egressPage.NextCursor != "" {
		return ExchangeDetail{}, errors.New("Exchange detail projection is incomplete")
	}
	type orderedAttempt struct {
		sequence int64
		id       string
	}
	ordered := make([]orderedAttempt, 0, len(egressPage.Items))
	seenAttempts := make(map[string]struct{}, len(egressPage.Items))
	proxyIDs := make(map[string]struct{})
	for _, item := range egressPage.Items {
		parent := item.Attempt.Parent()
		if parent.ExchangeID != record.SubjectID {
			return ExchangeDetail{}, errors.New(
				"Exchange detail contains unrelated egress evidence",
			)
		}
		if parent.Kind == egressaudit.ParentUpstreamAttempt {
			if _, seen := seenAttempts[parent.ID]; !seen {
				seenAttempts[parent.ID] = struct{}{}
				ordered = append(ordered, orderedAttempt{
					sequence: item.Sequence,
					id:       parent.ID,
				})
			}
		}
		if proxyID := item.Attempt.Decision().ProxyID; proxyID != "" {
			proxyIDs[proxyID] = struct{}{}
		}
	}
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left].sequence < ordered[right].sequence
	})
	result := record.ReasonCode
	if result == "" {
		result = string(record.Status)
	}
	detail := ExchangeDetail{
		ID:       record.SubjectID,
		AccessID: record.AccessID,
		Status:   string(record.Status),
		ProcessingTrace: ExchangeProcessingTrace{
			PluginRunIDs: make([]string, 0),
			AttemptIDs:   make([]string, 0, len(ordered)),
			Result:       result,
		},
	}
	for _, attempt := range ordered {
		detail.ProcessingTrace.AttemptIDs = append(
			detail.ProcessingTrace.AttemptIDs,
			attempt.id,
		)
	}
	if len(proxyIDs) == 1 {
		for proxyID := range proxyIDs {
			detail.ProcessingTrace.EgressProxyID = proxyID
		}
	}
	return detail, nil
}

func activityCursor(sequence int64) (string, error) {
	if sequence <= 0 {
		return "", errInvalidActivityQuery
	}
	payload := activityCursorPrefix + strconv.FormatInt(sequence, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)), nil
}

func parseActivityCursor(value string) (int64, error) {
	if value == "" || len(value) > 128 || strings.ContainsAny(value, " \t\r\n=") {
		return 0, errInvalidActivityQuery
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, errInvalidActivityQuery
	}
	payload, found := strings.CutPrefix(string(decoded), activityCursorPrefix)
	if !found {
		return 0, errInvalidActivityQuery
	}
	sequence, err := strconv.ParseInt(payload, 10, 64)
	if err != nil || sequence <= 0 {
		return 0, errInvalidActivityQuery
	}
	canonical, err := activityCursor(sequence)
	if err != nil || canonical != value {
		return 0, errInvalidActivityQuery
	}
	return sequence, nil
}

func (handler *Handler) getExchange(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	exchangeID := request.PathValue("exchangeId")
	record, err := handler.activities.GetExchange(request.Context(), exchangeID)
	switch {
	case errors.Is(err, activity.ErrExchangeNotFound):
		writeProblem(writer, http.StatusNotFound, ReasonExchangeNotFound)
		return
	case errors.Is(err, activity.ErrInvalidEvent):
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	case err != nil:
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	egressPage, err := handler.egress.List(
		request.Context(),
		egressaudit.PageRequest{
			Limit:      egressaudit.MaxPageLimit,
			ExchangeID: exchangeID,
		},
	)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	detail, err := exchangeDetailOf(record, egressPage)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	writeJSON(writer, http.StatusOK, detail)
}
