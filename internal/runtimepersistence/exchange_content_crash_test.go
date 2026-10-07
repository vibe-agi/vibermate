package runtimepersistence

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"modernc.org/sqlite"
)

const contentCrashEnv = "VIBERMATE_STORE_CRASH_TEST_"

var errContentCrashChildDiagnostic = errors.New("child race detector diagnostic rejected")

type contentCrashConfig struct {
	root, path, layout, phase, digest string
}

func (c contentCrashConfig) marker(event string) string {
	return "task4d:" + c.layout + ":" + c.phase + ":" + event + ":" + c.digest
}

func contentCrashRecord(t *testing.T) exchangecontent.Record {
	return sourceOnlyRecord(t, "crash-new", sourceText(strings.Repeat("shared-zstd", 1024)), sourceText("new-before-frame"), sourceText(strings.Repeat("&", 11<<20)), sourceText("complete-tail"))
}

func contentCrashFrames(t *testing.T, r exchangecontent.Record) []string {
	t.Helper()
	var frames []string
	if err := writeStoredBlockRows(context.Background(), r.Request.Messages[0].Blocks[2], candidateContentLimits().CanonicalBytes, func(d string, _ []byte) error {
		frames = append(frames, d)
		return nil
	}); err != nil || len(frames) != 3 {
		t.Fatalf("three-frame crash fixture: frames=%d err=%v", len(frames), err)
	}
	return frames
}

func contentCrashSource(t *testing.T, r exchangecontent.Record) (*exchangecontent.Source, string) {
	t.Helper()
	source, err := exchangecontent.SourceFromRecordWithin(candidateContentLimits(), r)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	if err := source.Walk(context.Background(), func(_ exchangecontent.Part, _ int, message exchangecontent.MessageSource) error {
		return exchangecontent.WriteCanonicalMessage(h, message)
	}); err != nil {
		t.Fatal(err)
	}
	return source, hex.EncodeToString(h.Sum(nil))
}

