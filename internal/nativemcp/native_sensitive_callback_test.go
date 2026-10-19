package nativemcp

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
)

func TestNativeSensitiveCallbackPersistsOnlyMACAndNeverReplays(t *testing.T) {
	data, err := os.ReadFile("testdata/codex-0.160-callbacks.synthetic.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Kind           string
		Request, Reply json.RawMessage
	}
	if err = json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for index, fixture := range fixtures {
		t.Run(fmt.Sprint(index)+"/"+fixture.Kind, func(t *testing.T) {
			for _, mode := range []environment.ContentRecordingMode{environment.ContentRecordingOff, environment.ContentRecordingMetadataOnly, environment.ContentRecordingFull} {
				t.Run(string(mode), func(t *testing.T) {
					recording := environment.ContentRecordingPolicy{Mode: mode}
					if mode != environment.ContentRecordingOff {
						recording.RetentionDays = 1
					}
					f := newNativeFixture(t, recording)
					f.handshake(t)
					callback, reply := string(fixture.Request), string(fixture.Reply)
					callbackRPC, err := parseResponse(fixture.Request, DefaultLimits())
					if err != nil {
						t.Fatal(err)
					}
					replied := make(chan struct{}, 1)
					f.transport.handler = func(r *http.Request, body []byte) (*http.Response, error) {
						if strings.Contains(string(body), `"method":"tools/call"`) {
							reader, writer := io.Pipe()
							go func() {
								defer writer.Close()
								if fixture.Kind == "verification" {
									_, _ = io.WriteString(writer, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\",\"params\":{}}\n\n")
								}
								_, _ = io.WriteString(writer, "event: message\nid: sensitive-event\ndata: "+callback+"\n\n")
								<-replied
								if fixture.Kind == "url" {
									_, _ = io.WriteString(writer, "event: message\nid: elicitation-complete\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/elicitation/complete\",\"params\":{\"elicitationId\":\"synthetic-elicitation\"}}\n\n")
								}
								_, _ = io.WriteString(writer, "event: message\nid: final-event\ndata: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ordinary synthetic result\"}]}}\n\n")
							}()
							return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}, nil
						}
						replied <- struct{}{}
						return &http.Response{StatusCode: 202, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
					}
					parent := f.captured(t, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"a"},"_meta":{"callId":"callback-parent"}}}`, "synthetic-session")
					parentResults := make(chan Terminal, 1)
					go func() { parentResults <- f.executor.Serve(context.Background(), parent, httptest.NewRecorder()) }()
					f.approve(t, parent.id)
					deadline := time.Now().Add(time.Second)
					found := false
					for time.Now().Before(deadline) {
						e := f.executor.(*executor)
						e.mu.Lock()
						owner := e.scopes[f.capture.Key()]
						for _, s := range owner.sessions {
							s.mu.Lock()
							found = s.parents[string(callbackRPC.id)] != nil
							s.mu.Unlock()
							if found {
								break
							}
						}
						e.mu.Unlock()
						if found {
							break
						}
						time.Sleep(2 * time.Millisecond)
					}
					if !found {
						t.Fatal("authenticated callback was not registered")
					}
					child := f.captured(t, "POST", reply, "synthetic-session")
					results := make(chan Terminal, 1)
					go func() { results <- f.executor.Serve(context.Background(), child, httptest.NewRecorder()) }()
					f.approve(t, child.id)
					terminal := <-results
					if terminal.Outcome != OutcomeCompleted {
						t.Fatalf("sensitive child %+v", terminal)
					}
					if terminal = (<-parentResults); terminal.Outcome != OutcomeCompleted {
						t.Fatalf("parent %+v", terminal)
					}
					if f.transport.sends() != 5 {
						t.Fatalf("sends%d", f.transport.sends())
					}
					var requestDigest, serverDigest, rpcDigest []byte
					if err := f.db.QueryRow(`SELECT b.request_digest,b.server_request_digest,i.rpc_id_digest FROM native_tool_bindings b JOIN native_tool_inbound i ON i.operation_id=b.operation_id WHERE b.operation_id=?`, child.id).Scan(&requestDigest, &serverDigest, &rpcDigest); err != nil {
						t.Fatal(err)
					}
					for _, raw := range []string{callback, reply, string(callbackRPC.id)} {
						direct := sha256.Sum256([]byte(raw))
						for _, digest := range [][]byte{requestDigest, serverDigest, rpcDigest} {
							if string(digest) == string(direct[:]) {
								t.Fatal("sensitive direct SHA persisted")
							}
						}
					}
					assertNoSensitiveRows(t, f, []string{"synthetic-private-credential", "synthetic-private-description", "synthetic-original-login", callback, reply})
					before := f.transport.sends()
					terminal, _ = f.serve(t, reply, "synthetic-session")
					if terminal.Outcome == OutcomeCompleted || f.transport.sends() != before {
						t.Fatal("answered callback replayed")
					}
					if err := f.executor.Close(context.Background()); err != nil {
						t.Fatal(err)
					}
					before = f.transport.sends()
					terminal, _ = f.serve(t, reply, "synthetic-session")
					if terminal.Outcome == OutcomeCompleted || f.transport.sends() != before {
						t.Fatal("closed key replayed callback")
					}
				})
			}
		})
	}
}

func assertNoSensitiveRows(t *testing.T, f *nativeFixture, sentinels []string) {
	t.Helper()
	if err := f.content.flush(); err != nil {
		t.Fatal(err)
	}
	// Inspect decoded optional bodies as well as scalar rows; compression must
	// not hide a retained sensitive sentinel from this privacy assertion.
	rows, err := f.contentDB.Query(`SELECT operation_id,part FROM native_tool_contents`)
	if err != nil {
		t.Fatal(err)
	}
	type contentKey struct {
		id   nativetool.ID
		part nativetool.ContentPart
	}
	var keys []contentKey
	for rows.Next() {
		var key contentKey
		if err := rows.Scan(&key.id, &key.part); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, key := range keys {
		v, err := f.content.store.GetNativeContent(context.Background(), key.id, key.part)
		if err != nil {
			t.Fatal(err)
		}
		for _, sentinel := range sentinels {
			if strings.Contains(string(v.Body), sentinel) {
				t.Fatalf("sensitive sentinel retained in optional %s", key.part)
			}
		}
	}
	for _, database := range []*sql.DB{f.db, f.contentDB} {
		tables, err := database.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for tables.Next() {
			var name string
			if err = tables.Scan(&name); err != nil {
				t.Fatal(err)
			}
			names = append(names, name)
		}
		tables.Close()
		for _, name := range names {
			rows, err := database.Query(`SELECT * FROM "` + name + `"`)
			if err != nil {
				t.Fatal(err)
			}
			columns, _ := rows.Columns()
			values := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for index := range values {
				destinations[index] = &values[index]
			}
			for rows.Next() {
				if err = rows.Scan(destinations...); err != nil {
					t.Fatal(err)
				}
				for _, value := range values {
					var bytes []byte
					switch v := value.(type) {
					case string:
						bytes = []byte(v)
					case []byte:
						bytes = v
					default:
						continue
					}
					for _, sentinel := range sentinels {
						digest := sha256.Sum256([]byte(sentinel))
						if strings.Contains(string(bytes), sentinel) || string(bytes) == string(digest[:]) || strings.Contains(string(bytes), hex.EncodeToString(digest[:])) {
							t.Fatalf("sensitive sentinel/digest retained in %s", name)
						}
					}
				}
			}
			if err = rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
		}
	}
	page, err := f.content.store.PageNativeInspection(context.Background(), nativetool.InspectionIndexRequest{Capture: f.capture, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range page.Items {
		if entry.Identity.Kind == nativetool.RequestServerReply {
			t.Fatal("callback content exposed")
		}
	}
}
