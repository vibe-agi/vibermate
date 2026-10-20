// Package nativemcp owns bounded native MCP parsing and the single native send
// consumer. It does not grant model, workspace or managed-account authority.
package nativemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/vibe-agi/vibermate/internal/recording"
	"io"
	"mime"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

type Executor interface {
	PendingPreview(context.Context, captureidentity.Reference, nativetool.ID, nativetool.Digest) (PendingPreview, error)
	Serve(context.Context, CapturedRequest, http.ResponseWriter) Terminal
	Revoke(context.Context, captureidentity.Reference, RevocationReason) error
	Close(context.Context) error
}
type IDSource interface {
	NewNativeOperationID(context.Context) (nativetool.ID, error)
	NewOwnerEpoch(context.Context) (uint64, error)
}

// AuthorityStore contains only required lifecycle and physical delivery authority.
type AuthorityStore interface {
	nativetool.LifecycleAuthority
	nativetool.ExecutionAuthority
}

type Dependencies struct {
	EgressDefaults  egressprofile.Resolver
	OwnerContext    context.Context
	Transport       originaltransport.Transport
	Store           AuthorityStore
	ToolDecisions   exchange.ToolDecisionGate
	Offline         offlinehold.Coordinator
	Clock           func() time.Time
	IDs             IDSource
	ContentProfiles ContentProfiles
	ContentRecorder ContentRecorder
	RecordingScopes recording.ScopeIssuer
}
type Outcome string

const (
	OutcomeCompleted Outcome = "completed"
	OutcomeFailed    Outcome = "failed"
	OutcomeCanceled  Outcome = "canceled"
	OutcomeUncertain Outcome = "uncertain"
	OutcomeRejected  Outcome = "rejected"
)

type Reason string

const (
	ReasonPermissionOff      Reason = "permission_off"
	ReasonTargetUnproven     Reason = "target_unproven"
	ReasonProtocolInvalid    Reason = "protocol_invalid"
	ReasonSessionInvalid     Reason = "session_invalid"
	ReasonApprovalRejected   Reason = "approval_rejected"
	ReasonApprovalExpired    Reason = "approval_expired"
	ReasonRevoked            Reason = "revoked"
	ReasonExecutionUncertain Reason = "execution_uncertain"
	ReasonBudgetExceeded     Reason = "budget_exceeded"
	ReasonStorageUnavailable Reason = "storage_unavailable"
	ReasonStreamDetached     Reason = "stream_detached"
)

type RevocationReason string

const (
	RevocationPermissionOff     RevocationReason = "permission_off"
	RevocationCaptureStopped    RevocationReason = "capture_stopped"
	RevocationAssignmentApplied RevocationReason = "assignment_applied"
	RevocationOwnerChanged      RevocationReason = "owner_changed"
	RevocationShutdown          RevocationReason = "shutdown"
)

type ContentObservationReason string

const ContentProfileUnavailable ContentObservationReason = "content_profile_unavailable"
const ContentRecordingUnavailable ContentObservationReason = "content_recording_unavailable"
const ContentAuthorityUnavailable = ContentRecordingUnavailable

type Terminal struct {
	OperationID       nativetool.ID
	Outcome           Outcome
	Reason            Reason
	BytesIn, BytesOut int64
	ContentReason     ContentObservationReason
}
type CapturedRequest struct {
	lease     *captureassignment.RequestLease
	admission captureadmission.Admission
	request   *http.Request
	id        nativetool.ID
	used      *atomic.Bool
}

func NewCapturedRequest(lease *captureassignment.RequestLease, admission captureadmission.Admission, request *http.Request, id nativetool.ID) (CapturedRequest, error) {
	if lease == nil || request == nil || request.URL == nil || admission.Validate() != nil {
		return CapturedRequest{}, errProtocol
	}
	if _, err := nativetool.NewID(string(id)); err != nil {
		return CapturedRequest{}, err
	}
	plan, ok := lease.Plan().ClientToolPlan()
	capture, err := admission.CaptureReference()
	if !ok || plan.Validate() != nil || err != nil || lease.Assignment().Capture != capture {
		return CapturedRequest{}, nativetool.ErrDenied
	}
	if err = exactTarget(request, plan); err != nil {
		return CapturedRequest{}, err
	}
	if headerMemory(request.Header) > DefaultLimits().BufferBytes {
		return CapturedRequest{}, errBudget
	}
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	cloned.Body = request.Body
	return CapturedRequest{lease, admission, cloned, id, &atomic.Bool{}}, nil
}

type executor struct {
	dependencies  Dependencies
	limits        Limits
	budget        memoryBudget
	mu            sync.Mutex
	scopes        map[string]*ownerScope
	claimed       map[nativetool.ID]*nativeClaim
	lastClaimKey  uint64
	pending       int
	parentsMu     sync.Mutex
	parentPending int
	closed        bool
	previews      map[nativetool.ID]*pendingPreviewEntry
	previewBytes  int
	wg            sync.WaitGroup
}

