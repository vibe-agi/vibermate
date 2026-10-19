package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/recording"
)

func validActivityInspectionID(id string) bool {
	if id == "" || len(id) > activity.MaxIdentityBytes || !utf8.ValidString(id) || strings.TrimSpace(id) != id {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// Released writers allow8192 bounded evidence pairs per array. Guard the
// encoded envelope, count and each decoded field in SQL before materializing
// the JSON in Go; malformed/duplicate-key objects never become small scalars.
func inspectionIdentityJSON(column string) string {
	return `CASE WHEN length(CAST(` + column + ` AS BLOB))<=33554432 AND json_valid(` + column + `) THEN CASE WHEN json_type(` + column + `)='null' THEN ` + column + ` WHEN json_type(` + column + `)='array' AND json_array_length(` + column + `)<=8192 AND NOT EXISTS(SELECT 1 FROM json_each(` + column + `) j WHERE j.type<>'object' OR json_type(j.value,'$.name') IS NOT 'text' OR json_type(j.value,'$.value') IS NOT 'text' OR length(CAST(json_extract(j.value,'$.name') AS BLOB)) NOT BETWEEN 1 AND 128 OR length(CAST(json_extract(j.value,'$.value') AS BLOB)) NOT BETWEEN 1 AND 512 OR (SELECT count(*) FROM json_each(j.value))<>2) THEN ` + column + ` END END`
}
func inspectionIdentityColumns(source InspectionSource) string {
	columns := []string{}
	for _, column := range []string{"client_kind", "session_id", "session_resumable", "actor_id", "actor_label", "actor_type", "actor_is_subagent", "provider_response_id", "provider_message_id", "protocol_ids_json", "attributes_json", "evidence_source", "confidence", "observed_at_unix_ms"} {
		switch column {
		case "session_resumable", "actor_is_subagent", "observed_at_unix_ms":
			columns = append(columns, column)
		case "protocol_ids_json", "attributes_json":
			columns = append(columns, inspectionIdentityJSON(column))
		default:
			columns = append(columns, `CASE WHEN length(CAST(`+column+` AS BLOB))<=512 THEN `+column+` END`)
		}
	}
	if source.Current {
		columns = append(columns, `CASE WHEN length(CAST(recording_scope_kind AS BLOB))<=32 THEN recording_scope_kind END`, `CASE WHEN length(CAST(recording_scope_id AS BLOB))<=128 THEN recording_scope_id END`, "recording_archive_epoch", "recording_capture_generation")
	} else {
		columns = append(columns, "''", "''", "0", "0")
	}
	return strings.Join(columns, ",")
}
func scanInspectionIdentity(row activityScanner) (agentconversation.ClientIdentity, recording.Scope, error) {
	var identity agentconversation.ClientIdentity
	var scope recording.Scope
	var at int64
	var protocols, attributes string
	err := row.Scan(&identity.Client, &identity.SessionID, &identity.SessionResumable, &identity.ActorID, &identity.ActorLabel, &identity.ActorType, &identity.ActorIsSubagent, &identity.ProviderResponseID, &identity.ProviderMessageID, &protocols, &attributes, &identity.Source, &identity.Confidence, &at, &scope.Capture.Kind, &scope.Capture.ID, &scope.Fence.ArchiveEpoch, &scope.Fence.CaptureGeneration)
	if err != nil {
		return identity, scope, err
	}
	if !validActivityMillis(at) || json.Unmarshal([]byte(protocols), &identity.ProtocolIDs) != nil || json.Unmarshal([]byte(attributes), &identity.Attributes) != nil {
		return identity, scope, activity.ErrInvalidEvent
	}
	identity.ObservedAt = time.UnixMilli(at).UTC()
	if identity.Validate() != nil {
		return identity, scope, activity.ErrInvalidEvent
	}
	return identity, scope, nil
}
func (r *ActivityInspectionReader) GetConversationIdentity(ctx context.Context, id string) (resultValue agentconversation.ClientIdentity, resultErr error) {
	if r != nil {
		defer func(caller context.Context) { resultErr = r.catalog.classifyReadCancellation(caller, resultErr) }(ctx)
	}
	if !validActivityInspectionID(id) {
		return agentconversation.ClientIdentity{}, activity.ErrInvalidEvent
	}
	s, err := r.begin(ctx)
	if err != nil {
		return agentconversation.ClientIdentity{}, err
	}
	defer s.close()
	identity, err := r.identity(s, id)
	if err != nil {
		return agentconversation.ClientIdentity{}, err
	}
	s.endSQL()
	if err = r.Check(s.lease.ctx, s.token); err != nil {
		return agentconversation.ClientIdentity{}, err
	}
	return identity, nil
}
func (r *ActivityInspectionReader) identity(s *activityInspectionRead, id string) (agentconversation.ClientIdentity, error) {
	var result *agentconversation.ClientIdentity
	var owner *recording.Scope
	denied := false
	for ordinal, source := range s.lease.sources {
		identity, scope, err := scanInspectionIdentity(s.tx.QueryRowContext(s.lease.ctx, `SELECT `+inspectionIdentityColumns(source)+fmt.Sprintf(` FROM s%d.runtime_exchange_agent_identities WHERE exchange_id=?`, ordinal), id))
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return agentconversation.ClientIdentity{}, err
		}
		if !source.Current {
			var proven bool
			scope, proven, err = r.legacyIdentityScope(s, ordinal, id)
			if err != nil {
				return agentconversation.ClientIdentity{}, err
			}
			if !proven {
				continue
			}
		} else if scope.Fence == (recording.Fence{}) {
			continue
		}
		if !validOriginalRecordingScope(scope) {
			return agentconversation.ClientIdentity{}, ErrInspectionUnavailable
		}
		visible, err := s.allows(scope)
		if err != nil {
			return agentconversation.ClientIdentity{}, err
		}
		denied = denied || !visible
		if owner != nil && *owner != scope {
			return agentconversation.ClientIdentity{}, activity.ErrInvalidEvent
		}
		copyScope := scope
		owner = &copyScope
		if result == nil {
			copy := identity
			result = &copy
		} else {
			merged, _, err := agentconversation.MergeClientIdentity(*result, identity)
			if err != nil {
				return agentconversation.ClientIdentity{}, activity.ErrInvalidEvent
			}
			result = &merged
		}
	}
	if result == nil || denied {
		return agentconversation.ClientIdentity{}, activity.ErrExchangeNotFound
	}
	return *result, nil
}
func (r *ActivityInspectionReader) legacyIdentityScope(s *activityInspectionRead, ordinal int, id string) (scope recording.Scope, proven bool, err error) {
	rows, err := s.tx.QueryContext(s.lease.ctx, `SELECT `+activityInspectionColumns(ordinal, s.lease.sources[ordinal])+fmt.Sprintf(` FROM s%d.runtime_activities a WHERE a.subject_id=? AND a.kind IN('exchange.started','exchange.completed') LIMIT 3`, ordinal), id)
	if err != nil {
		return scope, false, err
	}
	kinds := map[activity.Kind]bool{}
	for rows.Next() {
		var record activity.Record
		var fence recording.Fence
		state := activityScanState{record: &record}
		if err = rows.Scan(append(state.targets(), &fence.ArchiveEpoch, &fence.CaptureGeneration)...); err != nil {
			break
		}
		record, err = state.finish()
		if err != nil {
			break
		}
		candidate := activityScopeOf(record, fence)
		if kinds[record.Kind] || proven && candidate != scope {
			err = activity.ErrInvalidEvent
			break
		}
		kinds[record.Kind] = true
		scope = candidate
		proven = true
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return recording.Scope{}, false, err
	}
	ref, err := loadStoredContentReferenceWithin(s.lease.ctx, newSourceContentQueries(s.tx, ordinal), id, time.UnixMilli(0).UTC(), &r.content.limits)
	if errors.Is(err, exchangecontent.ErrNotFound) {
		return scope, proven, nil
	}
	if err != nil {
		return recording.Scope{}, false, err
	}
	kind, owner := storedContentScope(ref.manifest.Parent)
	candidate := recording.Scope{Fence: recording.Fence{ArchiveEpoch: 1}}
	candidate.Capture.Kind = captureidentity.Kind(kind)
	candidate.Capture.ID = owner
	if kind != "" {
		candidate.Fence.CaptureGeneration = 1
	}
	if proven && candidate != scope {
		return recording.Scope{}, false, activity.ErrInvalidEvent
	}
	return candidate, true, nil
}

var _ activity.IdentityInspectionReader = (*ActivityInspectionReader)(nil)
