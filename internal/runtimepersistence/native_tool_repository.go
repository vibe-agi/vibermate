package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/recording"
)

type nativeToolRepository struct {
	legacyHistory   bool
	history         nativetool.HistoryRecorder
	recordingScopes recording.ScopeIssuer
	database        *sql.DB
	operations      *operationGate
	committer       transactionCommitter
	issuerMu        sync.Mutex
	issuer          *nativetool.StartReceiptIssuer
	streamLifetime  time.Duration
	streams         map[nativetool.StreamAuthority]*nativeObservedStream
	recovery        map[nativetool.ID]nativeRecovery
	streamOpened    map[nativetool.ID]time.Time
	callbacks       map[nativetool.CallbackAuthority]*nativeObservedCallback
	replies         map[nativetool.ID]nativetool.CallbackAuthority
}

var _ nativetool.LifecycleRepository = (*nativeToolRepository)(nil)
var _ nativetool.Repository = (*nativeToolRepository)(nil)

func (store *Store) NativeToolRepository() nativetool.Repository { return store.nativeTools }

func (store *Store) NativeLifecycleRepository() nativetool.LifecycleRepository {
	return store.nativeTools
}
func nativeRevision(v uint64) bool            { return v > 0 && v <= math.MaxInt64 }
func nativeTime(t time.Time) bool             { return !t.IsZero() && t.UnixMilli() > 0 }
func nativeID(id nativetool.ID) bool          { _, err := nativetool.NewID(string(id)); return err == nil }
func nativeDigest(d nativetool.Digest) []byte { return d[:] }
func nativeOptional(d nativetool.Digest, ok bool) any {
	if !ok {
		return nil
	}
	return nativeDigest(d)
}

// Every public method uses the Store's admission gate and one consistent writer
// transaction. No public repository method is called from inside that transaction.
func (r *nativeToolRepository) transaction(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	permit, err := r.operations.admit(ctx)
	if err != nil {
		return errors.Join(nativetool.ErrUnavailable, err)
	}
	defer permit.finish()
	tx, err := r.database.BeginTx(permit.context, nil)
	if err != nil {
		return errors.Join(nativetool.ErrUnavailable, err, context.Cause(permit.context))
	}
	defer tx.Rollback()
	workContext := permit.context
	var history *nativeHistoryBatch
	if r.history != nil {
		history = &nativeHistoryBatch{recorder: r.history, scopes: r.recordingScopes}
		workContext = context.WithValue(workContext, nativeHistoryKey{}, history)
	}
	defer history.discard(nativetool.HistoryRolledBack)
	if r.legacyHistory {
		workContext = context.WithValue(workContext, nativeLegacyHistoryKey{}, true)
	}
	if err = fn(workContext, tx); err != nil {
		// SQLite interruption is not necessarily wrapped as context.Canceled.
		// Preserve the actual cancellation/close cause at the owned boundary.
		err = errors.Join(err, context.Cause(permit.context))
		for _, known := range []error{nativetool.ErrInvalid, nativetool.ErrNotFound, nativetool.ErrConflict, nativetool.ErrDenied, nativetool.ErrReplay, nativetool.ErrUnavailable} {
			if errors.Is(err, known) {
				return err
			}
		}
		return errors.Join(nativetool.ErrUnavailable, err)
	}
	if err = r.committer.Commit(tx); err != nil {
		history.discard(nativetool.HistoryCommitUnknown)
		return errors.Join(nativetool.ErrUnavailable, err, context.Cause(permit.context))
	}
	history.publish(permit.context)
	return nil
}
func nativeCapture(ctx context.Context, tx *sql.Tx, c captureidentity.Reference, now time.Time) error {
	if c.Validate() != nil {
		return nativetool.ErrInvalid
	}
	var state string
	var expires sql.NullInt64
	query := `SELECT state,expires_at_unix_ms FROM manual_captures WHERE capture_id=?`
	if c.Kind == captureidentity.KindManagedRun {
		query = `SELECT state,expires_at_unix_ms FROM capture_runs WHERE run_id=?`
	}
	if err := tx.QueryRowContext(ctx, query, c.ID).Scan(&state, &expires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nativetool.ErrNotFound
		}
		return err
	}
	valid := state == "active"
	if c.Kind == captureidentity.KindManagedRun {
		valid = state == "created" || state == "attached"
	}
	if !valid || expires.Valid && expires.Int64 <= now.UnixMilli() {
		return nativetool.ErrDenied
	}
	if c.Kind == captureidentity.KindManagedRun {
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM capture_runs WHERE run_id=? AND `+captureRunIdentityActive, c.ID, now.UnixMilli()).Scan(&active); err != nil {
			return err
		}
		if active != 1 {
			return nativetool.ErrDenied
		}
	} else {
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM manual_captures c WHERE capture_id=? AND (owner_kind='local_installation' OR EXISTS(SELECT 1 FROM proxy_client_bindings p WHERE p.binding_id=c.owner_id AND p.state='active'))`, c.ID).Scan(&active); err != nil {
			return err
		}
		if active != 1 {
			return nativetool.ErrDenied
		}
	}
	return nil
}

type nativeScope struct {
	recordingScope  recording.Scope
	revision, owner uint64
	state           string
	barrier         bool
	expires         time.Time
}

