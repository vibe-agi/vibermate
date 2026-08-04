package toolapproval

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchange"
)

const approvalIDBytes = 20

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

// DefaultClientRootGrace bounds the one ask that happens inside a program
// launch rather than inside a request.
//
// Every other ask interrupts something that was already going to wait: a
// connection is held open while its question is answered. The client-root ask
// is different — it sits between a person typing a command and that program
// starting, and nothing has begun yet. So it cannot borrow the installation's
// five-minute decision budget: an unanswered question would hold a terminal
// for five minutes before starting a client that was always going to start.
//
// The design names this shape directly — an ask may deny after a short grace
// (delivery and operations §7) — and the outcome is not a failure: a launch
// that cannot reach a person launches without a Root, which is where an
// uncatalogued program has always been.
const DefaultClientRootGrace = 30 * time.Second

type Config struct {
	DecisionTimeout time.Duration
	// ClientRootGrace overrides DefaultClientRootGrace. Zero selects the
	// default; it is never allowed to exceed DecisionTimeout, because that is
	// the installation's own statement about how long anyone may be kept
	// waiting on a person.
	ClientRootGrace time.Duration
}

func (config Config) Validate() error {
	if config.DecisionTimeout <= 0 {
		return errors.New("tool approval decision timeout must be positive")
	}
	if config.ClientRootGrace < 0 {
		return errors.New("tool approval client root grace must not be negative")
	}
	return nil
}

// clientRootGrace is the budget one in-launch ask actually gets.
func (config Config) clientRootGrace() time.Duration {
	grace := config.ClientRootGrace
	if grace <= 0 {
		grace = DefaultClientRootGrace
	}
	if grace > config.DecisionTimeout {
		return config.DecisionTimeout
	}
	return grace
}

func DefaultConfig() Config {
	return Config{
		DecisionTimeout: 5 * time.Minute,
		ClientRootGrace: DefaultClientRootGrace,
	}
}

type Options struct {
	Repository Repository
	Clock      Clock
	Random     io.Reader
	Config     Config
	// Remembered is told that a decision wrote a rule, in the same commit that
	// resolved the question. The rules in force have to follow that commit, or
	// the next connection would ask a question that was already answered.
	Remembered RememberedListener
}

// RememberedListener hears about answers that were remembered.
type RememberedListener interface {
	RulesRemembered(context.Context) error
}

func DefaultOptions(repository Repository) Options {
	return Options{
		Repository: repository,
		Clock:      SystemClock{},
		Random:     rand.Reader,
		Config:     DefaultConfig(),
	}
}

type waiter struct {
	result chan Record
}

type Authority struct {
	repository Repository
	clock      Clock
	random     io.Reader
	config     Config
	remembered RememberedListener
	recovery   Recovery

	mu      sync.Mutex
	closing bool
	active  int
	waiters *waiterRegistry
	// ephemeralSubjectLabels contains no-store display evidence, such as the
	// exact artifact path evaluated for a recognized-client launch. Durable
	// records keep only safe labels; this map exists for the current process and
	// is cleared as soon as the pending question terminates.
	ephemeralSubjectLabels map[string][]string
	changed                chan struct{}
}

func New(ctx context.Context, options Options) (*Authority, error) {
	if ctx == nil ||
		options.Repository == nil ||
		options.Clock == nil ||
		options.Random == nil ||
		options.Config.Validate() != nil {
		return nil, errors.New("tool approval dependencies are incomplete")
	}
	recovery, err := options.Repository.Recover(ctx, options.Clock.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("recover tool approvals: %w", err)
	}
	return &Authority{
		repository:             options.Repository,
		clock:                  options.Clock,
		random:                 options.Random,
		config:                 options.Config,
		remembered:             options.Remembered,
		recovery:               recovery,
		waiters:                newWaiterRegistry(),
		ephemeralSubjectLabels: make(map[string][]string),
		changed:                make(chan struct{}),
	}, nil
}

func (authority *Authority) Recovery() Recovery {
	if authority == nil {
		return Recovery{}
	}
	return authority.recovery
}

