package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/recording"
)

// RepairConversation modifies only the exact surviving current Activity root
// selected by inspection. The lifecycle owner excludes maintenance and closes
// the source token around this transaction; no control connection is borrowed.
func (s *ObservationStore) RepairConversation(ctx context.Context, repair activity.ConversationRepair) error {
	r := repair.Root
	identity := repair.Identity.Clone()
	if !r.Current || r.Scope.Validate() != nil || r.Sequence <= 0 || r.ActivityID == "" || r.ExchangeID == "" || !r.Token.Control.Valid() || r.Token.Control.ArchiveEpoch != r.Scope.Fence.ArchiveEpoch || identity.Validate() != nil {
		return recording.ErrObservationDiscarded
	}
	operation, finish, err := s.store.operations.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	repo := s.store.activityRepo
	repo.identityMu.Lock()
	defer repo.identityMu.Unlock()
	tx, err := s.store.database.BeginTx(operation, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var source, layout string
	if err = tx.QueryRowContext(operation, `SELECT layout_id,archive_id FROM observation_storage_state WHERE singleton=1`).Scan(&layout, &source); err != nil {
		return err
	}
	if source != r.SourceID || layout != r.Token.Control.LayoutID {
		return recording.ErrObservationDiscarded
	}
	var managed, manual, display string
	var epoch, generation uint64
	err = tx.QueryRowContext(operation, `SELECT capture_run_id,manual_capture_id,source_display_name,recording_archive_epoch,recording_capture_generation FROM runtime_activities WHERE sequence=? AND activity_id=? AND subject_id=? AND kind IN('exchange.started','exchange.completed')`, r.Sequence, r.ActivityID, r.ExchangeID).Scan(&managed, &manual, &display, &epoch, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return recording.ErrObservationDiscarded
	}
	if err != nil {
		return err
	}
	if !recordingScopeMatches(r.Scope, managed, manual) || epoch != r.Scope.Fence.ArchiveEpoch || generation != r.Scope.Fence.CaptureGeneration {
		return recording.ErrObservationDiscarded
	}
	var old agentconversation.ClientIdentity
	var scope recording.Scope
	var protocols, attributes string
	var at int64
	err = tx.QueryRowContext(operation, `SELECT client_kind,session_id,session_resumable,actor_id,actor_label,actor_type,actor_is_subagent,provider_response_id,provider_message_id,protocol_ids_json,attributes_json,evidence_source,confidence,observed_at_unix_ms,recording_scope_kind,recording_scope_id,recording_archive_epoch,recording_capture_generation FROM runtime_exchange_agent_identities WHERE exchange_id=?`, r.ExchangeID).Scan(&old.Client, &old.SessionID, &old.SessionResumable, &old.ActorID, &old.ActorLabel, &old.ActorType, &old.ActorIsSubagent, &old.ProviderResponseID, &old.ProviderMessageID, &protocols, &attributes, &old.Source, &old.Confidence, &at, &scope.Capture.Kind, &scope.Capture.ID, &scope.Fence.ArchiveEpoch, &scope.Fence.CaptureGeneration)
	if err == nil {
		if scope != r.Scope {
			return recording.ErrObservationDiscarded
		}
		old.ObservedAt = fromUnixMillis(at)
		if json.Unmarshal([]byte(protocols), &old.ProtocolIDs) != nil || json.Unmarshal([]byte(attributes), &old.Attributes) != nil {
			return activity.ErrInvalidEvent
		}
		identity, _, err = agentconversation.MergeClientIdentity(old, identity)
		if err != nil {
			return errors.Join(activity.ErrInvalidEvent, err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	ref, err := agentconversation.Project(agentconversation.ProjectionInput{CaptureRunID: managed, ExchangeID: r.ExchangeID, SourceDisplayName: display, ClientIdentity: &identity})
	if err != nil {
		return err
	}
	p, err := json.Marshal(identity.ProtocolIDs)
	if err != nil {
		return err
	}
	a, err := json.Marshal(identity.Attributes)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(operation, `INSERT INTO runtime_exchange_agent_identities(exchange_id,client_kind,session_id,session_resumable,actor_id,actor_label,actor_type,actor_is_subagent,provider_response_id,provider_message_id,protocol_ids_json,attributes_json,evidence_source,confidence,observed_at_unix_ms,recording_scope_kind,recording_scope_id,recording_archive_epoch,recording_capture_generation) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(exchange_id) DO UPDATE SET client_kind=excluded.client_kind,session_id=excluded.session_id,session_resumable=excluded.session_resumable,actor_id=excluded.actor_id,actor_label=excluded.actor_label,actor_type=excluded.actor_type,actor_is_subagent=excluded.actor_is_subagent,provider_response_id=excluded.provider_response_id,provider_message_id=excluded.provider_message_id,protocol_ids_json=excluded.protocol_ids_json,attributes_json=excluded.attributes_json,evidence_source=excluded.evidence_source,confidence=excluded.confidence,observed_at_unix_ms=excluded.observed_at_unix_ms`, r.ExchangeID, identity.Client, identity.SessionID, identity.SessionResumable, identity.ActorID, identity.ActorLabel, identity.ActorType, identity.ActorIsSubagent, identity.ProviderResponseID, identity.ProviderMessageID, string(p), string(a), identity.Source, identity.Confidence, toUnixMillis(identity.ObservedAt), r.Scope.Capture.Kind, r.Scope.Capture.ID, epoch, generation)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(operation, `UPDATE runtime_activities SET conversation_projection_id=?,conversation_display_name=?,conversation_kind=?,conversation_evidence=?,conversation_actor=? WHERE sequence=? AND activity_id=? AND subject_id=? AND recording_archive_epoch=? AND recording_capture_generation=?`, ref.ProjectionID, ref.DisplayName, ref.Kind, ref.Evidence, ref.Actor, r.Sequence, r.ActivityID, r.ExchangeID, epoch, generation)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return recording.ErrObservationDiscarded
	}
	return tx.Commit()
}