func loadNativeScope(ctx context.Context, tx *sql.Tx, c captureidentity.Reference) (nativeScope, error) {
	return loadNativeScopeFacts(ctx, tx, c, false)
}
func loadNativeScopeFacts(ctx context.Context, tx *sql.Tx, c captureidentity.Reference, receiving bool) (nativeScope, error) {
	var s nativeScope
	var rev, owner, expires, archiveEpoch, captureGeneration int64
	var barrier int
	query := `SELECT revision,owner_epoch,state,replay_barrier,expires_at_unix_ms,0,0 FROM native_tool_scopes WHERE capture_kind=? AND capture_id=?`
	if receiving {
		query = `SELECT s.revision,s.owner_epoch,s.state,s.replay_barrier,s.expires_at_unix_ms,r.archive_epoch,f.generation FROM native_tool_scopes s JOIN runtime_storage_state r ON r.singleton=1 JOIN recording_capture_fences f ON f.capture_kind=s.capture_kind AND f.capture_id=s.capture_id WHERE s.capture_kind=? AND s.capture_id=?`
	}
	err := tx.QueryRowContext(ctx, query, c.Kind, c.ID).Scan(&rev, &owner, &s.state, &barrier, &expires, &archiveEpoch, &captureGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nativetool.ErrNotFound
	}
	if err != nil {
		return s, err
	}
	if receiving && (archiveEpoch <= 0 || captureGeneration <= 0) || rev <= 0 || owner < 0 || (barrier != 0 && barrier != 1) || (s.state != "active" && s.state != "unbound" && s.state != "revoked" && s.state != "closed") || s.state == "active" && owner == 0 || expires <= 0 {
		return s, nativetool.ErrInvalid
	}
	s.recordingScope = recording.Scope{Capture: c, Fence: recording.Fence{ArchiveEpoch: uint64(archiveEpoch), CaptureGeneration: uint64(captureGeneration)}}
	s.revision = uint64(rev)
	s.owner = uint64(owner)
	s.barrier = barrier == 1
	s.expires = time.UnixMilli(expires).UTC()
	return s, nil
}
func nativeActiveScope(ctx context.Context, tx *sql.Tx, c captureidentity.Reference, owner uint64, now time.Time) (nativeScope, error) {
	if err := nativeCapture(ctx, tx, c, now); err != nil {
		return nativeScope{}, err
	}
	s, err := loadNativeScope(ctx, tx, c)
	if err != nil {
		return s, err
	}
	if s.state != "active" || s.owner != owner || !s.expires.After(now) {
		return s, nativetool.ErrDenied
	}
	return s, nil
}
func nativeEnvironment(ctx context.Context, tx *sql.Tx, c captureidentity.Reference, plan environment.ClientToolPlan, assignment uint64, now time.Time) error {
	if plan.Validate() != nil || !nativeRevision(assignment) {
		return nativetool.ErrInvalid
	}
	if err := nativeCapture(ctx, tx, c, now); err != nil {
		return err
	}
	a, exists, err := loadCaptureAssignment(ctx, tx, c)
	if err != nil {
		return err
	}
	if !exists || uint64(a.Revision) != assignment || a.EnvironmentID != plan.EnvironmentID() || a.EnvironmentRevision != plan.EnvironmentRevision() || a.EnvironmentDigest != plan.EnvironmentDigest() {
		return nativetool.ErrDenied
	}
	current, _, exists, err := loadEnvironmentAggregate(ctx, tx, plan.EnvironmentID())
	if err != nil {
		return err
	}
	if !exists || current.State != environment.StateActive || current.ClientToolAccess == nil || !current.ClientToolAccess.Enabled {
		return nativetool.ErrDenied
	}
	e, exists, err := loadEnvironmentRevision(ctx, tx, plan.EnvironmentID(), plan.EnvironmentRevision())
	if err != nil {
		return err
	}
	digest, err := environment.Digest(e)
	if err != nil {
		return err
	}
	if !exists || e.State != environment.StateActive || digest != plan.EnvironmentDigest() || e.ClientToolAccess == nil || !e.ClientToolAccess.Enabled || e.ClientToolAccess.Route == nil || *e.ClientToolAccess.Route != plan.Route() || e.ContentRecording != plan.ContentRecording() || e.EffectivePolicySet() != plan.PolicySet() {
		return nativetool.ErrDenied
	}
	return nil
}
func (r *nativeToolRepository) EnsureScope(ctx context.Context, in nativetool.ScopeInput) (uint64, error) {
	if in.Capture.Validate() != nil || in.OwnerEpoch > math.MaxInt64 || in.ExpectedRevision >= math.MaxInt64 || !nativeTime(in.ExpiresAt) {
		return 0, nativetool.ErrInvalid
	}
	var revision uint64
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		now := time.Now().UTC()
		if !in.ExpiresAt.After(now) {
			return nativetool.ErrDenied
		}
		if err := nativeCapture(ctx, tx, in.Capture, now); err != nil {
			return err
		}
		a, exists, err := loadCaptureAssignment(ctx, tx, in.Capture)
		if err != nil {
			return err
		}
		if !exists || a.Validate() != nil {
			return nativetool.ErrDenied
		}
		if in.OwnerEpoch > 0 {
			current, _, exists, err := loadEnvironmentAggregate(ctx, tx, a.EnvironmentID)
			if err != nil {
				return err
			}
			if !exists || current.State != environment.StateActive || current.ClientToolAccess == nil || !current.ClientToolAccess.Enabled {
				return nativetool.ErrDenied
			}
		}
		s, err := loadNativeScope(ctx, tx, in.Capture)
		if err != nil && !errors.Is(err, nativetool.ErrNotFound) {
			return err
		}
		state := "active"
		if in.OwnerEpoch == 0 {
			state = "unbound"
		}
		revision = in.ExpectedRevision + 1
		if errors.Is(err, nativetool.ErrNotFound) {
			if in.ExpectedRevision != 0 {
				return nativetool.ErrConflict
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO native_tool_scopes(capture_kind,capture_id,revision,owner_epoch,state,replay_barrier,expires_at_unix_ms) VALUES(?,?,?,?,?,0,?)`, in.Capture.Kind, in.Capture.ID, revision, in.OwnerEpoch, state, in.ExpiresAt.UnixMilli())
			return err
		}
		if s.revision != in.ExpectedRevision || s.state == "closed" {
			return nativetool.ErrConflict
		}
		if s.owner != in.OwnerEpoch {
			if err := retireNativeWork(ctx, tx, in.Capture, now); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE native_tool_scopes SET revision=?,owner_epoch=?,state=?,replay_barrier=CASE WHEN owner_epoch<>? THEN 1 ELSE replay_barrier END,expires_at_unix_ms=? WHERE capture_kind=? AND capture_id=? AND revision=?`, revision, in.OwnerEpoch, state, in.OwnerEpoch, in.ExpiresAt.UnixMilli(), in.Capture.Kind, in.Capture.ID, in.ExpectedRevision)
		return err
	})
	if err != nil {
		return 0, err
	}
	return revision, nil
}
func (r *nativeToolRepository) GetScope(ctx context.Context, capture captureidentity.Reference) (nativetool.Scope, error) {
	if capture.Validate() != nil {
		return nativetool.Scope{}, nativetool.ErrInvalid
	}
	var result nativetool.Scope
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		scope, err := loadNativeScope(ctx, tx, capture)
		if err != nil {
			return err
		}
		result = nativetool.Scope{Capture: capture, Revision: scope.revision, OwnerEpoch: scope.owner, State: scope.state, ReplayBarrier: scope.barrier, ExpiresAt: scope.expires}
		return nil
	})
	if err != nil {
		return nativetool.Scope{}, err
	}
	return result, nil
}
func (r *nativeToolRepository) RegisterSession(ctx context.Context, in nativetool.SessionInput) error {
	if !nativeID(in.ID) || !nativeRevision(in.OwnerEpoch) || !nativeRevision(in.ExpectedAuthorityRevision) || !nativeTime(in.ExpiresAt) {
		return nativetool.ErrInvalid
	}
	return r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		now := time.Now().UTC()
		s, err := nativeActiveScope(ctx, tx, in.Capture, in.OwnerEpoch, now)
		if err != nil {
			return err
		}
		if s.revision != in.ExpectedAuthorityRevision {
			return nativetool.ErrConflict
		}
		if !in.ExpiresAt.After(now) || in.ExpiresAt.After(s.expires) && in.ExpiresAt.After(now.Add(30*time.Minute)) {
			return nativetool.ErrDenied
		}
		if err := nativeEnvironment(ctx, tx, in.Capture, in.Plan, in.AssignmentRevision, now); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_sessions WHERE capture_kind=? AND capture_id=? AND owner_epoch=? AND state IN('initializing','ready') AND expires_at_unix_ms>?`, in.Capture.Kind, in.Capture.ID, in.OwnerEpoch, now.UnixMilli()).Scan(&count); err != nil {
			return err
		}
		if count >= 64 {
			return nativetool.ErrDenied
		}
		if in.ExpiresAt.After(s.expires) {
			if _, err = tx.ExecContext(ctx, `UPDATE native_tool_scopes SET expires_at_unix_ms=? WHERE capture_kind=? AND capture_id=? AND revision=? AND owner_epoch=?`, in.ExpiresAt.UnixMilli(), in.Capture.Kind, in.Capture.ID, s.revision, s.owner); err != nil {
				return err
			}
		}
		digest := in.Plan.EnvironmentDigest()
		route := in.Plan.Route()
		_, err = tx.ExecContext(ctx, `INSERT INTO native_tool_sessions(session_id,capture_kind,capture_id,environment_id,environment_revision,environment_digest,assignment_revision,route_id,route_revision,target_origin,protocol_version,revision,owner_epoch,state,catalog_generation,catalog_digest,expires_at_unix_ms) VALUES(?,?,?,?,?,?,?,?,?,?,'2025-06-18',1,?,'initializing',0,NULL,?)`, in.ID, in.Capture.Kind, in.Capture.ID, in.Plan.EnvironmentID(), in.Plan.EnvironmentRevision(), digest[:], in.AssignmentRevision, route.ID, route.Revision, route.TargetOrigin.String(), in.OwnerEpoch, in.ExpiresAt.UnixMilli())
		return err
	})
}

type nativeSession struct {
	id                                      nativetool.ID
	capture                                 captureidentity.Reference
	environment                             environment.EnvironmentID
	environmentRevision                     environment.Revision
	digest                                  environment.CandidateDigest
	assignment, revision, owner, generation uint64
	route                                   string
	routeRevision                           uint64
	origin, state                           string
	catalog                                 []byte
	expires                                 time.Time
}

func loadNativeSession(ctx context.Context, tx *sql.Tx, id nativetool.ID) (nativeSession, error) {
	var s nativeSession
	var kind, capture, env, protocol string
	var er, assignment, revision, owner, generation, routeRevision, expires int64
	var digest []byte
	err := tx.QueryRowContext(ctx, `SELECT capture_kind,capture_id,environment_id,environment_revision,environment_digest,assignment_revision,route_id,route_revision,target_origin,protocol_version,revision,owner_epoch,state,catalog_generation,catalog_digest,expires_at_unix_ms FROM native_tool_sessions WHERE session_id=?`, id).Scan(&kind, &capture, &env, &er, &digest, &assignment, &s.route, &routeRevision, &s.origin, &protocol, &revision, &owner, &s.state, &generation, &s.catalog, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nativetool.ErrNotFound
	}
	if err != nil {
		return s, err
	}
	c, err := captureidentity.New(captureidentity.Kind(kind), capture)
	eid, eerr := environment.NewEnvironmentID(env)
	origin, oerr := originidentity.ParseProviderOrigin(s.origin)
	if err != nil || eerr != nil || oerr != nil || origin.Scheme() != "https" || origin.BasePath() != "" || er <= 0 || assignment <= 0 || revision <= 0 || owner <= 0 || generation < 0 || routeRevision <= 0 || len(digest) != 32 || (generation == 0) != (s.catalog == nil) || s.catalog != nil && len(s.catalog) != 32 || protocol != "2025-06-18" || (s.state != "initializing" && s.state != "ready" && s.state != "revoked" && s.state != "expired") || expires <= 0 {
		return s, nativetool.ErrInvalid
	}
	if _, err := environment.NewClientToolRouteID(s.route); err != nil {
		return s, nativetool.ErrInvalid
	}
	s.id = id
	s.capture = c
	s.environment = eid
	s.environmentRevision = environment.Revision(er)
	copy(s.digest[:], digest)
	s.assignment = uint64(assignment)
	s.revision = uint64(revision)
	s.owner = uint64(owner)
	s.generation = uint64(generation)
	s.routeRevision = uint64(routeRevision)
	s.expires = time.UnixMilli(expires).UTC()
	return s, nil
}
func validateNativeSession(ctx context.Context, tx *sql.Tx, s nativeSession, now time.Time) error {
	if _, err := nativeActiveScope(ctx, tx, s.capture, s.owner, now); err != nil {
		return err
	}
	if !s.expires.After(now) || (s.state != "initializing" && s.state != "ready") {
		return nativetool.ErrDenied
	}
	a, exists, err := loadCaptureAssignment(ctx, tx, s.capture)
	if err != nil {
		return err
	}
	if !exists || uint64(a.Revision) != s.assignment || a.EnvironmentID != s.environment || a.EnvironmentRevision != s.environmentRevision || a.EnvironmentDigest != s.digest {
		return nativetool.ErrDenied
	}
	current, _, exists, err := loadEnvironmentAggregate(ctx, tx, s.environment)
	if err != nil {
		return err
	}
	if !exists || current.State != environment.StateActive || current.ClientToolAccess == nil || !current.ClientToolAccess.Enabled {
		return nativetool.ErrDenied
	}
	e, exists, err := loadEnvironmentRevision(ctx, tx, s.environment, s.environmentRevision)
	if err != nil {
		return err
	}
	digest, err := environment.Digest(e)
	if err != nil {
		return err
	}
	if !exists || e.State != environment.StateActive || digest != s.digest || e.ClientToolAccess == nil || !e.ClientToolAccess.Enabled || e.ClientToolAccess.Route == nil {
		return nativetool.ErrDenied
	}
	route := e.ClientToolAccess.Route
	if route.ID.String() != s.route || uint64(route.Revision) != s.routeRevision || route.TargetOrigin.String() != s.origin || route.Validate() != nil {
		return nativetool.ErrDenied
	}
	return nil
}
func (r *nativeToolRepository) ReadySession(ctx context.Context, id nativetool.ID, expected uint64, now time.Time) (uint64, error) {
	if !nativeID(id) || !nativeRevision(expected) || expected == math.MaxInt64 || !nativeTime(now) {
		return 0, nativetool.ErrInvalid
	}
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		s, err := loadNativeSession(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := validateNativeSession(ctx, tx, s, now); err != nil {
			return err
		}
		if s.revision != expected || s.state != "initializing" || s.generation != 0 {
			return nativetool.ErrConflict
		}
		var initialized, initialize int
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(o.rpc_kind='initialize'),0),COALESCE(SUM(o.rpc_kind='initialized'),0) FROM native_tool_operations o JOIN native_tool_bindings b ON b.operation_id=o.operation_id WHERE b.session_id=? AND b.owner_epoch=? AND b.session_revision=? AND ((o.rpc_kind='initialize' AND o.state='completed') OR (o.rpc_kind='initialized' AND o.state IN('validated','completed')))`, id, s.owner, expected).Scan(&initialize, &initialized)
		if err != nil {
			return err
		}
		if initialize != 1 || initialized != 1 {
			return nativetool.ErrDenied
		}
		rows, err := tx.QueryContext(ctx, `SELECT o.operation_id FROM native_tool_operations o JOIN native_tool_bindings b ON b.operation_id=o.operation_id WHERE b.session_id=? AND b.owner_epoch=? AND b.session_revision=? AND o.rpc_kind IN('initialize','initialized')`, id, s.owner, expected)
		if err != nil {
			return err
		}
		ids := []nativetool.ID{}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			parsed, err := nativetool.NewID(raw)
			if err != nil {
				rows.Close()
				return nativetool.ErrInvalid
			}
			ids = append(ids, parsed)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, operationID := range ids {
			op, err := loadNativeOperation(ctx, tx, operationID)
			if err != nil {
				return err
			}
			if op.Capture != s.capture || op.EnvironmentID != s.environment || op.EnvironmentRevision != s.environmentRevision || op.EnvironmentDigest != s.digest || op.TargetOrigin != s.origin {
				return nativetool.ErrInvalid
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE native_tool_sessions SET state='ready',revision=revision+1 WHERE session_id=? AND revision=?`, id, expected)
		return err
	})
	if err != nil {
		return 0, err
	}
	return expected + 1, nil
}
func (r *nativeToolRepository) SetCatalog(ctx context.Context, id nativetool.ID, expected uint64, evidence nativetool.CatalogEvidence, now time.Time) (uint64, error) {
	if !nativeID(id) || !nativeRevision(expected) || expected == math.MaxInt64 || !nativeTime(now) || evidence.Digest() == (nativetool.Digest{}) {
		return 0, nativetool.ErrInvalid
	}
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		s, err := loadNativeSession(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := validateNativeSession(ctx, tx, s, now); err != nil {
			return err
		}
		if s.revision != expected || s.state != "ready" || s.generation == math.MaxInt64 {
			return nativetool.ErrConflict
		}
		scope := evidence.Scope()
		if scope.Capture != s.capture || scope.SessionID != id || scope.SessionRevision != expected+1 || scope.CatalogGeneration != s.generation+1 || scope.OwnerEpoch != s.owner || scope.AssignmentRevision != s.assignment {
			return nativetool.ErrDenied
		}
		if err := nativeEnvironment(ctx, tx, s.capture, scope.Plan, s.assignment, now); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT o.operation_id FROM native_tool_operations o JOIN native_tool_bindings b ON o.operation_id=b.operation_id WHERE b.session_id=? AND b.session_revision=? AND b.owner_epoch=? AND b.assignment_revision=? AND o.capture_kind=? AND o.capture_id=? AND o.rpc_kind='tools_list' AND o.state='completed'`, id, expected, s.owner, s.assignment, s.capture.Kind, s.capture.ID)
		if err != nil {
			return err
		}
		ids := []nativetool.ID{}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			parsed, err := nativetool.NewID(raw)
			if err != nil {
				rows.Close()
				return nativetool.ErrInvalid
			}
			ids = append(ids, parsed)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		requests := []nativetool.Digest{}
		for _, operationID := range ids {
			op, err := loadNativeOperation(ctx, tx, operationID)
			if err != nil {
				return err
			}
			if op.Capture != s.capture || op.Kind != nativetool.RequestToolsList || op.State != "completed" {
				return nativetool.ErrInvalid
			}
			b, err := loadNativeBinding(ctx, tx, operationID)
			if err != nil {
				return err
			}
			requests = append(requests, b.request)
		}
		if !evidence.MatchesRequests(requests) {
			return nativetool.ErrDenied
		}
		digest := evidence.Digest()
		if _, err := tx.ExecContext(ctx, `UPDATE native_tool_sessions SET revision=revision+1,catalog_generation=catalog_generation+1,catalog_digest=? WHERE session_id=? AND revision=?`, digest[:], id, expected); err != nil {
			return err
		}
		return cancelNativeSessionApprovals(ctx, tx, s.capture, s.id, now, "canceled")
	})
	if err != nil {
		return 0, err
	}
	return expected + 1, nil
}
func (r *nativeToolRepository) InvalidateCatalog(ctx context.Context, id nativetool.ID, expected uint64, now time.Time) (uint64, error) {
	if !nativeID(id) || !nativeRevision(expected) || expected == math.MaxInt64 || !nativeTime(now) {
		return 0, nativetool.ErrInvalid
	}
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		s, err := loadNativeSession(ctx, tx, id)
		if err != nil {
			return err
		}
		if err = validateNativeSession(ctx, tx, s, now); err != nil {
			return err
		}
		if s.revision != expected || s.state != "ready" {
			return nativetool.ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `UPDATE native_tool_sessions SET revision=revision+1,catalog_generation=0,catalog_digest=NULL WHERE session_id=? AND revision=?`, id, expected); err != nil {
			return err
		}
		return cancelNativeSessionApprovals(ctx, tx, s.capture, id, now, "canceled")
	})
	if err != nil {
		return 0, err
	}
	return expected + 1, nil
}

