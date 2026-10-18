package productruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientannotation"
	"github.com/vibe-agi/vibermate/internal/clienttarget"
	"github.com/vibe-agi/vibermate/internal/codelibrary"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/toolpolicy"
)

// Real CONNECT -> Pipeline -> provider Client -> Raw Manager -> SQLite. The
// private upstream is the only substituted network edge. Direct header tokens
// must never enter stored payload/metadata, yet live continuity remains exact.
func TestNativeTurnStateNeverPersistsThroughRuntimeHTTP(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, recording := range []string{"full", "off", "failed"} {
			t.Run(fmt.Sprintf("stream=%v/%s", streaming, recording), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				f := newAccountReadFixture(t)
				if recording == "off" {
					f.aggregate.ContentRecording = environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}
				}
				protocol := &f.aggregate.ClientEndpoints[0].ProtocolPlans[0]
				protocol.Transforms = []codelibrary.TransformRevision{{ID: "privacy-transform", Revision: 1, CollectionID: "privacy", DisplayName: "Privacy", PublishedAt: time.Now().UTC(), Policy: messagetransform.Policy{RequestJavaScript: `request.headers["x-fixture"]="ok";`, ResponseJavaScript: `response.headers["x-fixture"]="ok";`}}}
				const sentinel = "r54-private-routing-token-6f8142"
				var mu sync.Mutex
				var observed []string
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					r.Body.Close()
					mu.Lock()
					observed = append(observed, r.Header.Get("X-Codex-Turn-State"))
					mu.Unlock()
					w.Header().Set("X-Codex-Turn-State", sentinel)
					w.Header().Set("X-Openai-Model", "gpt-native")
					body := `{"id":"resp_privacy","object":"response","model":"gpt-native","status":"completed","output":[{"id":"msg_reply","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"reply","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", body)
					} else {
						w.Header().Set("Content-Type", "application/json")
						io.WriteString(w, body)
					}
				}))
				defer upstream.Close()
				endpoint, _ := url.Parse(upstream.URL)
				auth, err := providertransport.NewStaticBearerAuthenticator(f.secrets)
				if err != nil {
					t.Fatal(err)
				}
				var raw rawevidence.Observer = f.runtime.rawEvidence
				if recording == "failed" {
					raw = failingNativeRawObserver{}
				}
				provider, err := providertransport.NewClient(providertransport.ClientOptions{Coordinator: f.runtime.offlineHold, Authenticators: []providertransport.Authenticator{auth}, Transport: privateLongSessionTransport{endpoint}, InstanceIDs: NewCryptographicInstanceIDSource(), Audit: f.runtime.egressCompletion, RawEvidence: raw})
				if err != nil {
					t.Fatal(err)
				}
				defer provider.Shutdown(context.Background())
				annotations, err := clientannotation.Open(ctx, f.secrets, rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				defer annotations.Destroy()
				decisions, err := toolpolicy.New(f.runtime.approvals)
				if err != nil {
					t.Fatal(err)
				}
				recorder := &transformHTTPRecorder{Recorder: f.runtime.contents, sink: f.runtime.contents.(exchangecontent.SourceRecorder), completed: make(chan string, 2)}
				pipeline, err := buildExchange(exchangeBuildRequest{bodyAdmission: f.runtime.bodyAdmission, ownerContext: ctx, actions: f.runtime.offlineHold, accounts: f.runtime.accounts, provider: provider, toolDecisions: decisions, activities: f.runtime.activities, identities: f.runtime.conversationIDs, contents: recorder, clock: SystemClock{}, hold: exchange.DefaultHoldPolicy(), annotations: annotations, rawEvidence: raw})
				if err != nil {
					t.Fatal(err)
				}
				defer pipeline.Shutdown(context.Background())
				previous := f.runtime.exchanges
				f.runtime.exchanges = pipeline
				defer func() { f.runtime.exchanges = previous }()
				proxy := f.serveProxy(t)
				roots := x509.NewCertPool()
				roots.AppendCertsFromPEM(f.runtime.LocalRootCertificate().CertificatePEM())
				transport := &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
				defer transport.CloseIdleConnections()
				client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
				for i := 0; i < 2; i++ {
					body := fmt.Sprintf(`{"model":"gpt-native","stream":%v,"client_metadata":{"turn_id":"privacy-turn"},"input":"hello"}`, streaming)
					req, _ := http.NewRequestWithContext(ctx, "POST", "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Session-Id", "privacy-session")
					req.Header.Set("Thread-Id", "privacy-thread")
					if i > 0 {
						req.Header.Set("X-Codex-Turn-State", sentinel)
					}
					resp, err := client.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					result, readErr := io.ReadAll(resp.Body)
					resp.Body.Close()
					if readErr != nil || resp.StatusCode != 200 || !bytes.Contains(result, []byte("reply")) {
						t.Fatalf("delivery: %d %s %v", resp.StatusCode, result, readErr)
					}
					if resp.Header.Get("X-Codex-Turn-State") != sentinel {
						t.Fatalf("live token changed: %v", resp.Header)
					}
					if resp.Header.Get("X-Openai-Model") != "gpt-native" {
						t.Errorf("known model header lost: %v", resp.Header)
					}
				}
				mu.Lock()
				got := append([]string(nil), observed...)
				mu.Unlock()
				if len(got) != 2 || got[0] != "" || got[1] != sentinel {
					t.Fatalf("live replay=%q", got)
				}
				if recording == "full" {
					ids := []string{}
					for i := 0; i < 2; i++ {
						select {
						case id := <-recorder.completed:
							ids = append(ids, id)
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					}
					layers := map[rawevidence.Layer]bool{}
					deadline := time.Now().Add(3 * time.Second)
					for {
						for _, id := range ids {
							if _, err := f.runtime.rawEvidence.ListExchange(ctx, id); err != nil {
								t.Fatal(err)
							}
							records, err := f.runtime.storage.RawEvidenceRepository().ListExchange(ctx, id)
							if err != nil {
								t.Fatal(err)
							}
							for _, r := range records {
								layers[r.Layer] = true
								if bytes.Contains(r.PayloadMetadata, []byte(sentinel)) || bytes.Contains(r.Body, []byte(sentinel)) {
									t.Errorf("routing token persisted in %s", r.Layer)
								}
							}
						}
						complete := true
						for _, layer := range []rawevidence.Layer{rawevidence.LayerClientIngress, rawevidence.LayerProviderEgress, rawevidence.LayerProviderResponse, rawevidence.LayerTransformRequestInput, rawevidence.LayerTransformResponseInput} {
							complete = complete && layers[layer]
						}
						if complete {
							break
						}
						if time.Now().After(deadline) {
							t.Fatalf("missing actual Raw boundaries: %v", layers)
						}
						time.Sleep(10 * time.Millisecond)
					}
				}

			})
		}
	}
}

type failingNativeRawObserver struct{}

func (failingNativeRawObserver) Observe(context.Context, rawevidence.Observation) (rawevidence.Watermark, error) {
	return rawevidence.Watermark{}, fmt.Errorf("injected recording failure")
}

func TestNativeTurnStateRuntimeShutdownJoinsHeldProviderBody(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	held, exited := make(chan struct{}), make(chan struct{})
	const token = "r54-owned-shutdown-routing-state"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
		call := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Codex-Turn-State", token)
		if call == 1 {
			io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_seed\",\"model\":\"gpt-native\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
			return
		}
		if r.Header.Get("X-Codex-Turn-State") != token {
			t.Error("owned token was not replayed before held body")
		}
		io.WriteString(w, "event: response.created\ndata: ")
		w.(http.Flusher).Flush()
		close(held)
		<-r.Context().Done()
		close(exited)
	}))
	defer upstream.Close()
	endpoint, _ := url.Parse(upstream.URL)
	f := newAccountReadFixtureWithBuilders(t, providerauth.StaticHeaderDriverRef(), runtimeBuildOverrides{provider: func(request providerBuildRequest) (providerRuntime, error) {
		auth, err := providertransport.NewStaticBearerAuthenticator(request.secrets)
		if err != nil {
			return nil, err
		}
		return providertransport.NewClient(providertransport.ClientOptions{EgressDefaults: request.egressDefaults, Coordinator: request.coordinator, Authenticators: []providertransport.Authenticator{auth}, Transport: privateLongSessionTransport{endpoint}, InstanceIDs: request.instanceIDs, Audit: request.audit, RawEvidence: request.rawEvidence})
	}})
	proxy := serveOwnedNativeProxy(t, f)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(f.runtime.LocalRootCertificate().CertificatePEM())
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	send := func(anchor string) error {
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-native","stream":true,"client_metadata":{"turn_id":"shutdown-turn"},"input":"hello"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Session-Id", "shutdown-session")
		req.Header.Set("Thread-Id", "shutdown-thread")
		if anchor != "" {
			req.Header.Set("X-Codex-Turn-State", anchor)
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	}
	if err := send(""); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- send(token) }()
	select {
	case <-held:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := f.runtime.Shutdown(shutdown); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-shutdown.Done():
		t.Fatal("Runtime shutdown retained provider HTTP body")
	}
	select {
	case <-finished:
	case <-shutdown.Done():
		t.Fatal("Runtime shutdown retained native request")
	}
}

func serveOwnedNativeProxy(t *testing.T, f accountReadFixture) *url.URL {
	t.Helper()
	ctx := context.Background()
	draft, err := f.runtime.environments.SaveDraft(ctx, environment.DraftCommand{Candidate: f.aggregate})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.runtime.environments.Preview(ctx, f.aggregate.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := f.runtime.environments.Publish(ctx, preview); err != nil || result.Outcome != environment.CommitOutcomeCommitted {
		t.Fatalf("publish account fixture: %v", err)
	}
	dir := t.TempDir()
	grant, err := f.runtime.captureRuns.Create(ctx, capturerun.CreateCommand{
		CWD: dir, CanonicalExecutablePath: filepath.Join(dir, "codex"), ExecutableLabel: "codex",
		Lifetime: 5 * time.Minute, CatalogRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	capture, err := captureidentity.New(captureidentity.KindManagedRun, grant.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := clienttarget.NewProfile("codex-cli", clienttarget.EnvironmentFacts{CodexBaseURL: "https://chatgpt.com/backend-api/codex"})
	if err != nil {
		t.Fatal(err)
	}
	assignment, _, err := f.runtime.assignments.CreateForLaunch(ctx, captureassignment.CreateCommand{
		Capture: capture, EnvironmentID: f.aggregate.ID, Source: captureassignment.SourceLaunch, ClientProfile: profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if assignment.ClientTarget.ActualOrigin().String() != "https://chatgpt.com/backend-api/codex" {
		t.Fatal("fixture lost the native Codex launch target and its model API base path")
	}

	rules := []connectionpolicy.Rule{{ID: "fixture.chatgpt", Priority: 1, Decision: connectionpolicy.DecisionAllow, Match: connectionpolicy.MatchExactHostPort("chatgpt.com", 443)}}
	if _, err := f.runtime.ConnectionRules().Replace(ctx, f.runtime.ConnectionRules().Current().Revision, rules, connectionpolicy.ModeDenyUnknown); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(f.runtime.ProxyHandler())
	t.Cleanup(func() { _ = f.runtime.Shutdown(context.Background()); server.Close() })
	target, _ := url.Parse(server.URL)
	target.User = url.UserPassword("capture", grant.ProxyCapability.Value())
	return target
}
