package desktopcontrol

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/vibe-agi/vibermate/internal/recording"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

// A lookup is bounded and resumable. Opening a tool never hydrates the whole
// conversation or relies on whichever history pages the UI happened to load.
type toolResultCursor struct {
	Version  int      `json:"version,omitempty"`
	Activity string   `json:"activity,omitempty"`
	Origin   string   `json:"origin"`
	Call     string   `json:"call"`
	Before   int64    `json:"before"`
	Exchange string   `json:"exchange,omitempty"`
	Content  string   `json:"content,omitempty"`
	Pending  []string `json:"pending,omitempty"`
}
type toolResultEntry struct {
	ExchangeID string                `json:"exchangeId"`
	Block      exchangecontent.Block `json:"block"`
}
type toolResultPage struct {
	Schema string            `json:"schema"`
	Origin string            `json:"originExchangeId"`
	Call   string            `json:"callId"`
	Items  []toolResultEntry `json:"items"`
	Next   string            `json:"nextCursor,omitempty"`
}

func (handler *Handler) getToolResults(writer http.ResponseWriter, request *http.Request) {
	values, queryErr := url.ParseQuery(request.URL.RawQuery)
	call := values.Get("callId")
	if queryErr != nil || request.ContentLength != 0 || len(request.TransferEncoding) != 0 ||
		len(call) == 0 || len(call) > 512 || strings.ContainsAny(call, "\r\n\x00") ||
		len(values["callId"]) != 1 || len(values) > 2 || (values.Has("cursor") && len(values["cursor"]) != 1) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	for key := range values {
		if key != "callId" && key != "cursor" {
			writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
			return
		}
	}
	check, err := inspectionResponseBoundary(request.Context(), handler.activities, handler.contents)
	if err != nil {
		writeInspectionProblem(writer, err)
		return
	}
	var expiry time.Time
	id := request.PathValue("exchangeId")
	origin, err := handler.activities.GetExchange(request.Context(), id)
	if err != nil && !errors.Is(err, activity.ErrExchangeNotFound) {
		writeInspectionProblem(writer, err)
		return
	}
	if err != nil || origin.Conversation == nil {
		writeProblem(writer, http.StatusNotFound, ReasonExchangeNotFound)
		return
	}
	cursor := toolResultCursor{Origin: id, Call: call}
	if encoded := values.Get("cursor"); encoded != "" {
		if len(encoded) > 128<<10 {
			writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
			return
		}
		data, e := base64.RawURLEncoding.DecodeString(encoded)
		if e != nil {
			writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
			return
		}
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF ||
			cursor.Origin != id || cursor.Call != call || cursor.Before < 0 || len(cursor.Pending) > 64 || len(cursor.Activity) > 512 ||
			(handler.activityInspection && (cursor.Version != 2 || cursor.Before != 0 || cursor.Activity == "")) ||
			(!handler.activityInspection && (cursor.Version != 0 || cursor.Activity != "")) ||
			((cursor.Exchange == "") != (cursor.Content == "" && len(cursor.Pending) == 0)) {
			writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
			return
		}
		for _, pending := range cursor.Pending {
			if pending == "" || len(pending) > exchangecontent.MaxPageCursorBytes {
				writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
				return
			}
		}
	}
	result := toolResultPage{Schema: "vibermate-tool-results/v1", Origin: id, Call: call, Items: []toolResultEntry{}}
	save := func(next toolResultCursor) error {
		if handler.activityInspection {
			next.Version = 2
			if next.Activity == "" || next.Before != 0 {
				return recording.ErrInspectionCursor
			}
		}
		data, err := json.Marshal(next)
		if err != nil {
			return err
		}
		encoded := base64.RawURLEncoding.EncodeToString(data)
		if len(encoded) > 128<<10 {
			return recording.ErrInspectionUnavailable
		}
		result.Next = encoded
		return nil
	}
	// Revalidate every resumed Exchange against the origin's exact actor/session.
	matches := func(candidate activity.Record) bool {
		return candidate.Conversation != nil && candidate.Conversation.ProjectionID == origin.Conversation.ProjectionID &&
			candidate.Conversation.Actor == origin.Conversation.Actor && !candidate.OccurredAt.Before(origin.OccurredAt)
	}
	checked := map[string]bool{}
	scan := func(candidate activity.Record, state toolResultCursor) error {
		if !matches(candidate) {
			return exchangecontent.ErrInvalidEvidence
		}
		var messages []exchangecontent.Message
		var blocks []exchangecontent.Block
		if len(state.Pending) > 0 {
			pending := state.Pending[0]
			state.Pending = state.Pending[1:]
			page, e := handler.contents.GetContentPage(request.Context(), candidate.SubjectID, pending)
			if e != nil {
				return e
			}
			includeInspectionExpiry(&expiry, page.InspectionExpiresAt)
			if !contentReferencesMatch(candidate, page.ExchangeID, page.Parent, page.Frozen) {
				return exchangecontent.ErrInvalidEvidence
			}
			if page.Kind == "text" || page.Kind == "arguments" {
				if page.BlockKind == "tool_result" && page.CallID == call {
					// A long result remains an explicitly loadable page, not a
					// truncated result or evidence that the tool is still running.
					result.Items = append(result.Items, toolResultEntry{ExchangeID: candidate.SubjectID, Block: exchangecontent.Block{
						Kind: "deferred", Availability: exchangecontent.AvailabilityRecorded,
						Deferred: &exchangecontent.DeferredContent{ExchangeID: candidate.SubjectID, Cursor: pending, EstimatedBytes: page.Total},
					}})
				}
			} else if page.Kind == "message" {
				blocks = page.Blocks
				if page.NextCursor != "" {
					state.Pending = append([]string{page.NextCursor}, state.Pending...)
				}
			} else {
				return exchangecontent.ErrInvalidEvidence
			}
		} else if state.Content == "" {
			projection, e := handler.contents.GetPagedProjection(request.Context(), candidate.SubjectID, exchangecontent.RequestViewFull)
			if errors.Is(e, exchangecontent.ErrNotFound) {
				return nil
			}
			if e != nil {
				return e
			}
			includeInspectionExpiry(&expiry, projection.ExpiresAt)
			if !contentReferencesMatch(candidate, projection.ExchangeID, projection.Parent, projection.Frozen) {
				return exchangecontent.ErrInvalidEvidence
			}
			messages = projection.Request.Messages
			if projection.Page != nil {
				state.Content = projection.Page.RequestNextCursor
			}
		} else {
			page, e := handler.contents.GetContentPage(request.Context(), candidate.SubjectID, state.Content)
			if e != nil {
				return e
			}
			includeInspectionExpiry(&expiry, page.InspectionExpiresAt)
			if !contentReferencesMatch(candidate, page.ExchangeID, page.Parent, page.Frozen) {
				return exchangecontent.ErrInvalidEvidence
			}
			if page.Kind != "request" {
				return exchangecontent.ErrInvalidEvidence
			}
			messages, state.Content = page.Messages, page.NextCursor
		}
		for _, message := range messages {
			blocks = append(blocks, message.Blocks...)
		}
		var deferred []string
		for _, block := range blocks {
			if block.Kind == "tool_result" && block.CallID == call && len(result.Items) < exchangecontent.PageMessageLimit {
				result.Items = append(result.Items, toolResultEntry{ExchangeID: candidate.SubjectID, Block: block})
			} else if block.Deferred != nil {
				deferred = append(deferred, block.Deferred.Cursor)
			}
		}
		if len(result.Items) > 0 {
			return nil
		}
		state.Pending = append(deferred, state.Pending...)
		if len(state.Pending) > 64 {
			return exchangecontent.ErrInvalidEvidence
		}
		if state.Content != "" || len(state.Pending) > 0 {
			state.Origin, state.Call, state.Exchange = id, call, candidate.SubjectID
			if handler.activityInspection {
				if candidate.InspectionCursor != "" {
					state.Activity = candidate.InspectionCursor
				}
			} else {
				state.Before = candidate.Sequence
			}
			return save(state)
		}
		return nil
	}
	if cursor.Exchange != "" {
		candidate, e := handler.activities.GetExchange(request.Context(), cursor.Exchange)
		if e != nil && !errors.Is(e, activity.ErrExchangeNotFound) {
			writeInspectionProblem(writer, e)
			return
		}
		if e != nil || !matches(candidate) {
			writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
			return
		}
		if err := scan(candidate, cursor); err != nil {
			writeInspectionProblem(writer, err)
			return
		}
		if result.Next != "" || len(result.Items) > 0 {
			writeInspectionJSON(writer, result, handler.activityExpiryCheck(check, expiry))
			return
		}
		checked[candidate.SubjectID] = true
	}
	page, e := handler.activities.List(request.Context(), activity.PageRequest{Limit: 12, BeforeSequence: cursor.Before, Cursor: cursor.Activity, ConversationProjectionID: origin.Conversation.ProjectionID})
	if e != nil {
		writeInspectionProblem(writer, e)
		return
	}
	for _, candidate := range page.Items {
		if !isExchangeActivity(candidate.Kind) || checked[candidate.SubjectID] || !matches(candidate) {
			continue
		}
		checked[candidate.SubjectID] = true
		if err := scan(candidate, toolResultCursor{}); err != nil {
			writeInspectionProblem(writer, err)
			return
		}
		if result.Next != "" || len(result.Items) > 0 {
			break
		}
	}
	if result.Next == "" && len(result.Items) == 0 && (page.NextBeforeSequence > 0 || page.NextCursor != "") {
		if err := save(toolResultCursor{Origin: id, Call: call, Before: page.NextBeforeSequence, Activity: page.NextCursor}); err != nil {
			writeInspectionProblem(writer, err)
			return
		}
	}
	writeInspectionJSON(writer, result, handler.activityExpiryCheck(check, expiry))
}