func New(d Dependencies, l Limits) (Executor, error) {
	if d.OwnerContext == nil || d.Transport == nil || d.Store == nil || d.ToolDecisions == nil || d.Offline == nil || d.Clock == nil || d.IDs == nil {
		return nil, nativetool.ErrInvalid
	}
	if err := l.validate(); err != nil {
		return nil, err
	}
	if err := d.OwnerContext.Err(); err != nil {
		return nil, err
	}
	if err := d.Store.Recover(d.OwnerContext, d.Clock().UTC()); err != nil {
		return nil, err
	}
	return &executor{dependencies: d, limits: l, budget: memoryBudget{maximum: l.BufferBytes}, scopes: map[string]*ownerScope{}, claimed: map[nativetool.ID]*nativeClaim{}}, nil
}
func reasonFor(err error) Reason {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return ReasonRevoked
	case errors.Is(err, errBudget):
		return ReasonBudgetExceeded
	case errors.Is(err, nativetool.ErrUnavailable):
		return ReasonStorageUnavailable
	case errors.Is(err, nativetool.ErrReplay):
		return ReasonExecutionUncertain
	case errors.Is(err, nativetool.ErrDenied):
		return ReasonPermissionOff
	case errors.Is(err, errSession) || errors.Is(err, nativetool.ErrConflict):
		return ReasonSessionInvalid
	default:
		return ReasonProtocolInvalid
	}
}
func reject(w http.ResponseWriter, id nativetool.ID, reason Reason) Terminal {
	return rejectStatus(w, id, reason, 0)
}

