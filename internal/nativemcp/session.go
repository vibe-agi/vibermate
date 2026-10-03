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
	capture         captureidentity.Reference
	epoch, revision uint64
	context         context.Context
	cancel          context.CancelFunc
	sessions        map[nativetool.ID]*session
	revoked         bool
	expires         time.Time
}
type session struct {
	mu                               sync.Mutex
	owner                            *ownerScope
	id                               nativetool.ID
	plan                             environment.ClientToolPlan
	assignment, revision, generation uint64
	wire, authorization, cookie      string
	ready, closed                    bool
	last, timeLimit                  time.Time
	active                           map[string]bool
	catalog                          *nativetool.CatalogEvidence
	pages                            []nativetool.CatalogPageObservation
	listDigests                      []nativetool.Digest
	nextCursor                       string
	catalogBytes                     int64
	hasher                           *nativetool.SensitiveBindingHasher
	parents                          map[string]*callbackParent
	events                           map[string][]byte
	retained                         int64
	publicBytes                      int64
	callbacks                        map[string]bool
	elicitations                     map[string]nativetool.ID
	tools                            map[nativetool.ID]*nativetool.SensitiveToolWitness
}
type callbackParent struct {
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
			owner = &ownerScope{capture: capture, epoch: epoch, revision: revision, context: ownerContext, cancel: cancel, sessions: map[nativetool.ID]*session{}, expires: now.Add(e.limits.SessionIdle)}
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
		if err = e.dependencies.Store.RegisterSession(ctx, nativetool.SessionInput{ID: id, Capture: capture, Plan: plan, AssignmentRevision: uint64(c.lease.Assignment().Revision), ExpectedAuthorityRevision: owner.revision, OwnerEpoch: owner.epoch, ExpiresAt: owner.expires}); err != nil {
			e.budget.release(memory)
			return nil, err
		}
		s := &session{owner: owner, id: id, plan: plan, assignment: uint64(c.lease.Assignment().Revision), revision: 1, last: now, timeLimit: owner.expires, authorization: c.request.Header.Get("Authorization"), cookie: c.request.Header.Get("Cookie"), active: map[string]bool{}, parents: map[string]*callbackParent{}, events: map[string][]byte{}}
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
		matches := s.wire == wire && !s.closed && s.timeLimit.After(now) && now.Sub(s.last) < e.limits.SessionIdle && s.authorization == c.request.Header.Get("Authorization") && s.cookie == c.request.Header.Get("Cookie") && s.assignment == uint64(c.lease.Assignment().Revision) && s.plan.EnvironmentDigest() == plan.EnvironmentDigest() && s.plan.Route() == plan.Route()
		if matches {
			s.last = now
		}
		s.mu.Unlock()
		if matches {
			return s, nil
		}
	}
	return nil, errSession
}
func (e *executor) acquireRPC(s *session, id nativetool.ID, r rpc) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := string(r.id)
	if key == "" {
		key = "operation:" + id.String()
	}
	if s.closed || len(s.active) >= e.limits.ConcurrentRPCsPerSession {
		return nil, errBudget
	}
	if s.active[key] {
		return nil, nativetool.ErrReplay
	}
	if r.kind == nativetool.RequestToolsList {
		cursor, _ := text(r.params["cursor"])
		if cursor != s.nextCursor {
			return nil, errSession
		}
		for active := range s.active {
			if active == "catalog-page" {
				return nil, errSession
			}
		}
		if len(s.pages) >= e.limits.CatalogPages {
			return nil, errBudget
		}
	}
	s.active[key] = true
	return func() { s.mu.Lock(); delete(s.active, key); s.mu.Unlock() }, nil
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
		if wire == "" || len(wire) > e.limits.OpaqueIDBytes {
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
		if _, seen := s.events[last]; !seen {
			return errSession
		}
	}
	return nil
}
func (e *executor) Revoke(ctx context.Context, capture captureidentity.Reference, reason RevocationReason) error {
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
	if owner != nil {
		owner.revoked = true
		owner.cancel()
		for _, s := range owner.sessions {
			s.mu.Lock()
			s.closed = true
			if s.hasher != nil {
				s.hasher.Close()
			}
			s.authorization = ""
			s.cookie = ""
			for _, parent := range s.parents {
				if parent.release != nil {
					parent.release()
					parent.release = nil
				}
			}
			clear(s.parents)
			clear(s.elicitations)
			clear(s.tools)
			clear(s.events)
			s.pages = nil
			s.catalog = nil
			retained := s.retained
			s.retained = 0
			s.mu.Unlock()
			e.budget.release(retained)
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
		e.budget.release(int64(claims) * 128)
		return result
	case <-ctx.Done():
		return errors.Join(result, ctx.Err())
	}
}
func equalEvent(previous, event []byte) bool { return bytes.Equal(previous, event) }
