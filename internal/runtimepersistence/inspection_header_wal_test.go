package runtimepersistence

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/activity"
)

// This test exercises production read-only attachments. It does not assume
// SQLite removes a header-only WAL; logs distinguish a stable read from drift.
func TestAuditHeaderOnlyWALSteadyProductionReads(t *testing.T) {
	f, _, archive, before := checkpointedWALInspectionFixture(t)
	header := auditValidWALHeader(t, before)
	if err := os.WriteFile(archive+"-wal", header, 0600); err != nil {
		t.Fatal(err)
	}
	reader := newActivityInspectionTestReader(t, f)
	for pass := range 4 {
		summary, err := reader.SummarizeExchanges(t.Context(), activity.SummaryScope{CaptureRunID: "audit-a"})
		t.Logf("pass=%d summary_requests=%d error=%v wal=%s", pass, summary.Requests, err, auditWALState(archive))
		if err != nil || summary.Requests != 1 {
			t.Errorf("steady header-only summary: %+v %v", summary, err)
		}
		point, err := reader.GetExchange(t.Context(), "wal-archived-exchange")
		t.Logf("pass=%d point_subject=%s error=%v wal=%s", pass, point.SubjectID, err, auditWALState(archive))
		if err != nil || point.SubjectID != "wal-archived-exchange" {
			t.Errorf("steady header-only point: %+v %v", point, err)
		}
	}
	auditUnchangedArchive(t, archive, before)
}

// A separate SQLite handle performs no SQL mutation, but is opened read-write
// with query_only. A failure here proves a benign-sidecar comparison gap, NOT
// that the production mode=ro catalog can trigger it without an external owner.
func TestAuditHeaderOnlyWALAfterQueryOnlyReadWriteClose(t *testing.T) {
	f, _, archive, before := checkpointedWALInspectionFixture(t)
	if err := os.WriteFile(archive+"-wal", auditValidWALHeader(t, before), 0600); err != nil {
		t.Fatal(err)
	}
	reader := newActivityInspectionTestReader(t, f)
	if _, err := reader.SummarizeExchanges(t.Context(), activity.SummaryScope{CaptureRunID: "audit-a"}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	u := url.URL{Scheme: "file", Path: archive, RawQuery: "mode=rw"}
	if _, err = db.ExecContext(t.Context(), `PRAGMA query_only=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(t.Context(), `ATTACH DATABASE ? AS archive`, u.String()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRowContext(t.Context(), `SELECT count(*) FROM archive.runtime_activities`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("read-only SQL count=%d err=%v", count, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("after query-only read-write handle closes: %s", auditWALState(archive))
	auditUnchangedArchive(t, archive, before)
	info, err := os.Stat(archive + "-wal")
	if err == nil && info.Size() == 32 {
		t.Skip("this SQLite/platform did not normalize the header-only WAL")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err == nil && info.Size() != 0 {
		t.Fatalf("unexpected nonempty WAL size %d", info.Size())
	}
	for pass := range 2 {
		summary, summaryErr := reader.SummarizeExchanges(t.Context(), activity.SummaryScope{CaptureRunID: "audit-a"})
		point, pointErr := reader.GetExchange(t.Context(), "wal-archived-exchange")
		t.Logf("pass=%d summary_error=%v point_error=%v", pass, summaryErr, pointErr)
		if summaryErr != nil || pointErr != nil || summary.Requests != 1 || point.SubjectID != "wal-archived-exchange" {
			t.Errorf("benign SQLite sidecar normalization invalidated source: summary=%v point=%v", summaryErr, pointErr)
		}
	}
}

// These payloads deliberately are not valid header-only WALs. This comparator
// guard is not a claim that SQLite itself rejects each malformed file at open.
func TestAuditHeaderOnlyWALDoesNotNormalizeMalformedOrPartialFrames(t *testing.T) {
	_, _, archive, before := checkpointedWALInspectionFixture(t)
	header := auditValidWALHeader(t, before)
	badChecksum := bytes.Clone(header)
	badChecksum[24] ^= 1
	cases := map[string][]byte{
		"truncated header": header[:31],
		"zero header":      make([]byte, 32),
		"bad checksum":     badChecksum,
		"partial frame":    append(bytes.Clone(header), 0),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(archive+"-wal", payload, 0600); err != nil {
				t.Fatal(err)
			}
			a, err := inspectionSourceFiles(archive, false)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Truncate(archive+"-wal", 0); err != nil {
				t.Fatal(err)
			}
			b, err := inspectionSourceFiles(archive, false)
			if err != nil {
				t.Fatal(err)
			}
			if sameInspectionSourceFiles(a, b, false) {
				t.Fatal("invalid nonempty WAL normalized as empty")
			}
		})
	}
}

func auditValidWALHeader(t *testing.T, archive []byte) []byte {
	t.Helper()
	pageSize := uint32(binary.BigEndian.Uint16(archive[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	donor := filepath.Join(t.TempDir(), "donor.db")
	db, err := sql.Open("sqlite", donor)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err = db.ExecContext(t.Context(), fmt.Sprintf(`PRAGMA page_size=%d; PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE synthetic(value); INSERT INTO synthetic VALUES(1)`, pageSize)); err != nil {
		t.Fatal(err)
	}
	wal, err := os.ReadFile(donor + "-wal")
	if err != nil || len(wal) <= 32 {
		t.Fatalf("actual SQLite WAL: bytes=%d err=%v", len(wal), err)
	}
	h := bytes.Clone(wal[:32])
	magic := binary.BigEndian.Uint32(h[:4])
	if magic&0xfffffffe != 0x377f0682 || binary.BigEndian.Uint32(h[4:8]) != 3007000 || binary.BigEndian.Uint32(h[8:12]) != pageSize {
		t.Fatal("unexpected SQLite-generated WAL header")
	}
	var order binary.ByteOrder = binary.LittleEndian
	if magic&1 != 0 {
		order = binary.BigEndian
	}
	var s0, s1 uint32
	for at := 0; at < 24; at += 8 {
		s0 += order.Uint32(h[at:at+4]) + s1
		s1 += order.Uint32(h[at+4:at+8]) + s0
	}
	if s0 != binary.BigEndian.Uint32(h[24:28]) || s1 != binary.BigEndian.Uint32(h[28:32]) {
		t.Fatal("SQLite-generated header checksum mismatch")
	}
	return h
}

func auditWALState(archive string) string {
	info, err := os.Stat(archive + "-wal")
	if err != nil {
		return fmt.Sprint(err)
	}
	return fmt.Sprintf("bytes=%d mtime=%s", info.Size(), info.ModTime())
}

func auditUnchangedArchive(t *testing.T, archive string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(archive)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("archive DB bytes changed: %v", err)
	}
}