// The helper does nothing in an ordinary test run. Any partial child
// configuration is an error, and all files must belong to the private root.
// Function registration happens only in this child, before opening any Store.
func TestContentSourceProcessCrashHelper(t *testing.T) {
	keys := []string{"ROOT", "PATH", "LAYOUT", "PHASE", "DIGEST"}
	values := make([]string, len(keys))
	present := 0
	for i, key := range keys {
		values[i] = os.Getenv(contentCrashEnv + key)
		if values[i] != "" {
			present++
		}
	}
	if present == 0 {
		return
	}
	if present != len(keys) {
		t.Fatal("incomplete crash child configuration")
	}
	c := contentCrashConfig{values[0], values[1], values[2], values[3], values[4]}
	root, err := filepath.EvalSymlinks(c.root)
	if err != nil || !filepath.IsAbs(c.root) || root != c.root || c.path != filepath.Join(root, "runtime.db") || !validStoredDigest(c.digest) {
		t.Fatalf("invalid private crash path/digest: %v", err)
	}
	if c.layout != "new-zstd" && c.layout != "shared-identity" {
		t.Fatal("invalid crash layout")
	}
	if !slices.Contains([]string{"first", "middle", "final", "postcommit", "child-error", "child-diagnostic"}, c.phase) {
		t.Fatal("invalid crash phase")
	}
	pipe := os.NewFile(3, "private-crash-marker")
	if pipe == nil {
		t.Fatal("missing private marker pipe")
	}
	defer pipe.Close()
	if _, err := pipe.Stat(); err != nil {
		t.Fatalf("invalid marker pipe: %v", err)
	}
	if c.phase == "child-error" {
		t.Fatal("deliberate crash harness child error (not a product failure)")
	}
	if c.phase == "child-diagnostic" {
		// Synthetic output only: no race is introduced into any product code.
		// Even an exact barrier and owned SIGKILL cannot mask this diagnostic.
		if _, err := fmt.Fprintln(os.Stderr, "WARNING: DATA RACE\ntask4d deliberate harness diagnostic, not an actual race"); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(pipe, c.marker("DIAGNOSTIC")+"\n"); err != nil {
			t.Fatal(err)
		}
		<-make(chan struct{})
	}
	r := contentCrashRecord(t)
	source, messageDigest := contentCrashSource(t, r)
	frames := contentCrashFrames(t, r)
	phaseDigests := map[string]string{"first": frames[0], "middle": frames[1], "final": frames[2], "postcommit": messageDigest}
	if c.digest != phaseDigests[c.phase] {
		t.Fatal("configured barrier digest disagrees with actual Source fixture")
	}
	event := "INSERT"
	if c.layout == "shared-identity" {
		event = "UPDATE"
	}
	barrier := func(marker string) {
		if _, err := io.WriteString(pipe, marker+"\n"); err != nil {
			t.Fatalf("write crash barrier: %v", err)
		}
		// The parent owns the only release: Process.Kill followed by Wait.
		// No rollback, Store shutdown, or successful helper return can run here.
		<-make(chan struct{})
	}
	if c.phase != "postcommit" {
		if err := sqlite.RegisterScalarFunction("task4d_crash_barrier", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			if args[0] != event || args[1] != c.digest {
				return nil, fmt.Errorf("unexpected publication event/digest: %v", args)
			}
			barrier(c.marker(event))
			return nil, errors.New("crash barrier unexpectedly returned")
		}); err != nil {
			t.Fatal(err)
		}
	}
	l := candidateContentLimits()
	s := openCandidateContentStore(t, c.path, &l)
	if c.phase != "postcommit" {
		// RegisterScalarFunction configures the registered driver singleton,
		// whereas Store deliberately constructs its own typed Driver. Copy
		// that registration into only this child's writer driver, then retire
		// its idle connection so the same connector opens with the UDF. No
		// production hook, alternate DSN, or replacement transaction is used.
		registered, err := sql.Open("sqlite", sqliteDSN(c.path, DefaultBusyTimeout, false))
		if err != nil {
			t.Fatal(err)
		}
		defer registered.Close()
		*s.database.Driver().(*sqlite.Driver) = *registered.Driver().(*sqlite.Driver)
		s.database.SetMaxIdleConns(0)
		s.database.SetMaxIdleConns(1)
		if err := s.database.PingContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		for _, triggerEvent := range []string{"INSERT", "UPDATE"} {
			query := fmt.Sprintf(`CREATE TEMP TRIGGER task4d_%s AFTER %s ON main.runtime_exchange_content_blocks WHEN NEW.digest='%s' BEGIN SELECT task4d_crash_barrier('%s',NEW.digest); END`, triggerEvent, triggerEvent, c.digest, triggerEvent)
			if _, err := s.database.Exec(query); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := putCandidateSource(t, s, source); err != nil {
		t.Fatalf("actual PutSource: %v", err)
	}
	if c.phase != "postcommit" {
		t.Fatal("PutSource returned without the precommit barrier")
	}
	barrier(c.marker("COMMIT")) // Only after the actual transaction returns success.
}

// One child and private pipe per call. Even marker/setup failures kill and
// reap that exact process. A watchdog is a failure, never crash evidence.
func runContentCrashChild(t *testing.T, c contentCrashConfig, expected string) (string, error) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	defer writer.Close()
	cmd := exec.Command(executable, "-test.run=^TestContentSourceProcessCrashHelper$", "-test.count=1", "-test.timeout=3m")
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, contentCrashEnv) {
			cmd.Env = append(cmd.Env, value)
		}
	}
	for key, value := range map[string]string{"ROOT": c.root, "PATH": c.path, "LAYOUT": c.layout, "PHASE": c.phase, "DIGEST": c.digest} {
		cmd.Env = append(cmd.Env, contentCrashEnv+key+"="+value)
	}
	cmd.ExtraFiles = []*os.File{writer}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		return "", err
	}
	writer.Close()
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	type markerResult struct {
		marker string
		err    error
	}
	markers := make(chan markerResult, 1)
	go func() {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 512), 512)
		if scanner.Scan() {
			markers <- markerResult{marker: scanner.Text()}
			return
		}
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		markers <- markerResult{err: err}
	}()
	budget := 2 * time.Minute
	if deadline, ok := t.Deadline(); ok {
		budget = min(budget, time.Until(deadline)-10*time.Second)
	}
	timer := time.NewTimer(max(budget, time.Millisecond))
	defer timer.Stop()
	var marker string
	var barrierErr, exitErr error
	reaped := false
	pipeClosed := false
	select {
	case result := <-markers:
		marker = result.marker
		if result.err != nil {
			pipeClosed = true
			barrierErr = fmt.Errorf("missing barrier: %w", result.err)
		} else if marker != expected {
			barrierErr = fmt.Errorf("barrier mismatch: got %q want %q", marker, expected)
		}
	case exitErr = <-wait:
		reaped = true
		barrierErr = errors.New("child exited before exact barrier")
	case <-timer.C:
		barrierErr = errors.New("crash harness watchdog expired")
	}
	if pipeClosed {
		// A failing helper closes its pipe before test cleanup/output drains.
		// Preserve that actual exit/error rather than racing to kill it.
		drain := time.NewTimer(5 * time.Second)
		select {
		case exitErr = <-wait:
			reaped = true
		case <-drain.C:
		}
		drain.Stop()
	}
	var killErr error
	if !reaped {
		killErr = cmd.Process.Kill()
		exitErr = <-wait
	}
	// Wait has completed before output or ProcessState is read.
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	// cmd.Wait drains both captured output streams before returning. Check
	// diagnostics on error paths too, so an expected negative-control error
	// cannot conceal a real child race warning.
	hasRaceDiagnostic := bytes.Contains(output.Bytes(), []byte("WARNING: DATA RACE"))
	if barrierErr != nil {
		if hasRaceDiagnostic {
			return marker, fmt.Errorf("%w; barrier=%v exit=%v output=%s", errContentCrashChildDiagnostic, barrierErr, exitErr, output.String())
		}
		return marker, fmt.Errorf("%w; exit=%v kill=%v output=%s", barrierErr, exitErr, killErr, output.String())
	}
	if killErr != nil || exitErr == nil || !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		return marker, fmt.Errorf("not an owned SIGKILL crash: exit=%v kill=%v status=%v output=%s", exitErr, killErr, status, output.String())
	}
	// Intentional SIGKILL replaces the child's normal race-detector exit
	// status, so exact marker/signal evidence alone is insufficient.
	if hasRaceDiagnostic {
		return marker, fmt.Errorf("%w; exact barrier=%s exit=%v signal=%s output=%s", errContentCrashChildDiagnostic, marker, exitErr, status.Signal(), output.String())
	}
	t.Logf("exact barrier=%s; child pid=%d reaped; exit=%v signal=%s", marker, cmd.Process.Pid, exitErr, status.Signal())
	return marker, nil
}