func rejectStatus(w http.ResponseWriter, id nativetool.ID, reason Reason, status int) Terminal {
	if tracked, ok := w.(*nativeWriter); ok && tracked.failAfterHeaders() {
		outcome := OutcomeRejected
		if reason == ReasonRevoked {
			outcome = OutcomeCanceled
		}
		return Terminal{OperationID: id, Outcome: outcome, Reason: reason}
	}
	if tracked, ok := w.(*nativeWriter); ok && len(tracked.correlation) > 0 && (reason == ReasonApprovalRejected || reason == ReasonApprovalExpired) {
		encoded, _ := json.Marshal(struct {
			Version string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Error   struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}{Version: "2.0", ID: tracked.correlation, Error: struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}{-32003, string(reason)}})
		if int64(len(encoded)) > tracked.maximum {
			w.WriteHeader(http.StatusBadRequest)
			return Terminal{OperationID: id, Outcome: OutcomeRejected, Reason: ReasonBudgetExceeded}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		tracked.terminal = true
		_, _ = w.Write(encoded)
		return Terminal{OperationID: id, Outcome: OutcomeRejected, Reason: reason}
	}
	if status == 0 {
		status = http.StatusBadRequest
	}
	if reason == ReasonExecutionUncertain {
		status = http.StatusConflict
	} else if reason == ReasonPermissionOff || reason == ReasonApprovalRejected {
		status = http.StatusForbidden
	} else if reason == ReasonStorageUnavailable {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"error":"`+string(reason)+`"}`)
	outcome := OutcomeRejected
	if reason == ReasonRevoked {
		outcome = OutcomeCanceled
	}
	return Terminal{OperationID: id, Outcome: outcome, Reason: reason}
}

func (e *executor) beginWork() (func(), error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, context.Canceled
	}
	if e.pending >= e.limits.Pending {
		return nil, errBudget
	}
	e.pending++
	e.wg.Add(1)
	var once sync.Once
	return func() { once.Do(func() { e.mu.Lock(); e.pending--; e.mu.Unlock(); e.wg.Done() }) }, nil
}

func (e *executor) Serve(ctx context.Context, c CapturedRequest, w http.ResponseWriter) (terminal Terminal) {
	if ctx == nil || w == nil || c.lease == nil || c.request == nil || c.used == nil || !c.used.CompareAndSwap(false, true) {
		if w != nil {
			return reject(w, c.id, ReasonProtocolInvalid)
		}
		return Terminal{OperationID: c.id, Outcome: OutcomeRejected, Reason: ReasonProtocolInvalid}
	}
	var claim nativeClaimHandle
	var effectiveBytes int64
	var finishWork func()
	defer func() {
		e.drainClaimConsumer(claim)
		if effectiveBytes != 0 {
			e.budget.release(effectiveBytes)
		}
		if finishWork != nil {
			finishWork()
		}
	}()
	defer c.lease.Release()
	defer func() {
		clear(c.request.Header)
		if c.request.Body != nil {
			_ = c.request.Body.Close()
		}
	}()
	headers := headerMemory(c.request.Header)
	if !e.budget.acquire(headers) {
		return reject(w, c.id, ReasonBudgetExceeded)
	}
	defer e.budget.release(headers)
	tracked := &nativeWriter{ResponseWriter: w, maximum: e.limits.EventBytes}
	w = tracked
	var err error
	finishWork, err = e.beginWork()
	if err != nil {
		return reject(w, c.id, reasonFor(err))
	}
	call, cancel := context.WithTimeout(ctx, e.limits.CallLifetime)
	defer cancel()
	stopOwner := context.AfterFunc(e.dependencies.OwnerContext, cancel)
	defer stopOwner()
	inputClosed := make(chan struct{})
	stopInput := context.AfterFunc(call, func() {
		defer close(inputClosed)
		if c.request.Body != nil {
			_ = c.request.Body.Close()
		}
	})
	defer func() {
		if !stopInput() {
			<-inputClosed
		}
	}()
	plan, _ := c.lease.Plan().ClientToolPlan()
	if err := exactTarget(c.request, plan); err != nil {
		return reject(w, c.id, ReasonTargetUnproven)
	}
	var r rpc
	var release func()
	if err := validateRequestHeaders(c.request); err != nil {
		return reject(w, c.id, ReasonProtocolInvalid)
	}
	if c.request.Method != http.MethodPost && (c.request.ContentLength != 0 || len(c.request.TransferEncoding) > 0) {
		return reject(w, c.id, ReasonProtocolInvalid)
	}
	if c.request.Method == http.MethodPost {
		if err := validateRequestHeaders(c.request); err != nil {
			return reject(w, c.id, ReasonProtocolInvalid)
		}
		raw, free, err := e.readBounded(c.request.Body, e.limits.RequestBytes)
		if err != nil {
			if errors.Is(err, errIncomingBodyTooLarge) {
				return rejectStatus(w, c.id, ReasonBudgetExceeded, http.StatusRequestEntityTooLarge)
			}
			return reject(w, c.id, reasonFor(err))
		}
		defer free()
		cost := 2 * jsonAllocation(raw)
		if !e.budget.acquire(cost) {
			return reject(w, c.id, ReasonBudgetExceeded)
		}
		defer e.budget.release(cost)
		r, err = parseRequest(raw, e.limits)
		if err != nil {
			return reject(w, c.id, reasonFor(err))
		}
	} else if c.request.Method == http.MethodGet {
		r.kind = nativetool.RequestGetStream
	} else if c.request.Method == http.MethodDelete {
		r.kind = nativetool.RequestDeleteSession
	} else {
		return reject(w, c.id, ReasonProtocolInvalid)
	}
	s, err := e.admitSession(call, c, r, plan)
	if err != nil {
		return reject(w, c.id, reasonFor(err))
	}
	knownRPCFailure := false
	r.resumeOrdinal = -1
	if cursor := c.request.Header.Get("Last-Event-Id"); cursor != "" {
		if r.kind != nativetool.RequestGetStream {
			return reject(w, c.id, ReasonProtocolInvalid)
		}
		r.stream, r.resumeOrdinal, err = e.attachStream(c, s, cursor)
		if err != nil {
			return reject(w, c.id, reasonFor(err))
		}
		defer e.detachStream(s, r.stream)
		s.mu.Lock()
		terminalStream := r.stream.terminal
		s.mu.Unlock()
		if terminalStream {
			return e.replayTerminal(call, c, s, r.stream, r.resumeOrdinal, w)
		}
		stop := context.AfterFunc(r.stream.context, cancel)
		defer stop()
	}
	r.failure = &knownRPCFailure
	renewalUnavailable := false
	r.renewalUnavailable = &renewalUnavailable
	r.cancel = cancel
	r.retention = &retentionState{}
	collectionToken := uint64(0)
	r.collectionToken = &collectionToken
	if r.kind == nativetool.RequestInitialize || r.kind == nativetool.RequestToolsList || r.kind == nativetool.RequestToolsCall || r.kind == nativetool.RequestPing {
		tracked.correlation = r.id
	}
	release, err = e.acquireRPC(s, c.id, r)
	if err != nil {
		return reject(w, c.id, reasonFor(err))
	}
	defer release()
	if r.kind == nativetool.RequestToolsList {
		r.collection = collectionToken
	}
	stopScope := context.AfterFunc(s.context, cancel)
	defer stopScope()
	selectedPlan, err := e.operationPlan(call, r, s)
	if err != nil {
		if r.kind == nativetool.RequestInitialize {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
			err = errors.Join(err, e.closeSession(cleanup, s))
			cancel()
		}
		return reject(w, c.id, reasonFor(err))
	}
	charge, err := effectiveEgressMemory(selectedPlan)
	if err != nil || !e.budget.acquire(charge) {
		return reject(w, c.id, ReasonBudgetExceeded)
	}
	effectiveBytes = charge
	binding, intents, witness, err := e.bindRequest(c, r, s, selectedPlan)
	if err != nil {
		return reject(w, c.id, reasonFor(err))
	}
	// The binding may select the actual registered parent. The probe and wire
	// must consume that same per-operation fact, never the ingress/session plan.
	plan = binding.Plan()
	s.mu.Lock()
	activeKey := string(r.id)
	if activeKey == "" {
		activeKey = "operation:" + c.id.String()
	}
	if active := s.active[activeKey]; active != nil {
		active.binding = binding
	}
	s.mu.Unlock()
	s.mu.Lock()
	sensitiveToolWitness := s.tools[c.id]
	s.mu.Unlock()
	if sensitiveToolWitness != nil {
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			op, err := e.dependencies.Store.Get(cleanup, c.id)
			if err == nil && op.CompletedAt != nil {
				s.mu.Lock()
				if s.tools[c.id] == sensitiveToolWitness {
					_ = s.hasher.RetireToolCall(*sensitiveToolWitness)
					delete(s.tools, c.id)
					s.retained -= 2048
					e.budget.release(2048)
				}
				s.mu.Unlock()
			}
		}()
	}
	rpcID, hasRPC := binding.RPCIDDigest()
	requestDigest := binding.RequestDigest()
	var inbound *nativetool.Digest
	if hasRPC {
		inbound = &rpcID
	}
	sessionID := s.id
	received, err := e.dependencies.Store.Receive(call, nativetool.ReceiveInput{ID: c.id, Capture: s.owner.capture, EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), ConnectionID: "native:" + s.id.String(), TargetOrigin: plan.Route().TargetOrigin.String(), Method: c.request.Method, Kind: r.kind, SessionID: &sessionID, RPCIDDigest: inbound, RequestDigest: func() *nativetool.Digest {
		if inbound != nil {
			return &requestDigest
		}
		return nil
	}(), Recording: plan.ContentRecording(), ObservedAt: e.dependencies.Clock().UTC()})
	if err != nil {
		return reject(w, c.id, reasonFor(err))
	}
	if received.Duplicate {
		return reject(w, received.OperationID, ReasonExecutionUncertain)
	}
	if r.kind == nativetool.RequestToolsCall && len(intents) > 0 && e.dependencies.RecordingScopes != nil && !received.CreatedAt.IsZero() {
		stamp := recording.StampOriginal(e.dependencies.RecordingScopes, received.RecordingScope)
		if stamp.Current() && stamp.Fence() == received.RecordingScope.Fence && stamp.MatchesCapture(received.RecordingScope.Capture) {
			r.retention.stamp = stamp
			r.retention.identity = nativetool.ContentIdentity{OperationID: received.OperationID, Scope: received.RecordingScope, EnvironmentID: plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(), EnvironmentDigest: plan.EnvironmentDigest(), Kind: r.kind, ToolName: intents[0].Call.Name, TargetOrigin: plan.Route().TargetOrigin.String(), CredentialOwner: string(plan.Route().CredentialOwner), Recording: plan.ContentRecording(), CreatedAt: received.CreatedAt, ExpiresAt: received.CreatedAt.Add(time.Duration(plan.ContentRecording().RetentionDays) * 24 * time.Hour)}
		}
	}
	if r.kind == nativetool.RequestServerReply {
		defer e.retireFailedReplyParent(s, string(r.id), c.id)
	}
	defer func() {
		if terminal.Outcome != OutcomeRejected && terminal.Outcome != OutcomeCanceled {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		op, err := e.dependencies.Store.Get(cleanup, c.id)
		if err != nil {
			return
		}
		if op.State == "awaiting_decision" && op.ApprovalID != nil {
			approval, err := e.dependencies.Store.GetApproval(cleanup, *op.ApprovalID)
			if err == nil {
				_, _ = e.dependencies.Store.CloseApproval(cleanup, approval.ID, approval.Revision, nativetool.ApprovalClosureCancelled, e.dependencies.Clock().UTC())
			}
			return
		}
		if op.State == "started" {
			if e.dependencies.Store.Finish(cleanup, c.id, nativetool.TerminalInput{Outcome: "uncertain", Reason: "execution_uncertain", ObservedAt: e.dependencies.Clock().UTC()}) == nil {
				e.notePhysicalTerminal(claim)
			}
			_ = e.Revoke(cleanup, s.owner.capture, RevocationOwnerChanged)
			return
		}
		if op.State == "received" || op.State == "validated" {
			outcome, reason := "failed", "rpc_error"
			if terminal.Reason == ReasonRevoked {
				outcome, reason = "canceled", "revoked"
			}
			if e.dependencies.Store.Finish(cleanup, c.id, nativetool.TerminalInput{Outcome: outcome, Reason: reason, ObservedAt: e.dependencies.Clock().UTC()}) == nil {
				e.notePhysicalTerminal(claim)
			}
		}
	}()
	if r.stream != nil {
		err = e.dependencies.Store.BindRecoveryGET(call, c.id, binding, r.stream.authority, e.dependencies.Clock().UTC())
	} else if r.kind == nativetool.RequestServerReply {
		s.mu.Lock()
		parent := s.parents[string(r.id)]
		var authority nativetool.CallbackAuthority
		if parent != nil {
			authority = parent.authority
		}
		s.mu.Unlock()
		if witness == nil {
			return reject(w, c.id, ReasonSessionInvalid)
		}
		err = e.dependencies.Store.BindObservedReply(call, c.id, binding, authority, *witness, e.dependencies.Clock().UTC())
	} else {
		err = e.dependencies.Store.Bind(call, c.id, binding, e.dependencies.Clock().UTC())
	}
	if err != nil {
		return reject(w, c.id, reasonFor(err))
	}
	if r.kind == nativetool.RequestToolsCall || r.kind == nativetool.RequestServerReply {
		decisionContext, err := exchange.NewToolDecisionContext(binding.Plan().PolicySet(), "", false, nil, nil)
		if err != nil {
			return reject(w, c.id, ReasonProtocolInvalid)
		}
		var decisionRequest exchange.ToolDecisionRequest
		if sensitiveToolWitness != nil {
			decisionRequest, err = exchange.NewSensitiveClientToolDecisionRequest(c.id, binding, decisionContext, intents, *sensitiveToolWitness)
		} else {
			decisionRequest, err = exchange.NewClientToolDecisionRequest(c.id, binding, decisionContext, intents, witness)
		}
		if err != nil {
			return reject(w, c.id, ReasonProtocolInvalid)
		}
		if err = e.retainPreview(call, c.id, binding, intents, r, s); err != nil {
			return reject(w, c.id, reasonFor(err))
		}
		decision, err := e.dependencies.ToolDecisions.Decide(call, decisionRequest)
		e.discardPreview(c.id)
		if err != nil {
			if call.Err() != nil {
				return reject(w, c.id, ReasonRevoked)
			}
			return reject(w, c.id, reasonFor(err))
		}
		if decision.Outcome != exchange.ToolDecisionApproved {
			reason := ReasonApprovalRejected
			if decision.ReasonCode == "native_approval_expired" {
				reason = ReasonApprovalExpired
			}
			return reject(w, c.id, reason)
		}
	}
	outgoing, err := outbound(call, c.request, r.raw, plan)
	if err != nil {
		return reject(w, c.id, reasonFor(err))
	}
	defer clear(outgoing.Header)
	action, err := e.dependencies.Offline.BeginAction(call, offlinehold.ActionRequest{ActionID: "native:" + c.id.String()})
	if err != nil {
		return reject(w, c.id, ReasonRevoked)
	}
	defer action.Release()
	lease, err := e.dependencies.Offline.Acquire(call, offlinehold.AcquireRequest{RequestID: "native:" + c.id.String(), Action: action, Target: nativeProbe(call, plan), SizeBytes: int64(len(r.raw))})
	if err != nil {
		return reject(w, c.id, ReasonRevoked)
	}
	defer lease.Release()
	if call.Err() != nil {
		return reject(w, c.id, ReasonRevoked)
	}
	attempt, err := e.dependencies.IDs.NewNativeOperationID(call)
	if err != nil {
		return reject(w, c.id, ReasonStorageUnavailable)
	}
	startedAt := e.dependencies.Clock().UTC()
	receipt, err := e.dependencies.Store.Start(call, c.id, attempt.String(), s.owner.revision, startedAt)
	if err != nil {
		return reject(w, c.id, reasonFor(err))
	}
	// Only this immediate Start's exact receipt can qualify its own attempt.
	// A valid receipt belonging to some other operation must never send here.
	if receipt.OperationID() == "" || receipt.OperationID() != c.id || receipt.AttemptID() != attempt.String() {
		return e.finishFailure(c.id, s, w, nativetool.ErrDenied, true, claim)
	}
	headerTimer := time.AfterFunc(e.limits.Headers, cancel)
	send := e.sendClaimed(call, outgoing, plan, receipt, s, r.stream != nil)
	claim = send.claim
	headerTimer.Stop()
	response, err := send.response, send.err
	if err != nil {
		if send.phase == nativeSendFirstClaimUnsent && knownUnsentKind(r.kind) {
			return e.finishKnownUnsent(c.id, s, w, err, r, binding, receipt, claim)
		}
		if r.stream != nil {
			return e.finishRecoveryTransportFailure(c, s, r.stream, receipt, nil, claim)
		}
		return e.finishFailure(c.id, s, w, err, true, claim)
	}
	if response == nil || response.Body == nil {
		return e.finishFailure(c.id, s, w, errProtocol, true, claim)
	}
	defer response.Body.Close()
	counted := &countedResponseBody{ReadCloser: response.Body}
	response.Body = counted
	responseClosed := make(chan struct{})
	closeResponse := context.AfterFunc(call, func() {
		defer close(responseClosed)
		_ = response.Body.Close()
	})
	defer func() {
		if !closeResponse() {
			<-responseClosed
		}
	}()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return e.finishFailure(c.id, s, w, errProtocol, true, claim)
	}
	if response.StatusCode == 404 || response.StatusCode >= 500 {
		return e.finishFailure(c.id, s, w, errSession, true, claim)
	}
	if err = e.validateResponseSession(c, r, s, response); err != nil {
		return e.finishFailure(c.id, s, w, err, true, claim)
	}
	media, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode == http.StatusOK && media == "text/event-stream" && r.stream == nil {
		r.stream, err = e.openStream(call, c, r, s, binding, receipt, startedAt, claim, effectiveBytes)
		if err != nil {
			return e.finishFailure(c.id, s, w, err, true, claim)
		}
		defer e.detachStream(s, r.stream)
		stop := context.AfterFunc(r.stream.context, cancel)
		defer stop()
	}
	if err = e.consumeResponse(call, c, r, s, binding, response, w); err != nil {
		if r.stream != nil {
			return e.finishStreamAttempt(c, r, s, receipt, counted, w, err, claim)
		}
		return e.finishFailure(c.id, s, w, err, true, claim)
	}
	contentReason := ContentObservationReason("")
	if r.kind == nativetool.RequestToolsCall && plan.ContentRecording().Mode == environment.ContentRecordingFull {
		if !r.retention.known {
			contentReason = ContentProfileUnavailable
		} else {
			arguments := r.params["arguments"]
			if len(arguments) == 0 {
				arguments = emptyToolArguments[:]
			}
			e.observeContent(r, nativetool.ContentToolArguments, arguments)
		}
	}
	var candidate *catalogCandidate
	if r.kind == nativetool.RequestToolsList && !knownRPCFailure {
		candidate, err = e.prepareCatalog(r, s)
		if err != nil {
			return e.finishFailure(c.id, s, w, err, true, claim)
		}
		defer e.releaseCatalogCandidate(candidate)
	}
	state, storedReason := "completed", "completed"
	if r.retention.unavailable {
		contentReason = ContentRecordingUnavailable
	}
	if knownRPCFailure {
		state, storedReason = "failed", "rpc_error"
	}
	if r.stream != nil {
		streamTerminal := e.finishStreamAttempt(c, r, s, receipt, counted, w, nil, claim)
		if streamTerminal.Outcome != OutcomeCompleted && !(knownRPCFailure && streamTerminal.Outcome == OutcomeFailed) {
			return streamTerminal
		}
	} else {
		err = e.dependencies.Store.Finish(call, c.id, nativetool.TerminalInput{Outcome: state, Reason: storedReason, BytesIn: counted.bytes.Load(), BytesOut: int64(len(r.raw)), ObservedAt: e.dependencies.Clock().UTC()})
		if err == nil {
			e.notePhysicalTerminal(claim)
		}
	}
	if err != nil {
		return e.finishFailure(c.id, s, w, err, true, claim)
	}
	if r.kind == nativetool.RequestInitialized && !knownRPCFailure {
		revision, err := e.dependencies.Store.ReadySession(call, s.id, s.revision, e.dependencies.Clock().UTC())
		if err != nil {
			renewalUnavailable = true
			e.fencePublication(s)
		} else {
			s.mu.Lock()
			s.revision = revision
			s.ready = true
			s.mu.Unlock()
		}
	}
	if r.kind == nativetool.RequestToolsList && !knownRPCFailure {
		if renewalUnavailable {
			e.fencePublication(s)
		} else if err = e.publishCatalog(call, candidate, s); err != nil {
			renewalUnavailable = true
			e.fencePublication(s)
		}
	}
	if r.kind == nativetool.RequestServerReply {
		s.mu.Lock()
		if parent := s.parents[string(r.id)]; parent != nil {
			parent.consumed = true
			if parent.sensitive != nil {
				_ = s.hasher.RetireParent(*parent.sensitive)
				parent.sensitive = nil
			}
			if parent.release != nil {
				parent.release()
				parent.release = nil
			}
			delete(s.parents, string(r.id))
			if parent.kind == "url" {
				var value map[string]json.RawMessage
				_ = json.Unmarshal(r.object["result"], &value)
				action, _ := text(value["action"])
				if action != "accept" {
					if _, exists := s.elicitations[parent.elicitationID]; exists {
						charge := int64(128 + len(parent.elicitationID))
						delete(s.elicitations, parent.elicitationID)
						s.retained -= charge
						e.budget.release(charge)
					}
				}
			}
		}
		s.mu.Unlock()
	}
	if r.kind == nativetool.RequestToolsCall {
		e.retireCompletedParents(call, s, c.id)
	}
	if r.kind == nativetool.RequestDeleteSession {
		if err = e.closeSession(context.Background(), s); err != nil {
			return Terminal{OperationID: c.id, Outcome: OutcomeCompleted, Reason: ReasonStorageUnavailable, BytesIn: counted.bytes.Load(), BytesOut: int64(len(r.raw))}
		}
	}
	if r.kind == nativetool.RequestCancel && !knownRPCFailure {
		if err = e.cancelTarget(s, r); err != nil {
			return Terminal{OperationID: c.id, Outcome: OutcomeCompleted, Reason: ReasonStorageUnavailable, BytesIn: counted.bytes.Load(), BytesOut: int64(len(r.raw))}
		}
	}
	outcome, reason := OutcomeCompleted, Reason("")
	if knownRPCFailure {
		outcome, reason = OutcomeFailed, ReasonProtocolInvalid
	}
	if renewalUnavailable {
		reason = ReasonStorageUnavailable
	}
	return Terminal{OperationID: c.id, Outcome: outcome, Reason: reason, BytesIn: counted.bytes.Load(), BytesOut: int64(len(r.raw)), ContentReason: contentReason}
}

// Conservative full canonical size includes escaped strings plus512 bytes for
// the added value/copies. The shared immutable fact transfers to retained stream
// ownership; SQL evidence does not own or justify a local-memory refund.
func effectiveEgressMemory(plan environment.ClientToolPlan) (int64, error) {
	resolved, err := plan.ResolvedEgress()
	if err != nil {
		return 0, err
	}
	encoded, err := json.Marshal(resolved.Profile())
	if err != nil || len(encoded) > nativetool.MaxEffectiveProfileJSONBytes {
		return 0, errBudget
	}
	return 512 + int64(len(encoded)), nil
}

func (e *executor) operationPlan(ctx context.Context, r rpc, s *session) (environment.ClientToolPlan, error) {
	s.mu.Lock()
	plan := s.plan
	if r.kind == nativetool.RequestServerReply {
		parent := s.parents[string(r.id)]
		if parent == nil || parent.consumed {
			s.mu.Unlock()
			return environment.ClientToolPlan{}, errSession
		}
		plan = parent.binding.Plan()
		s.mu.Unlock()
		if _, err := plan.ResolvedEgress(); err != nil {
			return environment.ClientToolPlan{}, err
		}
		return plan, nil
	}
	if r.stream != nil {
		plan = r.stream.binding.Plan()
		s.mu.Unlock()
		if _, err := plan.ResolvedEgress(); err != nil {
			return environment.ClientToolPlan{}, err
		}
		return plan, nil
	}
	s.mu.Unlock()
	// Only configuration is read here, outside owner/session locks. Bind/Start
	// still recheck the real current authority after this potentially blocked read.
	var resolved egressprofile.Resolved
	var err error
	if e.dependencies.EgressDefaults != nil {
		resolved, err = e.dependencies.EgressDefaults.Resolve(ctx, egressprofile.Inherit(), plan.Route().EgressProfile)
	} else if profile, explicit := plan.Route().EgressProfile.ExplicitProfile(); explicit {
		resolved, err = egressprofile.RestoreResolved(profile, egressprofile.SelectionFlow, 0)
	} else {
		err = egressprofile.ErrInvalidProfile
	}
	if err != nil {
		return environment.ClientToolPlan{}, errors.Join(nativetool.ErrUnavailable, err)
	}
	return plan.WithResolvedEgress(resolved)
}

func (e *executor) bindRequest(c CapturedRequest, r rpc, s *session, selected environment.ClientToolPlan) (nativetool.Binding, []protocolcore.ToolIntent, *nativetool.RegisteredParentWitness, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	input := nativetool.BindingInput{Kind: r.kind, Plan: selected, Capture: s.owner.capture, AssignmentRevision: s.assignment, OwnerEpoch: s.owner.epoch, SessionID: s.id, SessionRevision: s.revision, CatalogGeneration: s.generation, ExactRPC: r.raw}
	if s.catalog != nil {
		digest := s.catalog.Digest()
		input.CatalogDigest = &digest
	}
	var intents []protocolcore.ToolIntent
	if r.kind == nativetool.RequestToolsCall {
		if s.catalog == nil {
			return nativetool.Binding{}, nil, nil, errSession
		}
		name, _ := text(r.params["name"])
		args := r.params["arguments"]
		if len(args) == 0 {
			args = []byte("{}")
		}
		arguments, err := protocolcore.NewJSONObject(args, int(e.limits.RequestBytes))
		if err != nil {
			return nativetool.Binding{}, nil, nil, errProtocol
		}
		key, err := protocolcore.NewCallKey("native", c.id.String())
		if err != nil {
			return nativetool.Binding{}, nil, nil, errProtocol
		}
		intents = []protocolcore.ToolIntent{{ResponseID: c.id.String(), Ordinal: 0, Call: protocolcore.ToolCall{Key: key, Name: name, Arguments: arguments}}}
		input.Intents = intents
		input.CatalogEvidence = s.catalog
		// Required sensitive binding never depends on the optional retention
		// holder. Resolve/freeze once, then share that trusted classification
		// with approval preview and the synchronous recording borrow.
		var profile ContentProfile
		knownProfile := false
		if e.dependencies.ContentProfiles != nil {
			definition, found := s.catalog.Lookup(name)
			if found {
				candidate, available := e.dependencies.ContentProfiles.Resolve(s.plan.Route().Contract, definition)
				if available && candidate.valid(s.plan.Route().Contract, definition) {
					profile = frozenProfile(candidate)
					knownProfile = true
					if r.retention != nil {
						r.retention.known = true
						r.retention.profile = profile
					}
				}
			}
		}
		if meta := r.params["_meta"]; len(meta) > 0 {
			fields, _ := objectField(meta, e.limits)
			if observed, present := fields["callId"]; present {
				value, ok := text(observed)
				if !ok || value == "" || len(value) > e.limits.OpaqueIDBytes {
					return nativetool.Binding{}, nil, nil, errProtocol
				}
				input.ObservedCallID = []byte(value)
			}
		}
		if knownProfile {
			fields, err := objectField(args, e.limits)
			if err != nil {
				return nativetool.Binding{}, nil, nil, err
			}
			sensitive := false
			for _, path := range profile.arguments {
				if containsPath(fields, path) {
					sensitive = true
					break
				}
			}
			if sensitive {
				paths := make([]string, len(profile.arguments))
				for index, path := range profile.arguments {
					paths[index] = "/params/arguments" + path
				}
				observation, err := nativetool.NewSensitiveObservation("tools/call", paths)
				if err != nil {
					return nativetool.Binding{}, nil, nil, err
				}
				if !e.budget.acquire(2048) {
					return nativetool.Binding{}, nil, nil, errBudget
				}
				binding, witness, err := s.hasher.BindToolCall(c.id, input, observation)
				if err != nil {
					e.budget.release(2048)
					return nativetool.Binding{}, nil, nil, err
				}
				s.tools[c.id] = &witness
				s.retained += 2048
				return binding, intents, nil, nil
			}
		}
	}
	if r.kind == nativetool.RequestServerReply {
		p := s.parents[string(r.id)]
		if p == nil || p.consumed {
			return nativetool.Binding{}, nil, nil, errSession
		}
		parent := p.binding
		input.Plan = parent.Plan()
		input.AssignmentRevision = parent.AssignmentRevision()
		input.SessionRevision = parent.SessionRevision()
		input.CatalogGeneration = parent.CatalogGeneration()
		input.CatalogDigest = nil
		if digest, ok := parent.CatalogDigest(); ok {
			input.CatalogDigest = &digest
		}
		input.ParentOperationID = &p.operation
		if p.sensitive != nil {
			if err := validateSensitiveReply(r, p.kind); err != nil {
				return nativetool.Binding{}, nil, nil, err
			}
			digest := p.sensitive.RequestDigest()
			input.ServerRequestDigest = &digest
			b, err := s.hasher.BindReply(input, *p.sensitive)
			if err != nil {
				return b, nil, nil, err
			}
			witness, err := p.sensitive.RegisteredReply(c.id, b, r.raw)
			return b, nil, &witness, err
		}
		witness, err := p.observer.ObserveReply(c.id, p.request, r.raw)
		if err != nil {
			return nativetool.Binding{}, nil, nil, err
		}
		digest := p.digest
		input.ServerRequestDigest = &digest
		b, err := nativetool.NewBinding(input)
		return b, nil, &witness, err
	}
	if r.kind == nativetool.RequestCancel {
		identity, err := identity(r.params["requestId"], e.limits.OpaqueIDBytes)
		if err != nil {
			return nativetool.Binding{}, nil, nil, err
		}
		target := s.active[string(identity)]
		if target == nil || target.binding.Digest() == (nativetool.Digest{}) {
			return nativetool.Binding{}, nil, nil, errSession
		}
		if s.tools[target.operation] != nil {
			binding, err := s.hasher.BindCancellation(input, target.binding)
			return binding, nil, nil, err
		}
	}
	b, err := nativetool.NewBinding(input)
	return b, intents, nil, err
}
func (e *executor) finishFailure(id nativetool.ID, s *session, w http.ResponseWriter, cause error, possible bool, claim nativeClaimHandle) Terminal {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reason := reasonFor(cause)
	outcome := OutcomeFailed
	state, storedReason := "failed", "transport_failed"
	if possible {
		outcome = OutcomeUncertain
		reason = ReasonExecutionUncertain
		state, storedReason = "uncertain", "execution_uncertain"
	}
	err := e.dependencies.Store.Finish(ctx, id, nativetool.TerminalInput{Outcome: state, Reason: storedReason, ObservedAt: e.dependencies.Clock().UTC()})
	if err == nil {
		e.notePhysicalTerminal(claim)
	}
	if possible {
		if barrierErr := e.dependencies.Store.MarkReplayBarrier(ctx, s.owner.capture, s.owner.revision, e.dependencies.Clock().UTC()); barrierErr != nil {
			err = errors.Join(err, barrierErr)
		}
		s.mu.Lock()
		exact := s.closed
		for _, active := range s.active {
			if active.operation == id && active.protocolCanceled {
				exact = true
			}
		}
		s.mu.Unlock()
		if !exact {
			_ = e.Revoke(ctx, s.owner.capture, RevocationOwnerChanged)
		}
	}
	if err != nil {
		reason = ReasonStorageUnavailable
	}
	if w != nil {
		reject(w, id, reason)
	}
	return Terminal{OperationID: id, Outcome: outcome, Reason: reason}
}
func (e *executor) readBounded(reader io.Reader, maximum int64) ([]byte, func(), error) {
	if reader == nil {
		reader = bytes.NewReader(nil)
	}
	if !e.budget.acquire(maximum) {
		return nil, func() {}, errBudget
	}
	buffer := make([]byte, maximum)
	count := 0
	emptyReads := 0
	for count < len(buffer) {
		n, err := reader.Read(buffer[count:])
		count += n
		if err == io.EOF {
			return buffer[:count], func() { e.budget.release(maximum) }, nil
		}
		if err != nil {
			e.budget.release(maximum)
			return nil, func() {}, err
		}
		if n == 0 {
			emptyReads++
			if emptyReads >= 100 {
				e.budget.release(maximum)
				return nil, func() {}, io.ErrNoProgress
			}
			continue
		}
		emptyReads = 0
	}
	var extra [1]byte
	for {
		n, err := reader.Read(extra[:])
		if n > 0 {
			e.budget.release(maximum)
			return nil, func() {}, errIncomingBodyTooLarge
		}
		if err == io.EOF {
			return buffer, func() { e.budget.release(maximum) }, nil
		}
		if err != nil {
			e.budget.release(maximum)
			return nil, func() {}, err
		}
		emptyReads++
		if emptyReads >= 100 {
			e.budget.release(maximum)
			return nil, func() {}, io.ErrNoProgress
		}
	}
}
