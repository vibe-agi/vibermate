package nativemcp

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

type ownerScope struct {
	claimKey        uint64
	capture         captureidentity.Reference
	epoch, revision uint64
	context         context.Context
	cancel          context.CancelFunc
	sessions        map[nativetool.ID]*session
	revoked         bool
	expires         time.Time
}
type session struct {
	claimKey                         uint64
	mu                               sync.Mutex
	eventMu                          sync.Mutex
	owner                            *ownerScope
	context                          context.Context
	cancel                           context.CancelFunc
	id                               nativetool.ID
	plan                             environment.ClientToolPlan
	assignment, revision, generation uint64
	wire, authorization, cookie      string
	ready, closed                    bool
	closeFailed                      bool
	renewalFailed                    bool
	last, timeLimit                  time.Time
	active                           map[string]*activeRPC
	catalog                          *nativetool.CatalogEvidence
	pages                            []nativetool.CatalogPageObservation
	listDigests                      []nativetool.Digest
	nextCursor                       string
	catalogBytes                     int64
	collection                       uint64
	catalogBusy                      bool
	hasher                           *nativetool.SensitiveBindingHasher
	parents                          map[string]*callbackParent
	streams                          map[nativetool.ID]*streamRecord
	eventIndex                       map[string]streamCursor
	retained                         int64
	publicBytes                      int64
	callbacks                        map[string]bool
	elicitations                     map[string]nativetool.ID
	tools                            map[nativetool.ID]*nativetool.SensitiveToolWitness
}
type activeRPC struct {
	operation        nativetool.ID
	cancel           context.CancelFunc
	binding          nativetool.Binding
	protocolCanceled bool
	stream           *streamRecord
}
type callbackParent struct {
	authority     nativetool.CallbackAuthority
	operation     nativetool.ID
	binding       nativetool.Binding
	kind          string
	sensitive     *nativetool.SensitiveParent
	observer      *nativetool.ParentObserver
	request       []byte
	digest        nativetool.Digest
	consumed      bool
	elicitationID string
	release       func()
}