// Hash each typed row, including original codec/payload, and retain its
// multiplicity. Every runtime table is covered, including empty ones and refs.
func contentCrashRows(t *testing.T, s *Store) map[string][]string {
	t.Helper()
	tables, err := s.database.Query(`SELECT name FROM sqlite_schema WHERE type='table' AND name LIKE 'runtime_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := errors.Join(tables.Err(), tables.Close()); err != nil {
		t.Fatal(err)
	}
	snapshot := make(map[string][]string, len(names))
	for _, name := range names {
		rows, err := s.database.Query(`SELECT * FROM "` + strings.ReplaceAll(name, `"`, `""`) + `"`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		snapshot[name] = nil
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			h := sha256.New()
			for _, value := range values {
				var data []byte
				if blob, ok := value.([]byte); ok {
					data = blob
				} else {
					data = []byte(fmt.Sprint(value))
				}
				fmt.Fprintf(h, "%T:%d:", value, len(data))
				h.Write(data)
			}
			snapshot[name] = append(snapshot[name], hex.EncodeToString(h.Sum(nil)))
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
		slices.Sort(snapshot[name])
	}
	return snapshot
}

func seedContentCrashStore(t *testing.T, c contentCrashConfig, r exchangecontent.Record, frames []string) exchangecontent.Record {
	t.Helper()
	l := candidateContentLimits()
	s := openCandidateContentStore(t, c.path, &l)
	shared := sourceOnlyRecord(t, "crash-shared", r.Request.Messages[0].Blocks[0])
	if c.layout == "shared-identity" {
		shared.Request.Messages[0].Blocks = append(shared.Request.Messages[0].Blocks, r.Request.Messages[0].Blocks[2])
	}
	source, _ := contentCrashSource(t, shared)
	if err := putCandidateSource(t, s, source); err != nil {
		t.Fatal(err)
	}
	if c.layout == "shared-identity" {
		for _, digest := range frames {
			n, codec, data, err := s.exchangeContents.physicalContentRow(context.Background(), digest)
			if err != nil || codec != chunkCodecZstd {
				t.Fatalf("seed compressed frame: codec=%s err=%v", codec, err)
			}
			plain, err := decodeStoredPhysicalPayload(digest, n, codec, data)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.database.Exec(`UPDATE runtime_exchange_content_blocks SET codec='identity',payload=? WHERE digest=?`, plain, digest); err != nil {
				t.Fatal(err)
			}
		}
	}
	shutdownTestStore(t, s)
	return shared
}

