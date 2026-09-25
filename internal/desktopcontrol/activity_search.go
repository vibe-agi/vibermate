package desktopcontrol

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
)

const activitySearchCursorBytes = 25

type EvidenceSearchHit struct {
	Activity ActivitySummary        `json:"activity"`
	Context  activity.SearchContext `json:"context"`
	Matches  []string               `json:"matches"`
}

type EvidenceSearchPage struct {
	Items      []EvidenceSearchHit `json:"items"`
	NextCursor string              `json:"nextCursor,omitempty"`
}

func (handler *Handler) searchActivities(
	writer http.ResponseWriter,
	request *http.Request,
) {
	query, err := parseActivitySearchQuery(request.URL.RawQuery)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	page, err := handler.activities.Search(request.Context(), query)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	view, err := evidenceSearchPageOf(page)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	if err := handler.enrichSearchActivities(request.Context(), page, &view); err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	if page.NextBeforeSequence != 0 {
		view.NextCursor, err = activitySearchCursor(page.NextBeforeSequence, query)
		if err != nil {
			writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
			return
		}
	}
	writeJSON(writer, http.StatusOK, view)
}

func (handler *Handler) enrichSearchActivities(
	ctx context.Context,
	page activity.SearchPage,
	view *EvidenceSearchPage,
) error {
	records := activity.Page{Items: make([]activity.Record, len(page.Items))}
	projected := ActivityPage{Items: make([]ActivitySummary, len(view.Items))}
	for index, hit := range page.Items {
		records.Items[index] = hit.Record
		projected.Items[index] = view.Items[index].Activity
	}
	if err := handler.attachActivityIdentities(ctx, records, &projected); err != nil {
		return err
	}
	_ = handler.attachActivityRequestPreviews(ctx, &projected)
	for index := range view.Items {
		view.Items[index].Activity = projected.Items[index]
	}
	return nil
}

func parseActivitySearchQuery(rawQuery string) (activity.SearchQuery, error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return activity.SearchQuery{}, errInvalidActivityQuery
	}
	allowed := map[string]bool{
		"cursor": true, "limit": true, "q": true, "environmentId": true,
		"accountId": true, "model": true, "tool": true, "status": true,
		"reason": true, "from": true, "until": true,
	}
	for name, entries := range values {
		if !allowed[name] || len(entries) != 1 {
			return activity.SearchQuery{}, errInvalidActivityQuery
		}
	}
	query := activity.SearchQuery{
		Limit:         50,
		Text:          values.Get("q"),
		EnvironmentID: values.Get("environmentId"),
		AccountID:     values.Get("accountId"),
		Model:         values.Get("model"),
		Tool:          values.Get("tool"),
		Status:        activity.Status(values.Get("status")),
		Reason:        values.Get("reason"),
	}
	if values.Has("limit") {
		query.Limit, err = strconv.Atoi(values.Get("limit"))
		if err != nil {
			return activity.SearchQuery{}, errInvalidActivityQuery
		}
	}
	if values.Has("from") != values.Has("until") {
		return activity.SearchQuery{}, errInvalidActivityQuery
	}
	if values.Has("from") {
		query.OccurredAtOrAfter, err = time.Parse(time.RFC3339Nano, values.Get("from"))
		if err == nil {
			query.OccurredBefore, err = time.Parse(time.RFC3339Nano, values.Get("until"))
		}
		if err != nil {
			return activity.SearchQuery{}, errInvalidActivityQuery
		}
		query.OccurredAtOrAfter = query.OccurredAtOrAfter.UTC().Truncate(time.Millisecond)
		query.OccurredBefore = query.OccurredBefore.UTC().Truncate(time.Millisecond)
	}
	if values.Has("cursor") {
		query.BeforeSequence, err = parseActivitySearchCursor(values.Get("cursor"), query)
		if err != nil {
			return activity.SearchQuery{}, errInvalidActivityQuery
		}
	}
	if query.Validate() != nil {
		return activity.SearchQuery{}, errInvalidActivityQuery
	}
	return query, nil
}

func activitySearchCursor(sequence int64, query activity.SearchQuery) (string, error) {
	if sequence <= 0 {
		return "", errInvalidActivityQuery
	}
	payload := make([]byte, activitySearchCursorBytes)
	payload[0] = 1
	binary.BigEndian.PutUint64(payload[1:9], uint64(sequence))
	digest := activitySearchDigest(query)
	copy(payload[9:], digest[:16])
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func parseActivitySearchCursor(
	value string,
	query activity.SearchQuery,
) (int64, error) {
	payload, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(payload) != activitySearchCursorBytes || payload[0] != 1 ||
		base64.RawURLEncoding.EncodeToString(payload) != value {
		return 0, errInvalidActivityQuery
	}
	digest := activitySearchDigest(query)
	if subtle.ConstantTimeCompare(payload[9:], digest[:16]) != 1 {
		return 0, errInvalidActivityQuery
	}
	sequence := binary.BigEndian.Uint64(payload[1:9])
	if sequence == 0 || sequence > uint64(^uint64(0)>>1) {
		return 0, errInvalidActivityQuery
	}
	return int64(sequence), nil
}

func activitySearchDigest(query activity.SearchQuery) [sha256.Size]byte {
	fields := []string{
		query.Text,
		query.EnvironmentID,
		query.AccountID,
		query.Model,
		query.Tool,
		string(query.Status),
		query.Reason,
	}
	if !query.OccurredAtOrAfter.IsZero() {
		fields = append(
			fields,
			strconv.FormatInt(query.OccurredAtOrAfter.UnixMilli(), 10),
			strconv.FormatInt(query.OccurredBefore.UnixMilli(), 10),
		)
	}
	return sha256.Sum256([]byte(strings.Join(fields, "\x00")))
}

func evidenceSearchPageOf(page activity.SearchPage) (EvidenceSearchPage, error) {
	records := make([]activity.Record, len(page.Items))
	for index, hit := range page.Items {
		if hit.Validate() != nil {
			return EvidenceSearchPage{}, errors.New("Activity search result is invalid")
		}
		records[index] = hit.Record
	}
	activities, err := activityPageOf(activity.Page{Items: records})
	if err != nil {
		return EvidenceSearchPage{}, err
	}
	view := EvidenceSearchPage{Items: make([]EvidenceSearchHit, len(page.Items))}
	for index, hit := range page.Items {
		view.Items[index] = EvidenceSearchHit{
			Activity: activities.Items[index],
			Context:  hit.Context,
			Matches:  append([]string(nil), hit.Matches...),
		}
	}
	return view, nil
}
