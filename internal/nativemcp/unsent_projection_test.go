package nativemcp

import (
	"context"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

// Negative typed-projection corruption never modifies durable authority.
// Successful Start and actual Verify still delegate to the same real Store.
type unsentProjectionFault struct {
	nativetool.Repository
	target nativetool.ID
	mode   string
	armed  atomic.Bool
}

func (f *unsentProjectionFault) VerifyStartReceipt(ctx context.Context, receipt nativetool.StartReceipt, now time.Time) error {
	err := f.Repository.VerifyStartReceipt(ctx, receipt, now)
	if receipt.OperationID() == f.target && err != nil {
		f.armed.Store(true)
	}
	return err
}
func (f *unsentProjectionFault) Get(ctx context.Context, id nativetool.ID) (nativetool.Operation, error) {
	op, err := f.Repository.Get(ctx, id)
	if err != nil || id != f.target || !f.armed.Load() {
		return op, err
	}
	switch f.mode {
	case "unavailable":
		return op, nativetool.ErrUnavailable
	case "missing":
		return op, nativetool.ErrNotFound
	case "operation":
		op.ID = "ffffffffffffffffffffffffffffffff"
	case "capture":
		op.Capture.ID = "foreign-capture"
	case "kind":
		op.Kind = nativetool.RequestToolsCall
	case "binding":
		digest := nativetool.Digest{}
		op.BindingDigest = &digest
	case "environment_revision":
		op.EnvironmentRevision++
	case "environment_digest":
		op.EnvironmentDigest[0] ^= 1
	case "origin":
		op.TargetOrigin = "https://other.invalid"
	case "recording":
		op.Recording.Mode = environment.ContentRecordingMetadataOnly
	case "nonstarted":
		op.State = "validated"
	case "half_counts":
		zero := int64(0)
		op.BytesIn = &zero
	case "unknown_terminal":
		now := op.CreatedAt
		op.CompletedAt = &now
		op.State = "unexpected"
	case "bad_time":
		before := op.CreatedAt.Add(-time.Millisecond)
		op.CompletedAt = &before
		op.State, op.Reason = "uncertain", "execution_uncertain"
	}
	return op, nil
}

func TestUnsentTypedHistoryMustMatchOwnedBinding(t *testing.T) {
	for _, mode := range []string{"unavailable", "missing", "operation", "capture", "kind", "binding", "environment_revision", "environment_digest", "origin", "recording", "nonstarted", "half_counts", "unknown_terminal", "bad_time"} {
		t.Run(mode, func(t *testing.T) {
			f := newNativeFixture(t, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff})
			f.handshake(t)
			f.transport.handler = func(_ *http.Request, body []byte) (*http.Response, error) {
				r, err := parseRequest(body, DefaultLimits())
				if err != nil {
					return nil, err
				}
				return wireResponse(`{"jsonrpc":"2.0","id":`+string(r.id)+`,"result":{"tools":[{"name":"read_file","inputSchema":{"type":"object"}}]}}`, false), nil
			}
			request := f.captured(t, "POST", `{"jsonrpc":"2.0","id":30,"method":"ping"}`, "synthetic-session")
			held, finish := holdUnsentLifecycle(t, f, request)
			held.Repository = &unsentProjectionFault{Repository: held.Repository, target: request.id, mode: mode}
			before := readFreshGETFacts(t, f, request.id)
			sends := f.transport.sends()
			if result, _ := f.serve(t, `{"jsonrpc":"2.0","id":40,"method":"tools/list"}`, "synthetic-session"); result.Outcome != OutcomeCompleted {
				t.Fatal(result)
			}
			terminal := finish()
			facts := readFreshGETFacts(t, f, request.id)
			scope, err := f.store.NativeToolRepository().GetScope(context.Background(), f.capture)
			if err != nil || terminal.Outcome != OutcomeUncertain || terminal.Reason != ReasonStorageUnavailable || facts.OperationState != "uncertain" || facts.AuditKnown != 0 || facts.BytesIn.Valid || facts.BytesOut.Valid || scope.State != "revoked" || !scope.ReplayBarrier || f.transport.sends() != sends+1 || !reflect.DeepEqual(before.Once, facts.Once) {
				t.Fatal("untrusted history selected known-zero or escaped fencing")
			}
		})
	}
}