func newContentCrashConfig(t *testing.T, layout, phase, digest string) contentCrashConfig {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return contentCrashConfig{root, filepath.Join(root, "runtime.db"), layout, phase, digest}
}

func contentCrashRecordHash(t *testing.T, r exchangecontent.Record) string {
	t.Helper()
	h := sha256.New()
	if err := exchangecontent.WriteCanonicalJSON(h, r); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestContentSourceProcessCrash(t *testing.T) {
	r := contentCrashRecord(t)
	frames := contentCrashFrames(t, r)
	_, messageDigest := contentCrashSource(t, r)
	phases := []struct{ name, digest string }{{"first", frames[0]}, {"middle", frames[1]}, {"final", frames[2]}, {"postcommit", messageDigest}}
	for _, layout := range []string{"new-zstd", "shared-identity"} {
		t.Run(layout, func(t *testing.T) {
			for _, phase := range phases {
				t.Run(phase.name, func(t *testing.T) {
					c := newContentCrashConfig(t, layout, phase.name, phase.digest)
					shared := seedContentCrashStore(t, c, r, frames)
					l := candidateContentLimits()
					s := openCandidateContentStore(t, c.path, &l)
					baseline := contentCrashRows(t, s)
					schema := sourceSchemaHash(t, s)
					state, err := s.repo.ReadSchemaState(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					shutdownTestStore(t, s)
					event := "INSERT"
					if layout == "shared-identity" {
						event = "UPDATE"
					}
					if phase.name == "postcommit" {
						event = "COMMIT"
					}
					if _, err := runContentCrashChild(t, c, c.marker(event)); err != nil {
						t.Fatal(err)
					}
					// Reopen only after SIGKILL and Wait. No child-local connection
					// or TEMP schema is available to satisfy recovery assertions.
					s = openCandidateContentStore(t, c.path, &l)
					got, err := s.exchangeContents.Get(context.Background(), shared.ExchangeID, shared.RecordedAt)
					if err != nil || contentCrashRecordHash(t, got) != contentCrashRecordHash(t, shared) {
						t.Fatalf("original complete bytes changed after crash: %v", err)
					}
					after := contentCrashRows(t, s)
					if phase.name != "postcommit" {
						got, err := s.exchangeContents.Get(context.Background(), r.ExchangeID, r.RecordedAt)
						if !errors.Is(err, exchangecontent.ErrNotFound) || !reflect.DeepEqual(got, exchangecontent.Record{}) || !reflect.DeepEqual(after, baseline) {
							t.Fatalf("precommit crash published content/rows: Get=%v rowsEqual=%v", err, reflect.DeepEqual(after, baseline))
						}
					} else {
						assertContentCrashCommitted(t, s, r, frames, messageDigest, layout, baseline, after)
					}
					newState, err := s.repo.ReadSchemaState(context.Background())
					if err != nil || newState != state || sourceSchemaHash(t, s) != schema {
						t.Fatalf("persistent schema/revision changed: %v", err)
					}
					if countRows(t, s, "sqlite_temp_schema") != 0 {
						t.Fatal("child TEMP hooks survived reopen")
					}
					contentCrashIntegrity(t, s)
					t.Logf("reopen verified: tables=%d schema=%s revision=%d blocks=%d messages=%d nodes=%d contents=%d refs=%d", len(after), schema, state.Revision, len(after["runtime_exchange_content_blocks"]), len(after["runtime_exchange_content_messages"]), len(after["runtime_exchange_content_transcripts"]), len(after["runtime_exchange_contents"]), len(after["runtime_exchange_content_block_refs"]))
				})
			}
		})
	}
}

func assertContentCrashCommitted(t *testing.T, s *Store, r exchangecontent.Record, frames []string, messageDigest, layout string, before, after map[string][]string) {
	t.Helper()
	ctx := context.Background()
	got, err := s.exchangeContents.Get(ctx, r.ExchangeID, r.RecordedAt)
	if err != nil || contentCrashRecordHash(t, got) != contentCrashRecordHash(t, r) {
		t.Fatalf("postcommit full bytes/tail changed: %v", err)
	}
	deltas := map[string]int{"runtime_exchange_contents": 1, "runtime_exchange_content_messages": 1, "runtime_exchange_content_transcripts": 1, "runtime_exchange_content_block_refs": 6, "runtime_exchange_content_blocks": 5}
	if layout == "shared-identity" {
		deltas["runtime_exchange_content_blocks"] = 2
	}
	if len(after) != len(before) {
		t.Fatal("table set changed")
	}
	for name, oldRows := range before {
		if len(after[name]) != len(oldRows)+deltas[name] {
			t.Fatalf("committed %s rows=%d want=%d", name, len(after[name]), len(oldRows)+deltas[name])
		}
		remaining := append([]string(nil), after[name]...)
		for _, row := range oldRows {
			i, ok := slices.BinarySearch(remaining, row)
			if !ok {
				t.Fatalf("committed publication changed original %s row %s", name, row)
			}
			remaining = slices.Delete(remaining, i, i+1)
		}
	}
	var digest, manifest, node string
	if err := s.database.QueryRow(`SELECT m.digest,m.block_manifest,n.digest FROM runtime_exchange_contents c JOIN runtime_exchange_content_transcripts n ON n.digest=c.request_transcript_digest JOIN runtime_exchange_content_messages m ON m.digest=n.message_digest WHERE c.exchange_id=?`, r.ExchangeID).Scan(&digest, &manifest, &node); err != nil || digest != messageDigest || node != transcriptNodeDigest("", messageDigest) {
		t.Fatalf("committed logical message/node identity changed: %v", err)
	}
	var expectedManifest strings.Builder
	for _, block := range r.Request.Messages[0].Blocks {
		if err := writeStoredBlockRows(ctx, block, candidateContentLimits().CanonicalBytes, func(d string, _ []byte) error { expectedManifest.WriteString(d); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if manifest != expectedManifest.String() || len(manifest) != 6*64 {
		t.Fatal("committed ordered physical manifest changed")
	}
	for i := 0; i < len(manifest); i += 64 {
		var refs int
		if err := s.database.QueryRow(`SELECT count(*) FROM runtime_exchange_content_block_refs WHERE message_digest=? AND block_digest=?`, digest, manifest[i:i+64]).Scan(&refs); err != nil || refs != 1 {
			t.Fatalf("unowned committed physical slot=%d: %v", i/64, err)
		}
	}
	for _, frame := range frames {
		_, codec, _, err := s.exchangeContents.physicalContentRow(ctx, frame)
		want := chunkCodecZstd
		if layout == "shared-identity" {
			want = chunkCodecIdentity
		}
		if err != nil || codec != want {
			t.Fatalf("committed frame codec=%s want=%s: %v", codec, want, err)
		}
	}
	projection, err := s.exchangeContents.GetPagedProjection(ctx, r.ExchangeID, r.RecordedAt, exchangecontent.RequestViewFull)
	if err != nil || len(projection.Request.Messages) != 1 || projection.Request.Messages[0].Blocks[0].Deferred == nil {
		t.Fatalf("committed paged directory: %v", err)
	}
	message, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, projection.Request.Messages[0].Blocks[0].Deferred.Cursor)
	if err != nil || message.Total != 4 || len(message.Blocks) != 4 || message.Blocks[2].Deferred == nil || message.Blocks[3].Text != "complete-tail" {
		t.Fatalf("committed logical block directory/tail: %v", err)
	}
	ref, err := loadStoredContentReference(ctx, s.reads, r.ExchangeID, r.RecordedAt)
	if err != nil {
		t.Fatal(err)
	}
	text := r.Request.Messages[0].Blocks[2].Text
	// Full Get above verifies all 11MiB. These focused pages prove the
	// physical first-frame seam and final body/tail after process death; the
	// existing unchanged all-page test supplies exhaustive pagination coverage.
	seam := (storedFrameBody - len(`{"kind":"text","availability":"recorded","text":"`)) / 6
	for _, offset := range []int{seam - 1, len(text) - exchangecontent.PageBodyBytes} {
		cursor := pageCursor(ref, "request", "body", 1)
		cursor.Block, cursor.Offset = 2, offset
		page, err := s.exchangeContents.GetContentPage(ctx, r.ExchangeID, r.RecordedAt, encodeContentCursor(cursor))
		end := min(offset+exchangecontent.PageBodyBytes, len(text))
		if err != nil || page.Total != len(text) || page.Offset != offset || page.Text != text[offset:end] || end == len(text) && page.NextCursor != "" {
			t.Fatalf("committed boundary body offset=%d: %v", offset, err)
		}
	}
}

func contentCrashIntegrity(t *testing.T, s *Store) {
	t.Helper()
	sourceDatabaseIntegrity(t, s)
	for _, query := range []string{
		`SELECT count(*) FROM runtime_exchange_content_messages m WHERE NOT EXISTS(SELECT 1 FROM runtime_exchange_content_transcripts n WHERE n.message_digest=m.digest) AND NOT EXISTS(SELECT 1 FROM runtime_exchange_contents c WHERE c.system_message_digest=m.digest OR c.response_message_digest=m.digest)`,
		`SELECT count(*) FROM runtime_exchange_content_transcripts n WHERE NOT EXISTS(SELECT 1 FROM runtime_exchange_contents c WHERE c.request_transcript_digest=n.digest OR c.expected_transcript_digest=n.digest OR c.base_transcript_digest=n.digest) AND NOT EXISTS(SELECT 1 FROM runtime_exchange_content_transcripts child WHERE child.parent_digest=n.digest)`,
	} {
		var orphans int
		if err := s.database.QueryRow(query).Scan(&orphans); err != nil || orphans != 0 {
			t.Fatalf("unowned content rows=%d: %v", orphans, err)
		}
	}
}

func TestContentSourceProcessCrashHarnessControls(t *testing.T) {
	r := contentCrashRecord(t)
	frames := contentCrashFrames(t, r)
	for _, control := range []string{"wrong-marker", "child-error", "child-diagnostic"} {
		t.Run(control, func(t *testing.T) {
			c := newContentCrashConfig(t, "new-zstd", "first", frames[0])
			seedContentCrashStore(t, c, r, frames)
			if control == "child-error" {
				c.phase = "child-error"
			} else if control == "child-diagnostic" {
				c.phase = "child-diagnostic"
			}
			expected := c.marker("INSERT")
			if control == "wrong-marker" {
				expected += ":deliberately-wrong"
			} else if control == "child-diagnostic" {
				expected = c.marker("DIAGNOSTIC")
			}
			marker, err := runContentCrashChild(t, c, expected)
			accepted := err == nil
			if !accepted {
				switch control {
				case "wrong-marker":
					accepted = errors.Is(err, errContentCrashChildDiagnostic) || !strings.Contains(err.Error(), "barrier mismatch") || marker != c.marker("INSERT")
				case "child-error":
					accepted = errors.Is(err, errContentCrashChildDiagnostic) || !strings.Contains(err.Error(), "deliberate crash harness child error")
				case "child-diagnostic":
					accepted = !errors.Is(err, errContentCrashChildDiagnostic) || marker != expected || !strings.Contains(err.Error(), "not an actual race")
				}
			}
			if accepted {
				t.Fatalf("negative harness control falsely accepted: marker=%q err=%v", marker, err)
			}
			t.Logf("expected harness rejection (not product RED), child reaped: %v", err)
			l := candidateContentLimits()
			s := openCandidateContentStore(t, c.path, &l)
			if countRows(t, s, "runtime_exchange_contents") != 1 {
				t.Fatal("harness control published new content")
			}
			contentCrashIntegrity(t, s)
		})
	}
}
