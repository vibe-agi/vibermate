package runtimepersistence

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"modernc.org/sqlite"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
)

// This transparent probe preserves the real driver capabilities and delegates
// every SQL operation. Active-step barriers live in a real TEMP trigger/UDF.
type cancelWriterConnector struct {
	driver.Connector
	entered, release chan struct{}
	attempts, closes atomic.Int64
	afterRowsClose   func()
	closeErr         error
	physical         driver.Conn
}

func (c *cancelWriterConnector) Connect(ctx context.Context) (driver.Conn, error) {
	c.attempts.Add(1)
	v, e := c.Connector.Connect(ctx)
	if e != nil {
		return nil, e
	}
	c.physical = v
	return &cancelWriterConn{Conn: v, owner: c}, nil
}

type cancelWriterConn struct {
	driver.Conn
	owner *cancelWriterConnector
}

func (c *cancelWriterConn) Close() error {
	c.owner.closes.Add(1)
	return errors.Join(c.Conn.Close(), c.owner.closeErr)
}
func (c *cancelWriterConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c *cancelWriterConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, q)
}
func (c *cancelWriterConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}
func (c *cancelWriterConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
	if err == nil && c.owner.afterRowsClose != nil {
		return &cancelAfterRowsClose{Rows: rows, cancel: c.owner.afterRowsClose}, nil
	}
	return rows, err
}

func certifiedCancellationWriter(t *testing.T, configures ...func(driver.Connector) driver.Connector) (*ObservationStore, *ControlStore, *cancelWriterConnector) {
	t.Helper()
	control := inspectionControlFixture(t)
	_, current := inspectionSourcesFixture(t)
	// A new optional owner opens and certifies its own single physical writer.
	if err := current.store.database.Close(); err != nil {
		t.Fatal(err)
	}
	connector := newSQLiteConnector(current.store.databasePath, time.Second)
	for _, configure := range configures {
		connector = configure(connector)
	}
	probe := &cancelWriterConnector{Connector: &observationWriterConnector{Connector: connector}, entered: make(chan struct{}), release: make(chan struct{})}
	db := sql.OpenDB(probe)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	current.store.database = db
	t.Cleanup(func() { db.Close() })
	if err := current.PublishInspectionMirror(t.Context(), control); err != nil {
		t.Fatal(err)
	}
	return current, control, probe
}

func TestRecordingCanceledDenialVerificationIsNotCorruption(t *testing.T) {
	_, current := inspectionSourcesFixture(t)
	tx, err := current.store.reads.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = readRecordingDenialCoverage(ctx, tx, "main", recording.InspectionStamp{})
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrRecordingPrivacyUnavailable) {
		t.Fatalf("interrupted verification was reported as demonstrated privacy loss: %v", err)
	}
}

type cancelAfterRowsClose struct {
	driver.Rows
	cancel func()
}

func (r *cancelAfterRowsClose) Close() error { err := r.Rows.Close(); r.cancel(); return err }