func (authority *Authority) Decide(
	ctx context.Context,
	request exchange.ToolDecisionRequest,
) (exchange.ToolDecision, error) {
	operation, finish, err := authority.begin(ctx)
	if err != nil {
		return exchange.ToolDecision{}, err
	}
	defer finish()
	intents := request.ToolIntents()
	if request.ExchangeID() == "" ||
		request.AccessID().String() == "" ||
		request.PlanRevision() == 0 ||
		request.PlanHash().IsZero() ||
		len(intents) == 0 ||
		len(intents) > MaxToolIntents {
		return exchange.ToolDecision{}, ErrInvalidApproval
	}
	callIDs := make([]string, len(intents))
	names := make([]string, len(intents))
	for index, intent := range intents {
		if err := intent.Validate(); err != nil {
			return exchange.ToolDecision{}, err
		}
		callIDs[index] = intent.Call.Key.WireID()
		names[index] = intent.Call.Name
	}
	identifier, err := randomIdentifier(authority.random)
	if err != nil {
		return exchange.ToolDecision{}, err
	}
	now := authority.clock.Now().UTC()
	record := Record{
		ID:       identifier,
		Revision: 1,
		Kind:     KindToolIntent,
		// One complete tool group in one Exchange is one question. A later
		// kind that repeats across events merges on its own stable key.
		AggregateKey:  toolIntentAggregateKey(request.ExchangeID(), callIDs),
		SubjectRefs:   callIDs,
		SubjectLabels: names,
		RequestCount:  1,
		WaiterCount:   1,
		ExchangeID:    request.ExchangeID(),
		AccessID:      request.AccessID(),
		PlanRevision:  request.PlanRevision(),
		PlanHash:      request.PlanHash(),
		State:         StatePending,
		CreatedAt:     now,
		ExpiresAt:     now.Add(authority.config.DecisionTimeout),
	}
	if err := record.Validate(); err != nil {
		return exchange.ToolDecision{}, err
	}
	authority.mu.Lock()
	if authority.closing {
		authority.mu.Unlock()
		return exchange.ToolDecision{}, ErrRuntimeStopping
	}
	authority.mu.Unlock()
	// An identical pending question joins the existing entry, so a repeat of
	// the same group is one prompt answered once rather than a second prompt.
	pending, entry, joined := authority.waiters.join(record.AggregateKey, record.ID)
	waitingOn := entry.recordID
	timer := time.NewTimer(authority.config.DecisionTimeout)
	defer timer.Stop()
	if joined {
		// The entry is published before its record is written. A joiner waits
		// for that write, so it never counts itself onto a row that does not
		// exist yet and never waits on a question that was never asked.
		select {
		case <-entry.ready:
		case <-operation.Done():
			authority.departFrom(waitingOn, pending, "exchange_canceled")
			return exchange.ToolDecision{}, operation.Err()
		case <-timer.C:
			authority.departFrom(waitingOn, pending, "approval_expired")
			return exchange.ToolDecision{
				Outcome:    exchange.ToolDecisionRejected,
				ReasonCode: "approval_expired",
			}, nil
		}
		if !authority.waiters.durable(entry) {
			authority.waiters.remove(waitingOn, pending)
			return exchange.ToolDecision{}, ErrInvalidApproval
		}
		// The prompt counts what is actually waiting on it. A stale count
		// never decides anything, so a caller that merges onto a question
		// being answered right now keeps waiting for that answer.
		_, _ = authority.repository.Join(operation, waitingOn)
	} else {
		err := authority.repository.Create(operation, record)
		authority.waiters.publish(entry, err == nil)
		if err != nil {
			authority.waiters.remove(record.ID, pending)
			return exchange.ToolDecision{}, fmt.Errorf(
				"persist tool approval: %w",
				err,
			)
		}
	}
	authority.mu.Lock()
	authority.notifyLocked()
	authority.mu.Unlock()
	var resolved Record
	select {
	case resolved = <-pending.result:
	case <-operation.Done():
		authority.departFrom(waitingOn, pending, "exchange_canceled")
		return exchange.ToolDecision{}, operation.Err()
	case <-timer.C:
		authority.departFrom(waitingOn, pending, "approval_expired")
		return exchange.ToolDecision{
			Outcome:    exchange.ToolDecisionRejected,
			ReasonCode: "approval_expired",
		}, nil
	}
	authority.waiters.remove(waitingOn, pending)
	switch resolved.State {
	case StateAllowed:
		return exchange.ToolDecision{Outcome: exchange.ToolDecisionApproved}, nil
	case StateDenied:
		return exchange.ToolDecision{
			Outcome:    exchange.ToolDecisionRejected,
			ReasonCode: resolved.DecisionReason,
		}, nil
	case StateCanceled, StateExpired:
		return exchange.ToolDecision{
			Outcome:    exchange.ToolDecisionRejected,
			ReasonCode: resolved.DecisionReason,
		}, nil
	default:
		return exchange.ToolDecision{}, errors.New("tool approval resolved to an invalid state")
	}
}

func (authority *Authority) GetApproval(
	ctx context.Context,
	approvalID string,
) (View, error) {
	operation, finish, err := authority.begin(ctx)
	if err != nil {
		return View{}, err
	}
	defer finish()
	record, err := authority.repository.Get(operation, approvalID)
	if err != nil {
		return View{}, err
	}
	return authority.viewOf(record), nil
}

func (authority *Authority) ListApprovals(
	ctx context.Context,
	request PageRequest,
) (Page, error) {
	if err := request.Validate(); err != nil {
		return Page{}, err
	}
	operation, finish, err := authority.begin(ctx)
	if err != nil {
		return Page{}, err
	}
	defer finish()
	records, err := authority.repository.List(operation, request)
	if err != nil {
		return Page{}, err
	}
	page := Page{Items: make([]View, len(records))}
	for index, record := range records {
		page.Items[index] = authority.viewOf(record)
	}
	return page, nil
}

