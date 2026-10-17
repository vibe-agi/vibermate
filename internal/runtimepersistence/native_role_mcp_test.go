package runtimepersistence

import (
	"context"
	"database/sql"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/toolapproval"
	"github.com/vibe-agi/vibermate/internal/toolpolicy"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type nativeLocalTransport struct {
	target *url.URL
	client *http.Client
}

func (t nativeLocalTransport) RoundTrip(r *http.Request, _ egressnetwork.Policy) (*http.Response, error) {
	copy := r.Clone(r.Context())
	target := *r.URL
	target.Scheme = t.target.Scheme
	target.Host = t.target.Host
	copy.URL = &target
	return t.client.Do(copy)
}

func TestNativeRoleMCPCompletesWhileObservationWriterAndReadersAreHeld(t *testing.T) {
	for _, fault := range []string{"none", "delivery", "once", "event"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			directory := t.TempDir()
			control, err := OpenControl(ctx, ControlOptions{DatabasePath: filepath.Join(directory, "control.db"), LayoutID: "mcp-layout", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			fixture := newNativeLeaseFixtureOnStore(t, control.store, true, false)
			observation, err := OpenObservations(ctx, ObservationOptions{DatabasePath: filepath.Join(directory, "observations.db"), LayoutID: "mcp-layout", ArchiveID: "archive", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer observation.Shutdown(ctx)
			var sends atomic.Int64
			const requestBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sends.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != requestBody {
					t.Errorf("forwarded bytes changed: %q %v", body, err)
				}
				if r.Header.Get("Authorization") != "Bearer synthetic-original-login" {
					t.Error("original send credential changed")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Mcp-Session-Id", "synthetic-session")
				io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"synthetic","version":"1"}}}`)
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			bridge := nativetool.TransitionalRepository{AuthorityRepository: control.NativeToolRepository()}
			offline, _ := offlinehold.New(offlinehold.DefaultConfig())
			if err := offline.Start(ctx, offlinehold.RuntimeBinding{InstanceID: "native-role"}); err != nil {
				t.Fatal(err)
			}
			options := toolapproval.DefaultOptions(control.ToolApprovalRepository())
			options.NativeRepository = bridge
			authority, err := toolapproval.New(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			gate, _ := toolpolicy.New(authority)
			ids := &mcpFaultIDs{}
			executor, err := nativemcp.New(nativemcp.Dependencies{OwnerContext: ctx, Transport: nativeLocalTransport{target, server.Client()}, Store: bridge, ToolDecisions: gate, Offline: offline, Clock: fixture.clock.Now, IDs: ids}, nativemcp.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			defer executor.Close(ctx)
			lock, err := observation.store.database.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Rollback()
			if _, err := lock.ExecContext(ctx, `UPDATE observation_storage_state SET archive_id=archive_id`); err != nil {
				t.Fatal(err)
			}
			read1, err := observation.store.reads.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer read1.Close()
			read2, err := observation.store.reads.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer read2.Close()
			operation, _ := ids.NewNativeOperationID(ctx)
			injection := &nativeRoleCorruptCommit{database: control.store.database, operation: operation, fault: fault}
			if fault != "none" {
				control.store.nativeTools.committer = injection
			}
			defer func() { control.store.nativeTools.committer = sqlTransactionCommitter{}; injection.restore() }()
			serve := func() nativemcp.Terminal {
				lease, err := fixture.assignments.BeginRequest(ctx, fixture.capture, fixture.connectionID, fixture.facts)
				if err != nil {
					t.Error(err)
					return nativemcp.Terminal{}
				}
				request := httptest.NewRequest("POST", "https://native.example/backend-api/ps/mcp", strings.NewReader(requestBody))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Authorization", "Bearer synthetic-original-login")
				captured, err := nativemcp.NewCapturedRequest(lease, fixture.admission, request, operation)
				if err != nil {
					lease.Release()
					t.Error(err)
					return nativemcp.Terminal{}
				}
				return executor.Serve(ctx, captured, httptest.NewRecorder())
			}
			done := make(chan nativemcp.Terminal, 1)
			go func() { done <- serve() }()
			select {
			case result := <-done:
				if fault == "none" && result.Outcome != nativemcp.OutcomeCompleted {
					t.Fatalf("native failed with observations blocked: %+v", result)
				}
			case <-time.After(3 * time.Second):
				lock.Rollback()
				read1.Close()
				read2.Close()
				<-done
				t.Fatal("native send did not complete before optional storage release")
			}
			injection.restore()
			control.store.nativeTools.committer = sqlTransactionCommitter{}
			serve()
			want := int64(1)
			if fault != "none" {
				want = 0
				if !injection.used {
					t.Fatal("required proof corruption not exercised")
				}
			}
			if sends.Load() != want {
				t.Fatalf("native replay sent %d times", sends.Load())
			}

		})
	}
}

type nativeRoleCorruptCommit struct {
	database           *sql.DB
	operation          nativetool.ID
	fault              string
	used               bool
	originalConnection string
	originalRevision   int64
}

func (c *nativeRoleCorruptCommit) Commit(tx *sql.Tx) error {
	var claimed int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM native_tool_once WHERE operation_id=?`, c.operation).Scan(&claimed); err != nil {
		return err
	}
	if claimed != 0 && !c.used {
		if err := tx.QueryRow(`SELECT connection_id FROM native_tool_deliveries WHERE operation_id=?`, c.operation).Scan(&c.originalConnection); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT authority_revision FROM native_tool_once WHERE operation_id=?`, c.operation).Scan(&c.originalRevision); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if claimed == 0 || c.used {
		return nil
	}
	c.used = true
	var query string
	switch c.fault {
	case "delivery":
		query = `UPDATE native_tool_deliveries SET connection_id='foreign' WHERE operation_id=?`
	case "once":
		query = `UPDATE native_tool_once SET authority_revision=authority_revision+1 WHERE operation_id=?`
	case "event":
		query = `UPDATE native_tool_operations SET last_event_kind='completed' WHERE operation_id=?`
	}
	_, err := c.database.Exec(query, c.operation)
	return err
}
func (c *nativeRoleCorruptCommit) restore() {
	if !c.used {
		return
	}
	var query string
	switch c.fault {
	case "delivery":
		_, _ = c.database.Exec(`UPDATE native_tool_deliveries SET connection_id=? WHERE operation_id=?`, c.originalConnection, c.operation)
		return
	case "once":
		_, _ = c.database.Exec(`UPDATE native_tool_once SET authority_revision=? WHERE operation_id=?`, c.originalRevision, c.operation)
		return
	case "event":
		query = `UPDATE native_tool_operations SET last_event_kind=state WHERE operation_id=?`
	default:
		return
	}
	_, _ = c.database.Exec(query, c.operation)
}
