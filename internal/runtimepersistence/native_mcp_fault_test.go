package runtimepersistence

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/toolpolicy"
)

type mcpFaultIDs struct{ value atomic.Uint64 }

func (ids *mcpFaultIDs) NewNativeOperationID(context.Context) (nativetool.ID, error) {
	return nativetool.NewID(fmt.Sprintf("%032x", ids.value.Add(1)+50000))
}
func (ids *mcpFaultIDs) NewOwnerEpoch(context.Context) (uint64, error) {
	return ids.value.Add(1) + 100, nil
}

type mcpFaultTransport struct{ calls atomic.Int64 }

func (t *mcpFaultTransport) RoundTrip(*http.Request, egressnetwork.Policy) (*http.Response, error) {
	t.calls.Add(1)
	return nil, fmt.Errorf("synthetic transport must not be reached")
}

type mcpStartOnlyFault struct {
	operation nativetool.ID
	fault     transactionCommitter
	used      atomic.Bool
}

func (f *mcpStartOnlyFault) Commit(tx *sql.Tx) error {
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, f.operation).Scan(&count); err != nil {
		return err
	}
	if count == 1 && f.used.CompareAndSwap(false, true) {
		return f.fault.Commit(tx)
	}
	return tx.Commit()
}
func TestNativeMCPUncertainCommitNeverReachesLowLevelTransport(t *testing.T) {
	for _, fault := range []struct {
		name      string
		committer transactionCommitter
		committed bool
	}{{"commit_then_error", commitThenError{}, true}, {"rollback_then_error", rollbackThenError{}, false}} {
		t.Run(fault.name, func(t *testing.T) {
			fixture := newNativeLeaseFixture(t, true, false)
			ctx := context.Background()
			ids := &mcpFaultIDs{}
			transport := &mcpFaultTransport{}
			offline, _ := offlinehold.New(offlinehold.DefaultConfig())
			if err := offline.Start(ctx, offlinehold.RuntimeBinding{InstanceID: "native-fault"}); err != nil {
				t.Fatal(err)
			}
			options := toolapproval.DefaultOptions(fixture.store.ToolApprovalRepository())
			options.NativeRepository = fixture.store.NativeToolRepository()
			authority, err := toolapproval.New(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			gate, _ := toolpolicy.New(authority)
			executor, err := nativemcp.New(nativemcp.Dependencies{OwnerContext: ctx, Transport: transport, Store: fixture.store.NativeToolRepository(), ToolDecisions: gate, Offline: offline, Clock: fixture.clock.Now, IDs: ids}, nativemcp.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			defer executor.Close(ctx)
			lease, err := fixture.assignments.BeginRequest(ctx, fixture.capture, fixture.connectionID, fixture.facts)
			if err != nil {
				t.Fatal(err)
			}
			operation, _ := ids.NewNativeOperationID(ctx)
			request := httptest.NewRequest("POST", "https://native.example/backend-api/ps/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer synthetic-original-login")
			captured, err := nativemcp.NewCapturedRequest(lease, fixture.admission, request, operation)
			if err != nil {
				lease.Release()
				t.Fatal(err)
			}
			injection := &mcpStartOnlyFault{operation: operation, fault: fault.committer}
			fixture.store.nativeTools.committer = injection
			terminal := executor.Serve(ctx, captured, httptest.NewRecorder())
			fixture.store.nativeTools.committer = sqlTransactionCommitter{}
			if !injection.used.Load() || transport.calls.Load() != 0 || terminal.Outcome == nativemcp.OutcomeCompleted {
				t.Fatalf("uncertain Start sent %+v count%d", terminal, transport.calls.Load())
			}
			var once int
			if err = fixture.store.database.QueryRow(`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, operation).Scan(&once); err != nil {
				t.Fatal(err)
			}
			expected := 0
			if fault.committed {
				expected = 1
			}
			if once != expected {
				t.Fatalf("claims%d want%d", once, expected)
			}
			if receipt, err := fixture.store.NativeToolRepository().Start(ctx, operation, "delayed-fault-send", 1, fixture.clock.Now()); err == nil || receipt.OperationID() != "" {
				t.Fatal("unknown commit produced delayed receipt")
			}
		})
	}
}