func (e *executor) admitSession(ctx context.Context, c CapturedRequest, r rpc, plan environment.ClientToolPlan) (*session, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, errSession
	}
	capture, _ := c.admission.CaptureReference()
	owner := e.scopes[capture.Key()]
	now := e.dependencies.Clock().UTC().Truncate(time.Millisecond)
	if r.kind == nativetool.RequestInitialize {
		callbacks, err := declaredCallbacks(r, e.limits)
		if err != nil {
			return nil, err
		}
		if c.request.Header.Get("Mcp-Session-Id") != "" || c.request.Header.Get("Authorization") == "" && c.request.Header.Get("Cookie") == "" {
			return nil, errSession
		}
		keys := uint64(1)
		scopeExpired := false
		if owner != nil && !owner.revoked {
			scope, err := e.dependencies.Store.GetScope(ctx, capture)
			if err != nil {
				return nil, err
			}
			scopeExpired = !scope.ExpiresAt.After(now)
		}
		if owner == nil || owner.revoked || scopeExpired {
			keys = 2
		}
		firstKey, err := e.reserveClaimKeysLocked(keys)
		if err != nil {
			return nil, err
		}
		sessionKey := firstKey + keys - 1
		if owner != nil && !owner.revoked {
			for _, existing := range owner.sessions {
				existing.mu.Lock()
				expired := !existing.timeLimit.After(now) || now.Sub(existing.last) >= e.limits.SessionIdle
				existing.mu.Unlock()
				if expired {
					if err := e.closeSessionLocked(ctx, existing); err != nil {
						return nil, err
					}
				}
			}
			if scopeExpired {
				owner.revoked = true
				owner.cancel()
			}
		}
		if owner == nil || owner.revoked {
			current, err := e.dependencies.Store.GetScope(ctx, capture)
			if err != nil && !errors.Is(err, nativetool.ErrNotFound) {
				return nil, err
			}
			expected := uint64(0)
			if err == nil {
				expected = current.Revision
			}
			epoch, err := e.dependencies.IDs.NewOwnerEpoch(ctx)
			if err != nil {
				return nil, err
			}
			revision, err := e.dependencies.Store.EnsureScope(ctx, nativetool.ScopeInput{Capture: capture, ExpectedRevision: expected, OwnerEpoch: epoch, ExpiresAt: now.Add(e.limits.SessionIdle)})
			if err != nil {
				return nil, err
			}
			ownerContext, cancel := context.WithCancel(e.dependencies.OwnerContext)
			owner = &ownerScope{claimKey: firstKey, capture: capture, epoch: epoch, revision: revision, context: ownerContext, cancel: cancel, sessions: map[nativetool.ID]*session{}, expires: now.Add(e.limits.SessionIdle)}
			e.scopes[capture.Key()] = owner
		}
		active := 0
		for _, s := range owner.sessions {
			s.mu.Lock()
			if !s.closed && s.timeLimit.After(now) && now.Sub(s.last) < e.limits.SessionIdle {
				active++
			}
			s.mu.Unlock()
		}
		if active >= e.limits.SessionsPerCapture {
			return nil, errBudget
		}
		id, err := e.dependencies.IDs.NewNativeOperationID(ctx)
		if err != nil {
			return nil, err
		}
		memory := int64(4096 + len(c.request.Header.Get("Authorization")) + len(c.request.Header.Get("Cookie")))
		if !e.budget.acquire(memory) {
			return nil, errBudget
		}
		expires := now.Add(e.limits.SessionIdle)
		if err = e.dependencies.Store.RegisterSession(ctx, nativetool.SessionInput{ID: id, Capture: capture, Plan: plan, AssignmentRevision: uint64(c.lease.Assignment().Revision), ExpectedAuthorityRevision: owner.revision, OwnerEpoch: owner.epoch, ExpiresAt: expires}); err != nil {
			e.budget.release(memory)
			return nil, err
		}
		owner.expires = expires
		sessionContext, sessionCancel := context.WithCancel(owner.context)
		s := &session{claimKey: sessionKey, owner: owner, context: sessionContext, cancel: sessionCancel, id: id, plan: plan, assignment: uint64(c.lease.Assignment().Revision), revision: 1, last: now, timeLimit: expires, authorization: c.request.Header.Get("Authorization"), cookie: c.request.Header.Get("Cookie"), active: map[string]*activeRPC{}, parents: map[string]*callbackParent{}, streams: map[nativetool.ID]*streamRecord{}, eventIndex: map[string]streamCursor{}}
		owner.sessions[id] = s
		s.callbacks = callbacks
		s.elicitations = map[string]nativetool.ID{}
		s.tools = map[nativetool.ID]*nativetool.SensitiveToolWitness{}
		s.retained = memory
		return s, nil
	}
	if owner == nil || owner.revoked || owner.context.Err() != nil {
		return nil, errSession
	}
	wire := c.request.Header.Get("Mcp-Session-Id")
	if wire == "" || len(wire) > e.limits.OpaqueIDBytes {
		return nil, errSession
	}
	for _, s := range owner.sessions {
		s.mu.Lock()
		if s.wire == wire && !s.closed && (!s.timeLimit.After(now) || now.Sub(s.last) >= e.limits.SessionIdle) {
			s.mu.Unlock()
			if err := e.closeSessionLocked(ctx, s); err != nil {
				return nil, err
			}
			return nil, errSession
		}
		matches := s.wire == wire && !s.closed && !s.renewalFailed && s.timeLimit.After(now) && now.Sub(s.last) < e.limits.SessionIdle && s.authorization == c.request.Header.Get("Authorization") && s.cookie == c.request.Header.Get("Cookie") && s.assignment == uint64(c.lease.Assignment().Revision) && s.plan.EnvironmentDigest() == plan.EnvironmentDigest() && s.plan.Route() == plan.Route()
		if matches {
			expires := now.Add(e.limits.SessionIdle)
			if err := e.dependencies.Store.TouchSession(ctx, s.id, s.revision, expires, now); err != nil {
				s.mu.Unlock()
				return nil, err
			}
			s.last = now
			s.timeLimit = expires
			owner.expires = expires
		}
		s.mu.Unlock()
		if matches {
			return s, nil
		}
	}
	return nil, errSession
}
func (e *executor) closeSession(ctx context.Context, s *session) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closeSessionLocked(ctx, s)
}

