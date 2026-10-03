package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/originidentity"
)

type nativeBindingRow struct {
	session                                                       nativetool.ID
	sessionRevision, generation, owner, assignment, routeRevision uint64
	catalog                                                       []byte
	route, name                                                   string
	digest, request                                               nativetool.Digest
	parent                                                        *nativetool.ID
	server                                                        []byte
}

func loadNativeBinding(ctx context.Context, tx *sql.Tx, id nativetool.ID) (nativeBindingRow, error) {
	var b nativeBindingRow
	var session string
	var sr, generation, owner, assignment, rr int64
	var parent sql.NullString
	var request, params, group, digest, replay []byte
	err := tx.QueryRowContext(ctx, `SELECT session_id,session_revision,catalog_generation,catalog_digest,route_id,route_revision,owner_epoch,assignment_revision,request_digest,params_digest,group_digest,binding_digest,replay_key,parent_operation_id,server_request_digest,tool_name FROM native_tool_bindings WHERE operation_id=?`, id).Scan(&session, &sr, &generation, &b.catalog, &b.route, &rr, &owner, &assignment, &request, &params, &group, &digest, &replay, &parent, &b.server, &b.name)
	if errors.Is(err, sql.ErrNoRows) {
		return b, nativetool.ErrNotFound
	}
	if err != nil {
		return b, err
	}
	sid, err := nativetool.NewID(session)
	_, routeErr := environment.NewClientToolRouteID(b.route)
	if err != nil || routeErr != nil || sr <= 0 || generation < 0 || owner <= 0 || assignment <= 0 || rr <= 0 || len(request) != 32 || len(params) != 32 || len(group) != 32 || len(digest) != 32 || replay != nil && len(replay) != 32 || (generation == 0) != (b.catalog == nil) || b.catalog != nil && len(b.catalog) != 32 || parent.Valid != (b.server != nil) || b.server != nil && len(b.server) != 32 {
		return b, nativetool.ErrInvalid
	}
	if parent.Valid {
		parsed, err := nativetool.NewID(parent.String)
		if err != nil {
			return b, nativetool.ErrInvalid
		}
		b.parent = &parsed
	}
	if b.name != "" && (len(b.name) > 256 || strings.IndexFunc(b.name, func(c rune) bool {
		return !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == ':' || c == '/' || c == '-')
	}) >= 0) {
		return b, nativetool.ErrInvalid
	}
	b.session = sid
	b.sessionRevision = uint64(sr)
	b.generation = uint64(generation)
	b.owner = uint64(owner)
	b.assignment = uint64(assignment)
	b.routeRevision = uint64(rr)
	copy(b.digest[:], digest)
	copy(b.request[:], request)
	return b, nil
}
func loadNativeOperation(ctx context.Context, tx *sql.Tx, id nativetool.ID) (nativetool.Operation, error) {
	var op nativetool.Operation
	var kind, capture, env, recording, requestKind, connection, method string
	var er, revision, created, expires, retention int64
	var digest []byte
	var completed, in, out sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT capture_kind,capture_id,environment_id,environment_revision,environment_digest,connection_id,target_origin,method,rpc_kind,revision,state,decision_source,recording,retention_days,created_at_unix_ms,metadata_expires_at_unix_ms,completed_at_unix_ms,bytes_in,bytes_out,terminal_reason FROM native_tool_operations WHERE operation_id=?`, id).Scan(&kind, &capture, &env, &er, &digest, &connection, &op.TargetOrigin, &method, &requestKind, &revision, &op.State, &op.DecisionSource, &recording, &retention, &created, &expires, &completed, &in, &out, &op.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return op, nativetool.ErrNotFound
	}
	if err != nil {
		return op, err
	}
	c, err := captureidentity.New(captureidentity.Kind(kind), capture)
	eid, eerr := environment.NewEnvironmentID(env)
	origin, oerr := originidentity.ParseProviderOrigin(op.TargetOrigin)
	if retention < 0 || retention > 3650 {
		return op, nativetool.ErrInvalid
	}
	op.Recording = environment.ContentRecordingPolicy{Mode: environment.ContentRecordingMode(recording), RetentionDays: uint16(retention)}
	op.Kind = nativetool.RequestKind(requestKind)
	terminal := op.State == "denied" || op.State == "completed" || op.State == "failed" || op.State == "canceled" || op.State == "uncertain"
	validState := terminal || op.State == "received" || op.State == "validated" || op.State == "awaiting_decision" || op.State == "started"
	if err != nil || eerr != nil || oerr != nil || origin.Scheme() != "https" || origin.BasePath() != "" || !nativeID(id) || er <= 0 || revision <= 0 || len(digest) != 32 || created <= 0 || expires <= created || !validState || terminal != completed.Valid || completed.Valid && completed.Int64 < created || op.Kind.Validate() != nil || op.Recording.Validate() != nil || len(connection) == 0 || len(connection) > 512 || (method != "POST" && method != "GET" && method != "DELETE") || in.Valid && in.Int64 < 0 || out.Valid && out.Int64 < 0 || (op.DecisionSource != "none" && op.DecisionSource != "policy" && op.DecisionSource != "user") {
		return op, nativetool.ErrInvalid
	}
	if (!terminal && op.Reason != "") || terminal && op.Reason == "" {
		return op, nativetool.ErrInvalid
	}
	switch op.Reason {
	case "", "permission_off", "policy_strict", "approval_denied", "approval_expired", "revoked", "canceled", "transport_failed", "rpc_error", "execution_uncertain", "completed":
	default:
		return op, nativetool.ErrInvalid
	}
	op.ID = id
	op.Capture = c
	op.EnvironmentID = eid
	op.EnvironmentRevision = environment.Revision(er)
	copy(op.EnvironmentDigest[:], digest)
	op.Revision = uint64(revision)
	op.CreatedAt = time.UnixMilli(created).UTC()
	var eventRevision int64
	var eventKind string
	var eventAt int64
	if err := tx.QueryRowContext(ctx, `SELECT operation_revision,kind,occurred_at_unix_ms FROM native_tool_events WHERE operation_id=? ORDER BY operation_revision DESC LIMIT 1`, id).Scan(&eventRevision, &eventKind, &eventAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return op, nativetool.ErrInvalid
		}
		return op, err
	}
	if eventRevision != revision || eventAt < created {
		return op, nativetool.ErrInvalid
	}
	validEvent := map[string][]string{"received": {"received"}, "validated": {"validated", "allowed"}, "awaiting_decision": {"awaiting_decision"}, "denied": {"denied"}, "started": {"started", "progress"}, "completed": {"completed"}, "failed": {"failed"}, "canceled": {"canceled", "revoked"}, "uncertain": {"uncertain"}}
	matched := false
	for _, kind := range validEvent[op.State] {
		if kind == eventKind {
			matched = true
		}
	}
	if !matched {
		return op, nativetool.ErrInvalid
	}
	if completed.Valid {
		t := time.UnixMilli(completed.Int64).UTC()
		op.CompletedAt = &t
	}
	if in.Valid {
		v := in.Int64
		op.BytesIn = &v
	}
	if out.Valid {
		v := out.Int64
		op.BytesOut = &v
	}
	aggregate, exists, err := loadEnvironmentRevision(ctx, tx, eid, op.EnvironmentRevision)
	if err != nil {
		return op, err
	}
	if !exists || aggregate.ContentRecording != op.Recording {
		return op, nativetool.ErrInvalid
	}
	frozenDigest, err := environment.Digest(aggregate)
	if err != nil {
		return op, err
	}
	if frozenDigest != op.EnvironmentDigest {
		return op, nativetool.ErrInvalid
	}
	b, err := loadNativeBinding(ctx, tx, id)
	if err != nil && !errors.Is(err, nativetool.ErrNotFound) {
		return op, err
	}
	if err == nil {
		s, err := loadNativeSession(ctx, tx, b.session)
		if err != nil {
			return op, err
		}
		route := aggregate.ClientToolAccess
		if op.State == "received" || b.sessionRevision > s.revision || b.generation > s.generation || b.name != "" && op.Kind != nativetool.RequestToolsCall || op.Kind == nativetool.RequestToolsCall && b.generation == 0 || s.capture != op.Capture || s.environment != eid || s.environmentRevision != op.EnvironmentRevision || s.digest != op.EnvironmentDigest || s.owner != b.owner || s.assignment != b.assignment || s.origin != op.TargetOrigin || b.route != s.route || b.routeRevision != s.routeRevision || route == nil || !route.Enabled || route.Route == nil || route.Route.ID.String() != b.route || uint64(route.Route.Revision) != b.routeRevision || route.Route.TargetOrigin.String() != op.TargetOrigin || (op.Kind == nativetool.RequestServerReply) != (b.parent != nil) {
			return op, nativetool.ErrInvalid
		}
		op.BindingDigest = &b.digest
		op.ToolName = b.name
		op.CredentialOwner = string(route.Route.CredentialOwner)
	} else if op.State != "received" && op.State != "denied" && op.State != "canceled" {
		return op, nativetool.ErrInvalid
	}
	var approval string
	err = tx.QueryRowContext(ctx, `SELECT approval_id FROM native_tool_approvals WHERE operation_id=?`, id).Scan(&approval)
	if err == nil {
		a, err := nativetool.NewID(approval)
		if err != nil {
			return op, nativetool.ErrInvalid
		}
		record, err := loadNativeApproval(ctx, tx, a)
		if err != nil {
			return op, err
		}
		if record.OperationID != id || op.BindingDigest == nil || record.BindingDigest != *op.BindingDigest {
			return op, nativetool.ErrInvalid
		}
		op.ApprovalID = &a
	} else if !errors.Is(err, sql.ErrNoRows) {
		return op, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT part,body_digest,expires_at_unix_ms FROM native_tool_contents WHERE operation_id=? ORDER BY part`, id)
	if err != nil {
		return op, err
	}
	defer rows.Close()
	for rows.Next() {
		var part string
		var digest []byte
		var deadline int64
		if err := rows.Scan(&part, &digest, &deadline); err != nil {
			return op, err
		}
		if op.Recording.Mode != environment.ContentRecordingFull || op.Kind != nativetool.RequestToolsCall || len(digest) != 32 || deadline <= created {
			return op, nativetool.ErrInvalid
		}
		descriptor := &nativetool.ContentDescriptor{ExpiresAt: time.UnixMilli(deadline).UTC()}
		copy(descriptor.Digest[:], digest)
		switch part {
		case "tool_arguments":
			op.Arguments = descriptor
		case "tool_result":
			op.Result = descriptor
		default:
			return op, nativetool.ErrInvalid
		}
	}
	if err := rows.Err(); err != nil {
		return op, err
	}
	return op, nil
}
func loadNativeApproval(ctx context.Context, tx *sql.Tx, id nativetool.ID) (nativetool.Approval, error) {
	var a nativetool.Approval
	var operation, key string
	var digest []byte
	var revision, created, expires int64
	var resolved, consumed sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT operation_id,binding_digest,revision,state,created_at_unix_ms,expires_at_unix_ms,resolved_at_unix_ms,idempotency_key,consumed_at_unix_ms FROM native_tool_approvals WHERE approval_id=?`, id).Scan(&operation, &digest, &revision, &a.State, &created, &expires, &resolved, &key, &consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return a, nativetool.ErrNotFound
	}
	if err != nil {
		return a, err
	}
	oid, err := nativetool.NewID(operation)
	if err != nil || !nativeID(id) || len(digest) != 32 || revision <= 0 || created <= 0 || expires <= created || (a.State == "pending") != (!resolved.Valid) || resolved.Valid && resolved.Int64 < created || consumed.Valid && (a.State != "allowed" || consumed.Int64 < resolved.Int64) {
		return a, nativetool.ErrInvalid
	}
	switch a.State {
	case "pending":
		if key != "" {
			return a, nativetool.ErrInvalid
		}
	case "allowed", "denied":
		if len(key) < 16 || len(key) > 512 {
			return a, nativetool.ErrInvalid
		}
	case "canceled", "expired":
		if key != "" {
			return a, nativetool.ErrInvalid
		}
	default:
		return a, nativetool.ErrInvalid
	}
	a.ID = id
	a.OperationID = oid
	copy(a.BindingDigest[:], digest)
	a.Revision = uint64(revision)
	a.CreatedAt = time.UnixMilli(created).UTC()
	a.ExpiresAt = time.UnixMilli(expires).UTC()
	if consumed.Valid {
		t := time.UnixMilli(consumed.Int64).UTC()
		a.ConsumedAt = &t
	}
	return a, nil
}
func (r *nativeToolRepository) Get(ctx context.Context, id nativetool.ID) (nativetool.Operation, error) {
	if !nativeID(id) {
		return nativetool.Operation{}, nativetool.ErrInvalid
	}
	var op nativetool.Operation
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		op, err = loadNativeOperation(ctx, tx, id)
		return err
	})
	if err != nil {
		return nativetool.Operation{}, err
	}
	return op, nil
}
func (r *nativeToolRepository) GetApproval(ctx context.Context, id nativetool.ID) (nativetool.Approval, error) {
	if !nativeID(id) {
		return nativetool.Approval{}, nativetool.ErrInvalid
	}
	var a nativetool.Approval
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		a, err = loadNativeApproval(ctx, tx, id)
		if err != nil {
			return err
		}
		b, err := loadNativeBinding(ctx, tx, a.OperationID)
		if err != nil {
			return err
		}
		if b.digest != a.BindingDigest {
			return nativetool.ErrInvalid
		}
		op, err := loadNativeOperation(ctx, tx, a.OperationID)
		if err != nil {
			return err
		}
		if a.State == "pending" {
			return nativeApprovalAuthority(ctx, tx, op, time.Now().UTC())
		}
		return nil
	})
	if err != nil {
		return nativetool.Approval{}, err
	}
	return a, nil
}
func (r *nativeToolRepository) Page(ctx context.Context, in nativetool.PageRequest) (nativetool.Page, error) {
	if in.Capture.Validate() != nil || in.AfterSequence < 0 || in.Limit < 1 || in.Limit > 200 {
		return nativetool.Page{}, nativetool.ErrInvalid
	}
	page := nativetool.Page{Items: []nativetool.Operation{}}
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// Fetch only references first, then close the cursor before loading the
		// current projection. Late events for old operations are intentionally visible.
		rows, err := tx.QueryContext(ctx, `SELECT e.sequence,e.operation_id,e.operation_revision,e.kind,e.occurred_at_unix_ms FROM native_tool_events e JOIN native_tool_operations o ON o.operation_id=e.operation_id WHERE o.capture_kind=? AND o.capture_id=? AND e.sequence>? ORDER BY e.sequence LIMIT ?`, in.Capture.Kind, in.Capture.ID, in.AfterSequence, in.Limit)
		if err != nil {
			return err
		}
		type ref struct {
			sequence, revision, occurred int64
			id, kind                     string
		}
		refs := []ref{}
		for rows.Next() {
			var item ref
			if err := rows.Scan(&item.sequence, &item.id, &item.revision, &item.kind, &item.occurred); err != nil {
				rows.Close()
				return err
			}
			refs = append(refs, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, item := range refs {
			id, err := nativetool.NewID(item.id)
			if err != nil || item.sequence <= 0 || item.revision <= 0 || item.occurred <= 0 {
				return nativetool.ErrInvalid
			}
			op, err := loadNativeOperation(ctx, tx, id)
			if err != nil {
				return err
			}
			if op.Capture != in.Capture || uint64(item.revision) > op.Revision {
				return nativetool.ErrInvalid
			}
			switch item.kind {
			case "received", "validated", "awaiting_decision", "allowed", "denied", "started", "progress", "completed", "failed", "canceled", "uncertain", "revoked":
			default:
				return nativetool.ErrInvalid
			}
			candidate := nativetool.Page{Items: append(append([]nativetool.Operation{}, page.Items...), op), NextSequence: item.sequence}
			encoded, err := json.Marshal(candidate)
			if err != nil {
				return err
			}
			if len(encoded) > 128<<10 {
				break
			}
			page = candidate
		}
		return nil
	})
	if err != nil {
		return nativetool.Page{}, err
	}
	return page, nil
}
