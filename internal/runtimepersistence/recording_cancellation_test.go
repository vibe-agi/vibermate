package runtimepersistence

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
)

// A real SQLite transaction is active before cancellation. The wrapper only
// observes the physical connector and holds the actual PrepareContext boundary.
type cancelWriterConnector struct {
	driver.Connector
	entered, release chan struct{}
	armed            atomic.Bool
	holdExec         atomic.Bool
	attempts, closes atomic.Int64
	afterRowsClose   func()
}

func (c *cancelWriterConnector) Connect(ctx context.Context) (driver.Conn, error) {
	c.attempts.Add(1)
	v, e := c.Connector.Connect(ctx)
	if e != nil {
		return nil, e
	}
	return &cancelWriterConn{Conn: v, owner: c}, nil
}

type cancelWriterConn struct {
	driver.Conn
	owner *cancelWriterConnector
}

func (c *cancelWriterConn) Close() error { c.owner.closes.Add(1); return c.Conn.Close() }
func (c *cancelWriterConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c *cancelWriterConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	if c.owner.armed.CompareAndSwap(true, false) {
		close(c.owner.entered)
		<-c.owner.release
	}
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, q)
}
func (c *cancelWriterConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if c.owner.holdExec.CompareAndSwap(true, false) {
		close(c.owner.entered)
		<-c.owner.release
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}
func (c *cancelWriterConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
	if err == nil && c.owner.afterRowsClose != nil {
		return &cancelAfterRowsClose{Rows: rows, cancel: c.owner.afterRowsClose}, nil
	}
	return rows, err
}

func certifiedCancellationWriter(t *testing.T) (*ObservationStore, *ControlStore, *cancelWriterConnector) {
	t.Helper()
	control := inspectionControlFixture(t)
	_, current := inspectionSourcesFixture(t)
	// A new optional owner opens and certifies its own single physical writer.
	if err := current.store.database.Close(); err != nil {
		t.Fatal(err)
	}
	probe := &cancelWriterConnector{Connector: &observationWriterConnector{Connector: newSQLiteConnector(current.store.databasePath, time.Second)}, entered: make(chan struct{}), release: make(chan struct{})}
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

func TestRecordingCanceledRawTransactionKeepsCertifiedWriter(t *testing.T) {
	current, _, probe := certifiedCancellationWriter(t)
	db := current.store.database
	repo := newRawEvidenceRepository(db, current.store.operations)
	now := time.Now().UTC()
	if _, err := repo.BeginWriterSession(t.Context(), rawevidence.WriterSession{WriterID: "writer-1", MaximumUnflushedTime: time.Second, StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	probe.armed.Store(true)
	done := make(chan error, 1)
	go func() {
		done <- repo.AppendBatch(ctx, []rawevidence.StoredEnvelope{rawEvidenceRecordForTest("writer-1.1", 1, rawevidence.LayerClientIngress, nil, []byte(`{"version":1,"headers":[]}`))}, now)
	}()
	<-probe.entered
	cancel()
	close(probe.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation identity lost: %v", err)
	}
	// CloseWriterSession joins behind any SQL rollback; replacement is forbidden.
	err := repo.CloseWriterSession(t.Context(), "writer-1", now)
	t.Logf("close=%v physical attempts=%d closes=%d", err, probe.attempts.Load(), probe.closes.Load())
	if err != nil || probe.attempts.Load() != 1 || probe.closes.Load() != 0 {
		t.Fatalf("canceled append lost certified physical writer: %v attempts=%d closes=%d", err, probe.attempts.Load(), probe.closes.Load())
	}
	if err := current.CheckRecordingWriter(t.Context()); err != nil {
		t.Fatal("TEMP certificate lost", err)
	}
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

func TestRecordingCanceledMirrorTransactionKeepsCertifiedWriter(t *testing.T) {
	current, control, probe := certifiedCancellationWriter(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	probe.holdExec.Store(true)
	done := make(chan error, 1)
	go func() { done <- current.PublishInspectionMirror(ctx, control) }()
	<-probe.entered
	cancel()
	close(probe.release)
	if err := <-done; !errors.Is(err, context.Canceled) || errors.Is(err, ErrRecordingPrivacyUnavailable) {
		t.Fatalf("mirror cancellation=%v", err)
	}
	if err := current.CheckRecordingWriter(t.Context()); err != nil {
		t.Fatal(err)
	}
	if probe.attempts.Load() != 1 || probe.closes.Load() != 0 {
		t.Fatalf("writer replaced attempts=%d closes=%d", probe.attempts.Load(), probe.closes.Load())
	}
	if err := current.PublishInspectionMirror(t.Context(), control); err != nil {
		t.Fatal("canceled generation prevented later publication", err)
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