// e.mu owns registry reachability; s.mu owns active consumers and allocations.
func (e *executor) closeSessionLocked(ctx context.Context, s *session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		e.clearSession(s)
		delete(s.owner.sessions, s.id)
		if s.closeFailed {
			return nativetool.ErrUnavailable
		}
		return nil
	}
	revision, err := e.dependencies.Store.CloseSession(ctx, s.id, s.revision, e.dependencies.Clock().UTC())
	if err != nil {
		// The transaction may have committed despite its error. Keep this old
		// handle failed; only existing owner reconciliation may settle SQL.
		s.closeFailed = true
		s.owner.revoked = true
		s.owner.cancel()
		e.clearSession(s)
		delete(s.owner.sessions, s.id)
		return err
	}
	s.revision = revision
	e.clearSession(s)
	delete(s.owner.sessions, s.id)
	return nil
}
func (e *executor) clearSession(s *session) {
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	if s.hasher != nil {
		s.hasher.Close()
	}
	// Closed sessions are unlinked immediately, but in-flight consumers retain
	// their exact handles and charged map/wire allocations until final release.
	if len(s.active) > 0 {
		return
	}
	for _, stream := range s.streams {
		if stream.attached {
			return
		}
	}
	s.authorization = ""
	s.cookie = ""
	for _, parent := range s.parents {
		if parent.release != nil {
			parent.release()
			parent.release = nil
		}
	}
	s.parents = nil
	s.elicitations = nil
	s.tools = nil
	for _, stream := range s.streams {
		stream.retired = true
		stream.expiryTimer.Stop()
		stream.cancel()
		stream.charged = 0
	}
	s.streams = nil
	s.eventIndex = nil
	s.active = nil
	s.callbacks = nil
	s.listDigests = nil
	s.hasher = nil
	s.pages = nil
	s.catalog = nil
	retained := s.retained
	s.retained = 0
	s.publicBytes = 0
	s.catalogBytes = 0
	e.budget.release(retained)
}
func (e *executor) acquireRPC(s *session, id nativetool.ID, r rpc) (func(), error) {
	e.reclaimDeliveredStreams(s)
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.stream != nil {
		if s.closed || s.renewalFailed || r.stream.retired || !r.stream.attached {
			return nil, errSession
		}
		// The retained originating RPC already owns its logical concurrency
		// slot. This GET owns only the exclusive physical attachment.
		return func() {}, nil
	}
	if r.kind == nativetool.RequestCancel {
		identity, err := identity(r.params["requestId"], e.limits.OpaqueIDBytes)
		if err != nil {
			return nil, err
		}
		if target := s.active[string(identity)]; target != nil && target.stream != nil {
			// Cancellation addresses the retained logical slot even at its
			// configured ceiling; the notification creates no second RPC.
			return func() {}, nil
		}
	}
	if r.kind != nativetool.RequestCancel && r.kind != nativetool.RequestDeleteSession && r.kind != nativetool.RequestServerReply && (len(s.streams) >= e.limits.Pending || len(s.eventIndex) >= e.limits.Pending) {
		return nil, errBudget
	}
	key := string(r.id)
	if key == "" {
		key = "operation:" + id.String()
	}
	if s.closed || s.renewalFailed || len(s.active) >= e.limits.ConcurrentRPCsPerSession {
		return nil, errBudget
	}
	if s.active[key] != nil {
		return nil, nativetool.ErrReplay
	}
	if r.kind == nativetool.RequestToolsList {
		cursor, _ := text(r.params["cursor"])
		if cursor != s.nextCursor {
			return nil, errSession
		}
		if s.catalogBusy {
			return nil, errSession
		}
		if len(s.pages) >= e.limits.CatalogPages {
			return nil, errBudget
		}
		s.catalogBusy = true
		if r.collectionToken != nil {
			*r.collectionToken = s.collection
		}
	}
	s.active[key] = &activeRPC{operation: id, cancel: r.cancel}
	return func() {
		s.mu.Lock()
		active := s.active[key]
		if active == nil || active.stream == nil || active.stream.retired || active.stream.terminal {
			delete(s.active, key)
		}
		if r.kind == nativetool.RequestToolsList {
			s.catalogBusy = false
		}
		drained := (s.closed || s.renewalFailed) && len(s.active) == 0
		s.mu.Unlock()
		if drained {
			_ = e.closeSession(context.Background(), s)
		}
	}, nil
}
func (e *executor) validateResponseSession(c CapturedRequest, r rpc, s *session, response *http.Response) error {
	if version := response.Header.Get("Mcp-Protocol-Version"); version != "" && version != "2025-06-18" {
		return errSession
	}
	if len(response.Header.Values("Mcp-Session-Id")) > 1 || response.Header.Get("Mcp-Method") != "" || response.Header.Get("Mcp-Name") != "" {
		return errProtocol
	}
	wire := response.Header.Get("Mcp-Session-Id")
	if r.kind == nativetool.RequestInitialize {
		// A correlated initialization error need not create a session. A
		// successful result still requires the authenticated header below.
		if wire == "" {
			return nil
		}
		if len(wire) > e.limits.OpaqueIDBytes {
			return errSession
		}
		for _, ch := range wire {
			if ch < 0x21 || ch > 0x7e {
				return errSession
			}
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		for _, other := range s.owner.sessions {
			if other == s {
				continue
			}
			other.mu.Lock()
			conflict := other.wire == wire && !other.closed
			other.mu.Unlock()
			if conflict {
				return errSession
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.wire = wire
		hasher, err := nativetool.NewSensitiveBindingHasher(nativetool.SensitiveScope{Capture: s.owner.capture, Plan: s.plan, AssignmentRevision: s.assignment, OwnerEpoch: s.owner.epoch, SessionID: s.id})
		if err != nil {
			return err
		}
		s.hasher = hasher
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if wire != "" && wire != s.wire || s.closed {
		return errSession
	}
	if last := c.request.Header.Get("Last-Event-Id"); last != "" {
		if r.kind != nativetool.RequestGetStream {
			return errProtocol
		}
		if _, seen := s.eventIndex[last]; !seen {
			return errSession
		}
	}
	return nil
}
func (e *executor) Revoke(ctx context.Context, capture captureidentity.Reference, reason RevocationReason) error {
	return e.revokeOwner(ctx, capture, nil, reason)
}

// A failed old request may clean up only its original in-memory owner. A
// replacement is admitted by real EnsureScope and must not be fenced by it.
func (e *executor) revokeOwner(ctx context.Context, capture captureidentity.Reference, expected *ownerScope, reason RevocationReason) error {
	if ctx == nil || capture.Validate() != nil {
		return nativetool.ErrInvalid
	}
	switch reason {
	case RevocationPermissionOff, RevocationCaptureStopped, RevocationAssignmentApplied, RevocationOwnerChanged, RevocationShutdown:
	default:
		return nativetool.ErrInvalid
	}
	e.mu.Lock()
	owner := e.scopes[capture.Key()]
	if expected != nil && owner != expected {
		e.mu.Unlock()
		return nil
	}
	for id, preview := range e.previews {
		if preview.capture == capture {
			e.discardPreviewLocked(id)
		}
	}
	if owner != nil {
		owner.revoked = true
		owner.cancel()
		for id, s := range owner.sessions {
			s.mu.Lock()
			e.clearSession(s)
			s.mu.Unlock()
			delete(owner.sessions, id)
		}
	}
	e.mu.Unlock()
	if owner == nil {
		return nil
	}
	scope, err := e.dependencies.Store.GetScope(ctx, capture)
	if err != nil {
		return err
	}
	if scope.OwnerEpoch != owner.epoch || scope.State == "revoked" || scope.State == "closed" {
		return nil
	}
	return e.dependencies.Store.Revoke(ctx, capture, scope.Revision, "revoked", e.dependencies.Clock().UTC())
}
func (e *executor) Close(ctx context.Context) error {
	if ctx == nil {
		return nativetool.ErrInvalid
	}
	e.mu.Lock()
	e.closed = true
	captures := []captureidentity.Reference{}
	for _, owner := range e.scopes {
		captures = append(captures, owner.capture)
	}
	e.mu.Unlock()
	var result error
	for _, capture := range captures {
		result = errors.Join(result, e.Revoke(ctx, capture, RevocationShutdown))
	}
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
		e.mu.Lock()
		claims := len(e.claimed)
		clear(e.claimed)
		e.mu.Unlock()
		e.budget.release(int64(claims) * nativeClaimReservationBytes)
		return result
	case <-ctx.Done():
		return errors.Join(result, ctx.Err())
	}
}
func equalEvent(previous, event []byte) bool { return bytes.Equal(previous, event) }

func (e *executor) touchObserved(ctx context.Context, s *session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.renewalFailed {
		return nil
	}
	now := e.dependencies.Clock().UTC().Truncate(time.Millisecond)
	interval := e.limits.SessionIdle / 2
	if interval > time.Minute {
		interval = time.Minute
	}
	if now.Sub(s.last) < interval {
		return nil
	}
	expires := now.Add(e.limits.SessionIdle)
	if err := e.dependencies.Store.TouchSession(ctx, s.id, s.revision, expires, now); err != nil {
		s.renewalFailed = true
		return err
	}
	s.last = now
	s.timeLimit = expires
	return nil
}

func (e *executor) observeActivity(ctx context.Context, request rpc, s *session, knownTerminal bool) error {
	err := e.touchObserved(ctx, s)
	if err != nil && knownTerminal && request.renewalUnavailable != nil {
		*request.renewalUnavailable = true
		return nil
	}
	return err
}

func (e *executor) fencePublication(s *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renewalFailed = true
	e.discardCatalog(s)
}
