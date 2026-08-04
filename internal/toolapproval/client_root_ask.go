package toolapproval

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"
)

// ClientRootAskOutcome is the answer a launch receives about whether this
// client may be given the local Root. Like every ask in front of an
// irreversible step, there is no third state: anything that is not an explicit
// allow denies, and the client launches without a Root.
//
// Denying is not a failure to launch. The client still runs behind the proxy;
// it just cannot complete a decrypted handshake, which is the same place an
// uncatalogued program has always been.
type ClientRootAskOutcome struct {
	Allowed    bool
	ReasonCode string
}

// ClientRootAskRequest is one launch waiting on a person.
//
// It carries who signed the artifact and what will be set for it, and nothing
// about what the client will do afterwards — at launch there is no request,
// host, or credential to describe. The signer identity is the subject rather
// than the path, because the decision is about a publisher and is meant to
// survive that publisher shipping a new build tomorrow.
type ClientRootAskRequest struct {
	// SignerID names the catalogued publisher entry that recognized this
	// artifact.
	SignerID string
	// SignerRevision is the entry's revision, so a changed entry asks again
	// instead of inheriting an answer given about different terms.
	SignerRevision uint64
	// SignedPath is the artifact the platform evaluated. It is shown to the
	// person and is not part of what a remembered answer is keyed on.
	SignedPath string
}

func (request ClientRootAskRequest) validate() error {
	if err := validateIdentity(
		"client root ask signer ID",
		request.SignerID,
		false,
	); err != nil {
		return err
	}
	if request.SignerRevision == 0 {
		return errors.New("client root ask signer revision is required")
	}
	if request.SignedPath == "" || len(request.SignedPath) > 4096 {
		return errors.New("client root ask signed path is required")
	}
	return nil
}

// subject identifies the publisher this decision is about. A new build from
// the same publisher produces the same subject, which is the point: the person
// is asked about Anthropic or OpenAI once, not about every release.
func (request ClientRootAskRequest) subject() string {
	return "client-signer:" + request.SignerID
}

func clientRootAskAggregateKey(request ClientRootAskRequest) string {
	digest := sha256.New()
	writeAggregateField(digest, string(KindClientRootAsk))
	writeAggregateField(digest, request.SignerID)
	writeAggregateField(
		digest,
		strconvUint(request.SignerRevision),
	)
	return base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}

// AskClientRoot blocks until a person decides whether a recognized client may
// receive the local Root.
//
// It exists because a release catalog cannot name every build a user base
// runs, so recognition by publisher has to be possible — and because widening
// who may receive the Root must not be something that happens quietly. Every
// failure denies.
func (authority *Authority) AskClientRoot(
	ctx context.Context,
	request ClientRootAskRequest,
) (ClientRootAskOutcome, error) {
	if authority == nil {
		return deniedClientRoot("approval_unavailable"), nil
	}
	if err := request.validate(); err != nil {
		return deniedClientRoot("invalid_ask"), err
	}
	grace := authority.config.clientRootGrace()
	allowed, reason, err := authority.awaitDecision(
		ctx,
		clientRootAskAggregateKey(request),
		// Nothing is open behind this one. A person is waiting at a terminal
		// for a program to start, so an unanswered question denies after a
		// grace and the client starts without a Root.
		grace,
		[]string{request.SignedPath},
		func(identifier string, now time.Time) Record {
			return Record{
				ID:           identifier,
				Revision:     1,
				Kind:         KindClientRootAsk,
				AggregateKey: clientRootAskAggregateKey(request),
				SubjectRefs:  []string{request.subject()},
				// The exact path is no-store evidence projected from process
				// memory while this question is pending. The durable row keeps
				// only the safe signer label.
				SubjectLabels: []string{request.SignerID},
				RequestCount:  1,
				WaiterCount:   1,
				State:         StatePending,
				CreatedAt:     now,
				// The row expires when the wait does. A question that outlived
				// its caller would be shown as still live and could be
				// answered by somebody after the launch had already gone ahead
				// without a Root — a late allow with nobody left to receive
				// it, which is precisely what must not happen.
				ExpiresAt: now.Add(grace),
			}
		},
	)
	if err != nil {
		return deniedClientRoot(reason), err
	}
	if allowed {
		return ClientRootAskOutcome{Allowed: true}, nil
	}
	return deniedClientRoot(reason), nil
}

func deniedClientRoot(reason string) ClientRootAskOutcome {
	return ClientRootAskOutcome{ReasonCode: reason}
}

func strconvUint(value uint64) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = digits[value%10]
		value /= 10
	}
	return string(buffer[index:])
}