func (authority *Authority) DecideApproval(
	ctx context.Context,
	command DecisionCommand,
) (View, error) {
	if err := command.Validate(); err != nil {
		return View{}, err
	}
	operation, finish, err := authority.begin(ctx)
	if err != nil {
		return View{}, err
	}
	defer finish()
	record, err := authority.repository.Decide(
		operation,
		command,
		authority.clock.Now().UTC(),
	)
	if err != nil {
		return View{}, err
	}
	// The rule landed with the decision. Putting it in force is what makes the
	// answer stick; a waiter released before that could still be re-asked.
	if remembers(record.DecisionScope) && authority.remembered != nil {
		if err := authority.remembered.RulesRemembered(operation); err != nil {
			return View{}, err
		}
	}
	authority.waiters.resolve(record.ID, record.Clone())
	view := authority.viewOf(record)
	authority.forgetEphemeralSubjectLabels(record.ID)
	authority.mu.Lock()
	authority.notifyLocked()
	authority.mu.Unlock()
	return view, nil
}

func (authority *Authority) Shutdown(ctx context.Context) error {
	if authority == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("tool approval shutdown context is nil")
	}
	authority.mu.Lock()
	if !authority.closing {
		authority.closing = true
		authority.notifyLocked()
	}
	authority.mu.Unlock()
	canceled, err := authority.repository.CancelPending(
		ctx,
		"runtime_stopping",
		authority.clock.Now().UTC(),
	)
	for _, record := range canceled {
		authority.waiters.resolve(record.ID, record.Clone())
	}
	authority.mu.Lock()
	clear(authority.ephemeralSubjectLabels)
	authority.notifyLocked()
	for authority.active != 0 {
		changed := authority.changed
		authority.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		}
		authority.mu.Lock()
	}
	authority.mu.Unlock()
	return err
}

func (authority *Authority) begin(
	ctx context.Context,
) (context.Context, func(), error) {
	if authority == nil || ctx == nil {
		return nil, nil, ErrInvalidApproval
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	authority.mu.Lock()
	if authority.closing {
		authority.mu.Unlock()
		return nil, nil, ErrRuntimeStopping
	}
	authority.active++
	authority.notifyLocked()
	authority.mu.Unlock()
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			authority.mu.Lock()
			authority.active--
			authority.notifyLocked()
			authority.mu.Unlock()
		})
	}, nil
}

func (authority *Authority) cancelBestEffort(
	approvalID string,
	reason string,
) {
	authority.forgetEphemeralSubjectLabels(approvalID)
	cancelContext, cancel := context.WithTimeout(
		context.Background(),
		time.Second,
	)
	defer cancel()
	_, _ = authority.repository.Cancel(
		cancelContext,
		approvalID,
		reason,
		authority.clock.Now().UTC(),
	)
}

func (authority *Authority) rememberEphemeralSubjectLabels(
	approvalID string,
	labels []string,
) {
	if authority == nil || approvalID == "" || len(labels) == 0 {
		return
	}
	authority.mu.Lock()
	if authority.ephemeralSubjectLabels == nil {
		authority.ephemeralSubjectLabels = make(map[string][]string)
	}
	if _, exists := authority.ephemeralSubjectLabels[approvalID]; !exists {
		authority.ephemeralSubjectLabels[approvalID] = slices.Clone(labels)
	}
	authority.mu.Unlock()
}

func (authority *Authority) forgetEphemeralSubjectLabels(approvalID string) {
	if authority == nil || approvalID == "" {
		return
	}
	authority.mu.Lock()
	delete(authority.ephemeralSubjectLabels, approvalID)
	authority.mu.Unlock()
}

func (authority *Authority) viewOf(record Record) View {
	view := ViewOf(record)
	if authority == nil {
		return view
	}
	authority.mu.Lock()
	labels := slices.Clone(authority.ephemeralSubjectLabels[record.ID])
	authority.mu.Unlock()
	if len(labels) == len(view.SubjectRefs) {
		view.SubjectLabels = labels
	}
	return view
}

func (authority *Authority) notifyLocked() {
	close(authority.changed)
	authority.changed = make(chan struct{})
}

func randomIdentifier(source io.Reader) (string, error) {
	value := make([]byte, approvalIDBytes)
	if _, err := io.ReadFull(source, value); err != nil {
		return "", fmt.Errorf("generate tool approval ID: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

var (
	_ exchange.ToolDecisionGate = (*Authority)(nil)
	_ Controller                = (*Authority)(nil)
)

// toolIntentAggregateKey identifies one complete tool group inside one
// Exchange. Two different groups are two questions even when their tool names
// match, so the call identities take part.
func toolIntentAggregateKey(exchangeID string, callIDs []string) string {
	digest := sha256.New()
	writeAggregateField(digest, string(KindToolIntent))
	writeAggregateField(digest, exchangeID)
	for _, callID := range callIDs {
		writeAggregateField(digest, callID)
	}
	return base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}

func writeAggregateField(digest hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write([]byte(value))
}