func TestRecordingDetectedDenialCorruptionSurvivesConcurrentCancellation(t *testing.T) {
	_, current := inspectionSourcesFixture(t)
	// Positive malformed-chain evidence is detected before rows.Close cancels.
	nativeExec(t, current.store.database, `INSERT INTO recording_denials VALUES(1,'usage','broken',zeroblob(32))`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	probe := &cancelWriterConnector{Connector: newSQLiteReadConnector(current.store.databasePath, time.Second), afterRowsClose: cancel}
	db := sql.OpenDB(probe)
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = readRecordingDenialCoverage(ctx, tx, "main", recording.InspectionStamp{})
	if ctx.Err() == nil || !errors.Is(err, ErrRecordingPrivacyUnavailable) {
		t.Fatalf("positive corruption erased by concurrent cancellation: ctx=%v result=%v", ctx.Err(), err)
	}
}

func (c *cancelWriterConn) ResetSession(ctx context.Context) error {
	return c.Conn.(driver.SessionResetter).ResetSession(ctx)
}
func (c *cancelWriterConn) IsValid() bool                  { return c.Conn.(driver.Validator).IsValid() }
func (c *cancelWriterConn) Ping(ctx context.Context) error { return c.Conn.(driver.Pinger).Ping(ctx) }

func (c *cancelWriterConn) Exec(q string, args []driver.Value) (driver.Result, error) {
	return c.Conn.(driver.Execer).Exec(q, args)
}
func (c *cancelWriterConn) Query(q string, args []driver.Value) (driver.Rows, error) {
	return c.Conn.(driver.Queryer).Query(q, args)
}

func TestRecordingCancellationProbePreservesActualDriverCapabilities(t *testing.T) {
	conn, err := newSQLiteConnector(filepath.Join(t.TempDir(), "capabilities.db"), time.Second).Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	wrapped := &cancelWriterConn{Conn: conn, owner: &cancelWriterConnector{}}
	types := []reflect.Type{reflect.TypeFor[driver.ConnBeginTx](), reflect.TypeFor[driver.ConnPrepareContext](), reflect.TypeFor[driver.ExecerContext](), reflect.TypeFor[driver.QueryerContext](), reflect.TypeFor[driver.Execer](), reflect.TypeFor[driver.Queryer](), reflect.TypeFor[driver.SessionResetter](), reflect.TypeFor[driver.Validator](), reflect.TypeFor[driver.Pinger]()}
	for _, capability := range types {
		if reflect.TypeOf(conn).Implements(capability) != reflect.TypeOf(wrapped).Implements(capability) {
			t.Fatalf("driver capability changed: %v", capability)
		}
	}
}

var activeSQLBarrierSequence atomic.Uint64

func TestRecordingActiveSQLCancellationRefusesPhysicalReplacementAndRetainsMarker(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	function := fmt.Sprintf("task7_active_sql_%d", activeSQLBarrierSequence.Add(1))
	if err := sqlite.RegisterScalarFunction(function, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		close(entered)
		<-release
		return int64(1), nil
	}); err != nil {
		t.Fatal(err)
	}
	registered, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer registered.Close()
	current, control, probe := certifiedCancellationWriter(t, func(c driver.Connector) driver.Connector { c.(*sqliteConnector).driver = registered.Driver(); return c })
	nativeExec(t, control.store.database, `INSERT INTO recording_capture_fences VALUES('managed_run','active-sql',1,0)`)
	if err = current.PublishInspectionMirror(t.Context(), control); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, current.store.database, `CREATE TEMP TRIGGER task7_active_sql AFTER INSERT ON main.capture_observations BEGIN SELECT `+function+`(); END`)
	repo := newRawEvidenceRepository(current.store.database, current.store.operations)
	if _, err = repo.BeginWriterSession(t.Context(), rawevidence.WriterSession{WriterID: "active-sql-session", StartedAt: time.Now().UTC(), MaximumUnflushedTime: time.Second}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- current.PutCaptureObservation(ctx, recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: "active-sql"}, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}, recording.CaptureObservation{ObservedAt: time.Now().UTC()})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("actual SQLite INSERT did not enter callback")
	}
	cancel()
	// The callback keeps the exact physical connection alive. Observe the real
	// modernc Validator acknowledge SQLite interruption before allowing SQL back.
	deadline := time.Now().Add(time.Second)
	for probe.physical.(driver.Validator).IsValid() {
		if time.Now().After(deadline) {
			t.Fatal("actual SQLite interrupt not acknowledged")
		}
		runtime.Gosched()
	}
	unblock()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("actual interrupted SQL cancellation=%v", err)
	}

	err = repo.CloseWriterSession(t.Context(), "active-sql-session", time.Now().UTC())
	if !errors.Is(err, ErrInspectionUnavailable) || probe.attempts.Load() != 2 || probe.closes.Load() != 1 {
		t.Fatalf("invalid physical writer was replaced: close=%v attempts=%d closes=%d", err, probe.attempts.Load(), probe.closes.Load())
	}
	// Do not repair the interrupted writer or fabricate a clean session marker.
	db, err := sql.Open("sqlite", current.store.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state string
	if err = db.QueryRow(`SELECT state FROM runtime_raw_evidence_writer_sessions WHERE writer_id='active-sql-session'`).Scan(&state); err != nil || state != "open" {
		t.Fatalf("unclean marker changed: %q %v", state, err)
	}
	t.Log("actual active-SQL invalidation: preserved driver capabilities, physical attempts=2/closes=1, replacement refused, original marker open")

}

func TestObservationActualDriverCloseErrorRemainsOwnershipFailure(t *testing.T) {
	current, _, probe := certifiedCancellationWriter(t)
	sentinel := errors.New("actual observation driver close failed")
	probe.closeErr = sentinel
	err := current.Shutdown(t.Context())
	if !errors.Is(err, sentinel) || probe.closes.Load() != 1 {
		t.Fatalf("real driver Close failure hidden: %v closes=%d", err, probe.closes.Load())
	}
}