type nativeLegacyHistoryKey struct{}

func appendNativeEvent(ctx context.Context, tx *sql.Tx, id nativetool.ID, revision uint64, kind string, now time.Time) error {
	if ctx.Value(nativeLegacyHistoryKey{}) != true {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO native_tool_events(operation_id,operation_revision,kind,occurred_at_unix_ms) VALUES(?,?,?,?)`, id, revision, kind, now.UnixMilli())
	return err
}
func (r *nativeToolRepository) Receive(ctx context.Context, in nativetool.ReceiveInput) (nativetool.ReceiveResult, error) {
	if !nativeID(in.ID) || in.Capture.Validate() != nil || in.Kind.Validate() != nil || in.Recording.Validate() != nil || !nativeTime(in.ObservedAt) || len(in.ConnectionID) == 0 || len(in.ConnectionID) > 512 || (in.Method != "POST" && in.Method != "GET" && in.Method != "DELETE") || (in.RPCIDDigest == nil) != (in.RequestDigest == nil) || (in.SessionID != nil && !nativeID(*in.SessionID)) || (in.RPCIDDigest != nil && in.SessionID == nil) {
		return nativetool.ReceiveResult{}, nativetool.ErrInvalid
	}
	if (in.Kind == nativetool.RequestGetStream && in.Method != "GET") || (in.Kind == nativetool.RequestDeleteSession && in.Method != "DELETE") || (in.Kind != nativetool.RequestUnclassified && in.Kind != nativetool.RequestGetStream && in.Kind != nativetool.RequestDeleteSession && in.Method != "POST") {
		return nativetool.ReceiveResult{}, nativetool.ErrInvalid
	}
	origin, err := originidentity.ParseProviderOrigin(in.TargetOrigin)
	if err != nil || origin.Scheme() != "https" || origin.BasePath() != "" {
		return nativetool.ReceiveResult{}, nativetool.ErrInvalid
	}
	var result nativetool.ReceiveResult
	err = r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := nativeCapture(ctx, tx, in.Capture, in.ObservedAt); err != nil {
			return err
		}
		scope, err := loadNativeScopeFacts(ctx, tx, in.Capture, true)
		if err != nil {
			return err
		}
		if !scope.expires.After(in.ObservedAt) || scope.state == "closed" || scope.state == "revoked" {
			return nativetool.ErrDenied
		}
		a, exists, err := loadCaptureAssignment(ctx, tx, in.Capture)
		if err != nil {
			return err
		}
		if !exists || a.EnvironmentID != in.EnvironmentID || a.EnvironmentRevision != in.EnvironmentRevision || a.EnvironmentDigest != in.EnvironmentDigest {
			return nativetool.ErrDenied
		}
		aggregate, exists, err := loadEnvironmentRevision(ctx, tx, in.EnvironmentID, in.EnvironmentRevision)
		if err != nil {
			return err
		}
		if !exists || aggregate.ContentRecording != in.Recording {
			return nativetool.ErrInvalid
		}
		frozenDigest, err := environment.Digest(aggregate)
		if err != nil {
			return err
		}
		if frozenDigest != in.EnvironmentDigest {
			return nativetool.ErrInvalid
		}
		if aggregate.ClientToolAccess != nil && aggregate.ClientToolAccess.Enabled && (aggregate.ClientToolAccess.Route == nil || aggregate.ClientToolAccess.Route.TargetOrigin.String() != in.TargetOrigin) {
			return nativetool.ErrInvalid
		}
		if in.SessionID != nil {
			s, err := loadNativeSession(ctx, tx, *in.SessionID)
			if err != nil {
				return err
			}
			if s.capture != in.Capture || s.environment != in.EnvironmentID || s.environmentRevision != in.EnvironmentRevision || s.digest != in.EnvironmentDigest || s.origin != in.TargetOrigin || s.owner != scope.owner || !s.expires.After(in.ObservedAt) || (s.state != "initializing" && s.state != "ready") {
				return nativetool.ErrDenied
			}
		}
		if in.RPCIDDigest != nil {
			var id string
			var digest []byte
			err := tx.QueryRowContext(ctx, `SELECT operation_id,request_digest FROM native_tool_inbound WHERE session_id=? AND rpc_id_digest=? AND request_kind=?`, *in.SessionID, in.RPCIDDigest[:], in.Kind).Scan(&id, &digest)
			if err == nil {
				if len(digest) != 32 || string(digest) != string(in.RequestDigest[:]) {
					return nativetool.ErrConflict
				}
				parsed, err := nativetool.NewID(id)
				if err != nil {
					return err
				}
				result = nativetool.ReceiveResult{OperationID: parsed, Duplicate: true}
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		var pending int
		var duplicate int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_operations WHERE operation_id=?`, in.ID).Scan(&duplicate); err != nil {
			return err
		}
		if duplicate != 0 {
			return nativetool.ErrConflict
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_operations WHERE capture_kind=? AND capture_id=? AND state IN('received','validated','awaiting_decision','started')`, in.Capture.Kind, in.Capture.ID).Scan(&pending); err != nil {
			return err
		}
		if pending >= 1024 {
			return nativetool.ErrDenied
		}
		digest := in.EnvironmentDigest
		if _, err := tx.ExecContext(ctx, `INSERT INTO native_tool_operations(operation_id,capture_kind,capture_id,environment_id,environment_revision,environment_digest,connection_id,target_origin,method,rpc_kind,revision,state,decision_source,recording,retention_days,created_at_unix_ms,metadata_expires_at_unix_ms,terminal_reason,last_event_kind,last_event_at_unix_ms,maximum_event_at_unix_ms,recording_archive_epoch,recording_capture_generation) VALUES(?,?,?,?,?,?,?,?,?,?,1,'received','none',?,?,?,?, '','received',?,?,?,?)`, in.ID, in.Capture.Kind, in.Capture.ID, in.EnvironmentID, in.EnvironmentRevision, digest[:], in.ConnectionID, in.TargetOrigin, in.Method, in.Kind, in.Recording.Mode, in.Recording.RetentionDays, in.ObservedAt.UnixMilli(), in.ObservedAt.Add(30*24*time.Hour).UnixMilli(), in.ObservedAt.UnixMilli(), in.ObservedAt.UnixMilli(), scope.recordingScope.Fence.ArchiveEpoch, scope.recordingScope.Fence.CaptureGeneration); err != nil {
			return err
		}
		if err := appendNativeEvent(ctx, tx, in.ID, 1, "received", in.ObservedAt); err != nil {
			return err
		}
		if in.RPCIDDigest != nil {
			if _, err := tx.ExecContext(ctx, `INSERT INTO native_tool_inbound(session_id,rpc_id_digest,request_kind,request_digest,operation_id) VALUES(?,?,?,?,?)`, *in.SessionID, in.RPCIDDigest[:], in.Kind, in.RequestDigest[:], in.ID); err != nil {
				return err
			}
		}
		stageNativeHistory(ctx, nativetool.Operation{ID: in.ID, Capture: in.Capture, RecordingScope: scope.recordingScope, EnvironmentID: in.EnvironmentID, EnvironmentRevision: in.EnvironmentRevision, EnvironmentDigest: in.EnvironmentDigest, Recording: in.Recording, Kind: in.Kind, TargetOrigin: in.TargetOrigin, Revision: 1, State: "received", DecisionSource: "none", CreatedAt: in.ObservedAt, MaximumEventAt: in.ObservedAt, MetadataExpiresAt: in.ObservedAt.Add(30 * 24 * time.Hour)}, "received", in.ObservedAt)
		result = nativetool.ReceiveResult{OperationID: in.ID, RecordingScope: scope.recordingScope, CreatedAt: time.UnixMilli(in.ObservedAt.UnixMilli()).UTC()}
		return nil
	})
	if err != nil {
		return nativetool.ReceiveResult{}, err
	}
	return result, nil
}

func nativeTransition(ctx context.Context, tx *sql.Tx, op nativetool.Operation, state, source, reason, event string, now time.Time) error {
	if op.Revision == math.MaxInt64 || now.Before(op.CreatedAt) {
		return nativetool.ErrInvalid
	}
	var completed any
	if state == "denied" || state == "canceled" || state == "failed" || state == "completed" || state == "uncertain" {
		completed = now.UnixMilli()
	}
	result, err := tx.ExecContext(ctx, `UPDATE native_tool_operations SET revision=revision+1,state=?,decision_source=?,terminal_reason=?,completed_at_unix_ms=?,last_event_kind=?,last_event_at_unix_ms=?,maximum_event_at_unix_ms=MAX(maximum_event_at_unix_ms,?) WHERE operation_id=? AND revision=?`, state, source, reason, completed, event, now.UnixMilli(), now.UnixMilli(), op.ID, op.Revision)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return nativetool.ErrConflict
	}
	if err := appendNativeEvent(ctx, tx, op.ID, op.Revision+1, event, now); err != nil {
		return err
	}
	op.Revision++
	op.State = state
	op.DecisionSource = source
	op.Reason = reason
	if completed != nil {
		t := now
		op.CompletedAt = &t
	}
	if now.After(op.MaximumEventAt) {
		op.MaximumEventAt = now
	}
	stageNativeHistory(ctx, op, event, now)
	return nil
}
func (r *nativeToolRepository) Bind(ctx context.Context, id nativetool.ID, b nativetool.Binding, now time.Time) error {
	if b.Kind() == nativetool.RequestServerReply {
		return nativetool.ErrDenied
	}
	return r.bind(ctx, id, b, now)
}
func (r *nativeToolRepository) bind(ctx context.Context, id nativetool.ID, b nativetool.Binding, now time.Time) error {
	resolved, resolvedErr := b.Plan().ResolvedEgress()
	if resolvedErr != nil {
		return nativetool.ErrInvalid
	}
	profileJSON, profileErr := json.Marshal(resolved.Profile())
	if profileErr != nil || len(profileJSON) > nativetool.MaxEffectiveProfileJSONBytes {
		return nativetool.ErrInvalid
	}
	if !nativeID(id) || b.Digest() == (nativetool.Digest{}) || !nativeTime(now) {
		return nativetool.ErrInvalid
	}
	denied := false
	err := r.transaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		op, err := loadNativeOperation(ctx, tx, id)
		if err != nil {
			return err
		}
		if op.State != "received" {
			return nativetool.ErrConflict
		}
		plan := b.Plan()
		if op.Capture != b.Capture() || op.EnvironmentID != plan.EnvironmentID() || op.EnvironmentRevision != plan.EnvironmentRevision() || op.EnvironmentDigest != plan.EnvironmentDigest() || op.TargetOrigin != plan.Route().TargetOrigin.String() || op.Recording != plan.ContentRecording() || op.Kind != b.Kind() {
			return nativetool.ErrInvalid
		}
		if _, err := nativeActiveScope(ctx, tx, b.Capture(), b.OwnerEpoch(), now); err != nil {
			return err
		}
		a, exists, err := loadCaptureAssignment(ctx, tx, b.Capture())
		if err != nil {
			return err
		}
		if !exists || uint64(a.Revision) != b.AssignmentRevision() || a.EnvironmentID != op.EnvironmentID || a.EnvironmentRevision != op.EnvironmentRevision || a.EnvironmentDigest != op.EnvironmentDigest {
			return nativetool.ErrDenied
		}
		current, _, exists, err := loadEnvironmentAggregate(ctx, tx, op.EnvironmentID)
		if err != nil {
			return err
		}
		if !exists || current.State != environment.StateActive || current.ClientToolAccess == nil || !current.ClientToolAccess.Enabled {
			denied = true
			return nativeTransition(ctx, tx, op, "denied", "policy", "permission_off", "denied", now)
		}
		if err := nativeEnvironment(ctx, tx, b.Capture(), plan, b.AssignmentRevision(), now); err != nil {
			return err
		}
		s, err := loadNativeSession(ctx, tx, b.SessionID())
		if err != nil {
			return err
		}
		if err := validateNativeSession(ctx, tx, s, now); err != nil {
			return err
		}
		if s.capture != b.Capture() || b.SessionRevision() > s.revision || b.Kind() != nativetool.RequestServerReply && s.revision != b.SessionRevision() || s.owner != b.OwnerEpoch() || s.assignment != b.AssignmentRevision() || s.environment != plan.EnvironmentID() || s.environmentRevision != plan.EnvironmentRevision() || s.digest != plan.EnvironmentDigest() {
			return nativetool.ErrConflict
		}
		catalog, hasCatalog := b.CatalogDigest()
		if b.CatalogGeneration() > s.generation && !(b.Kind() == nativetool.RequestServerReply && b.SessionRevision() < s.revision) || b.Kind() != nativetool.RequestServerReply && (s.generation != b.CatalogGeneration() || (s.catalog != nil) != hasCatalog || hasCatalog && string(s.catalog) != string(catalog[:])) {
			return nativetool.ErrConflict
		}
		switch b.Kind() {
		case nativetool.RequestInitialize:
			if s.state != "initializing" || s.generation != 0 {
				return nativetool.ErrDenied
			}
		case nativetool.RequestInitialized:
			if s.state != "initializing" || s.generation != 0 {
				return nativetool.ErrDenied
			}
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM native_tool_operations o JOIN native_tool_bindings b ON b.operation_id=o.operation_id WHERE b.session_id=? AND b.session_revision=? AND b.owner_epoch=? AND o.rpc_kind='initialize' AND o.state='completed'`, s.id, s.revision, s.owner).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return nativetool.ErrDenied
			}
		case nativetool.RequestToolsList, nativetool.RequestPing, nativetool.RequestGetStream:
			if s.state != "ready" {
				return nativetool.ErrDenied
			}
		case nativetool.RequestToolsCall:
			if s.state != "ready" || s.generation == 0 {
				return nativetool.ErrDenied
			}
		case nativetool.RequestDeleteSession:
		case nativetool.RequestCancel:
			target, ok := b.CancelledRPCIDDigest()
			if !ok || s.state != "ready" {
				return nativetool.ErrDenied
			}
			var targetID string
			if err := tx.QueryRowContext(ctx, `SELECT i.operation_id FROM native_tool_inbound i JOIN native_tool_operations o ON o.operation_id=i.operation_id WHERE i.session_id=? AND i.rpc_id_digest=? AND o.capture_kind=? AND o.capture_id=? AND o.state IN('validated','awaiting_decision','started') AND o.rpc_kind NOT IN('initialized','cancel','server_reply')`, s.id, target[:], s.capture.Kind, s.capture.ID).Scan(&targetID); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nativetool.ErrDenied
				}
				return err
			}
			parsed, err := nativetool.NewID(targetID)
			if err != nil {
				return nativetool.ErrInvalid
			}
			targetOperation, err := loadNativeOperation(ctx, tx, parsed)
			if err != nil {
				return err
			}
			targetBinding, err := loadNativeBinding(ctx, tx, parsed)
			if err != nil {
				return err
			}
			if targetOperation.BindingDigest == nil || targetBinding.session != s.id || targetBinding.owner != s.owner || targetBinding.assignment != s.assignment || targetBinding.sessionRevision > s.revision {
				return nativetool.ErrDenied
			}
		case nativetool.RequestServerReply:
			parent, ok := b.ParentOperationID()
			if !ok {
				return nativetool.ErrDenied
			}
			frozen := nativeBindingRow{resolved: resolved, bindingVersion: 2, session: b.SessionID(), sessionRevision: b.SessionRevision(), generation: b.CatalogGeneration(), owner: b.OwnerEpoch(), assignment: b.AssignmentRevision(), parent: &parent, catalog: nil}
			if hasCatalog {
				frozen.catalog = catalog[:]
			}
			if err := r.validateNativeParent(ctx, tx, op, frozen, s, now); err != nil {
				return err
			}
		default:
			return nativetool.ErrInvalid
		}
		rpc, hasRPC := b.RPCIDDigest()
		var inboundSession string
		var inboundRPC, inboundRequest []byte
		err = tx.QueryRowContext(ctx, `SELECT session_id,rpc_id_digest,request_digest FROM native_tool_inbound WHERE operation_id=?`, id).Scan(&inboundSession, &inboundRPC, &inboundRequest)
		if hasRPC {
			digest := b.RequestDigest()
			if err != nil || inboundSession != string(b.SessionID()) || len(inboundRPC) != 32 || string(inboundRPC) != string(rpc[:]) || len(inboundRequest) != 32 || string(inboundRequest) != string(digest[:]) {
				return nativetool.ErrInvalid
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nativetool.ErrInvalid
		}
		parent, hasParent := b.ParentOperationID()
		var parentValue any
		if hasParent {
			parentValue = parent
		}
		server, hasServer := b.ServerRequestDigest()
		replay, hasReplay := b.ReplayKey()
		cancel, hasCancel := b.CancelledRPCIDDigest()
		route := plan.Route()
		request := b.RequestDigest()
		params := b.ParamsDigest()
		group := b.GroupDigest()
		digest := b.Digest()
		if _, err := tx.ExecContext(ctx, `INSERT INTO native_tool_bindings(operation_id,session_id,session_revision,catalog_generation,catalog_digest,route_id,route_revision,owner_epoch,assignment_revision,request_digest,params_digest,group_digest,binding_digest,replay_key,parent_operation_id,server_request_digest,tool_name,cancel_rpc_id_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, b.SessionID(), b.SessionRevision(), b.CatalogGeneration(), nativeOptional(catalog, hasCatalog), route.ID, route.Revision, b.OwnerEpoch(), b.AssignmentRevision(), request[:], params[:], group[:], digest[:], nativeOptional(replay, hasReplay), parentValue, nativeOptional(server, hasServer), b.ToolName(), nativeOptional(cancel, hasCancel)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO native_tool_binding_egress(operation_id,binding_digest,binding_version,profile_json,selection_source,default_revision) VALUES(?,?,2,?,?,?)`, id, digest[:], string(profileJSON), resolved.Source(), resolved.DefaultRevision()); err != nil {
			return err
		}
		op.BindingDigest = &digest
		op.ToolName = b.ToolName()
		op.CredentialOwner = string(route.CredentialOwner)
		if plan.PolicySet().ToolMode == environment.ToolPolicyStrict && (b.Kind() == nativetool.RequestToolsCall || b.Kind() == nativetool.RequestServerReply) {
			denied = true
			return nativeTransition(ctx, tx, op, "denied", "policy", "policy_strict", "denied", now)
		}
		return nativeTransition(ctx, tx, op, "validated", "none", "", "validated", now)
	})
	if err != nil {
		return err
	}
	if denied {
		return nativetool.ErrDenied
	}
	return nil
}

func (r *nativeToolRepository) validateNativeParent(ctx context.Context, tx *sql.Tx, op nativetool.Operation, b nativeBindingRow, s nativeSession, now time.Time) error {
	if b.parent == nil || *b.parent == op.ID || s.state != "ready" {
		return nativetool.ErrDenied
	}
	p, err := loadNativeOperation(ctx, tx, *b.parent)
	if err != nil {
		return err
	}
	parent, err := loadNativeBinding(ctx, tx, *b.parent)
	if err != nil {
		return err
	}
	if p.Capture != op.Capture || p.EnvironmentID != op.EnvironmentID || p.EnvironmentRevision != op.EnvironmentRevision || p.EnvironmentDigest != op.EnvironmentDigest || p.TargetOrigin != op.TargetOrigin || parent.session != s.id || parent.owner != s.owner || parent.assignment != s.assignment || parent.sessionRevision != b.sessionRevision || parent.generation != b.generation || string(parent.catalog) != string(b.catalog) || parent.bindingVersion != 2 || b.bindingVersion != 2 || parent.resolved != b.resolved {
		return nativetool.ErrDenied
	}
	return r.verifyNativeCallback(ctx, tx, op, p, now)
}
