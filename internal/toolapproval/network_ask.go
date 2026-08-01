package toolapproval

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// NetworkAskOutcome is the answer a waiting connection receives. There is no
// third state: everything that is not an explicit allow is a deny, because
// this decision sits in front of a dial.
type NetworkAskOutcome struct {
	Allowed    bool
	ReasonCode string
}

// NetworkAskRequest is one connection waiting on a person. It carries the
// target and the ingress that asked, and nothing else: a connection that has
// not been dialled has no path, header, body, or credential to describe.
type NetworkAskRequest struct {
	IngressID string
	Host      string
	Port      uint16
}

func (request NetworkAskRequest) validate() error {
	if err := validateIdentity(
		"network ask ingress ID",
		request.IngressID,
		false,
	); err != nil {
		return err
	}
	if request.Host == "" ||
		len(request.Host) > 253 ||
		strings.ToLower(request.Host) != request.Host ||
		strings.ContainsAny(request.Host, " \t\r\n") {
		return fmt.Errorf("%w: network ask host is invalid", ErrInvalidApproval)
	}
	if request.Port == 0 {
		return fmt.Errorf("%w: network ask port is invalid", ErrInvalidApproval)
	}
	return nil
}

func (request NetworkAskRequest) authority() string {
	return request.Host + ":" + strconv.Itoa(int(request.Port))
}

// networkAskAggregateKey merges identical questions. Design 06 keys this on
// the kind, the ingress, the host, and the port, so a burst of connections to
// one host is one question. Fields are length-prefixed so two adjacent values
// cannot run together into the same key.
func networkAskAggregateKey(request NetworkAskRequest) string {
	digest := sha256.New()
	writeAggregateField(digest, string(KindNetworkAsk))
	writeAggregateField(digest, request.IngressID)
	writeAggregateField(digest, request.Host)
	writeAggregateField(digest, strconv.Itoa(int(request.Port)))
	return base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}

// AskNetwork blocks until a person decides, or until this wait fails. Every
// failure denies: this sits in front of a dial, so an error that produced an
// allow would be the one outcome worse than not asking at all.
func (authority *Authority) AskNetwork(
	ctx context.Context,
	request NetworkAskRequest,
) (NetworkAskOutcome, error) {
	if authority == nil {
		return denied("approval_unavailable"), nil
	}
	if err := request.validate(); err != nil {
		return denied("invalid_ask"), err
	}
	operation := ctx
	identifier, err := randomIdentifier(authority.random)
	if err != nil {
		return denied("approval_unavailable"), err
	}
	now := authority.clock.Now().UTC()
	aggregateKey := networkAskAggregateKey(request)
	record := Record{
		ID:            identifier,
		Revision:      1,
		Kind:          KindNetworkAsk,
		AggregateKey:  aggregateKey,
		SubjectRefs:   []string{request.authority()},
		SubjectLabels: []string{request.Host},
		Target:        Target{Host: request.Host, Port: request.Port},
		RequestCount:  1,
		WaiterCount:   1,
		State:         StatePending,
		CreatedAt:     now,
		ExpiresAt:     now.Add(authority.config.DecisionTimeout),
	}
	if err := record.Validate(); err != nil {
		return denied("invalid_ask"), err
	}

	authority.mu.Lock()
	if authority.closing {
		authority.mu.Unlock()
		return denied("runtime_stopping"), nil
	}
	authority.mu.Unlock()

	pending, entry, joined := authority.waiters.join(aggregateKey, record.ID)
	waitingOn := entry.recordID
	// Waiting is bounded from the moment the caller arrives, including the
	// wait for the question itself to become durable.
	timer := time.NewTimer(authority.config.DecisionTimeout)
	defer timer.Stop()
	if joined {
		// The entry is published before its record is written, so a joiner
		// waits for that write rather than counting itself onto a row that
		// does not exist yet.
		select {
		case <-entry.ready:
		case <-operation.Done():
			authority.departFrom(waitingOn, pending, "connection_canceled")
			return denied("connection_canceled"), nil
		case <-timer.C:
			authority.departFrom(waitingOn, pending, "approval_expired")
			return denied("approval_expired"), nil
		}
		if !authority.waiters.durable(entry) {
			authority.waiters.remove(waitingOn, pending)
			return denied("approval_unavailable"), nil
		}
		// The person sees one question with a true count rather than one
		// prompt per connection. The count is what the prompt displays, not
		// what it decides: a caller that merges onto a question in the moment
		// it is being answered still receives that answer, so a failed count
		// leaves the caller waiting rather than denying it.
		_, _ = authority.repository.Join(operation, waitingOn)
	} else {
		err := authority.repository.Create(operation, record)
		authority.waiters.publish(entry, err == nil)
		if err != nil {
			authority.waiters.remove(record.ID, pending)
			return denied("approval_unavailable"), err
		}
	}
	authority.mu.Lock()
	authority.notifyLocked()
	authority.mu.Unlock()

	var resolved Record
	select {
	case resolved = <-pending.result:
	case <-operation.Done():
		authority.departFrom(waitingOn, pending, "connection_canceled")
		return denied("connection_canceled"), nil
	case <-timer.C:
		authority.departFrom(waitingOn, pending, "approval_expired")
		return denied("approval_expired"), nil
	}
	authority.waiters.remove(waitingOn, pending)
	if resolved.State == StateAllowed {
		return NetworkAskOutcome{Allowed: true}, nil
	}
	reason := resolved.DecisionReason
	if reason == "" {
		reason = "denied"
	}
	return denied(reason), nil
}

// departFrom takes one caller out of a question. The question survives while
// anyone is still waiting on it, so one connection going away never answers
// for the others.
func (authority *Authority) departFrom(
	recordID string,
	pending *waiter,
	reason string,
) {
	authority.waiters.remove(recordID, pending)
	if authority.waiters.waiterCount(recordID) == 0 {
		authority.cancelBestEffort(recordID, reason)
		return
	}
	_, _ = authority.repository.Leave(context.Background(), recordID)
}

func denied(reason string) NetworkAskOutcome {
	return NetworkAskOutcome{ReasonCode: reason}
}
