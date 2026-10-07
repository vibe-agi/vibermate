package productruntime

// This adapter is also compiled against the archived formal v0.1.23 source.
// Only the admission binding in task7BindAdmission is version-specific.
import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/accountoperation"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/clientannotation"
	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/toolpolicy"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

const task7OldSource = "2237b3ae35e8097987b685f6bcd08224bde62f0f"

type task7Receipt struct {
	Source           string
	Captures         []string
	Credentials      []string // disposable synthetic admission only; receipt mode 0600
	Exchanges        []string
	Records          map[string]string
	Snapshot         map[string][]string
	PrimaryKeyHashes map[string]map[string]string
	SchemaHash       string
}
type task7Fixture struct {
	t           *testing.T
	ctx         context.Context
	runtime     *Runtime
	options     Options
	pipeline    exchangeRuntime
	server      *httptest.Server
	provider    *providertransport.Client
	annotations *clientannotation.Signer
	proxy       proxyRuntime
	transport   *http.Transport
	diagnostics atomic.Int32
	calls       atomic.Int32
	response    string
	inputs      []string
}

func task7Must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func task7Hash(v []byte) string { return fmt.Sprintf("%x", sha256.Sum256(v)) }
func task7JSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	task7Must(t, e)
	return b
}
func task7Write(t *testing.T, name string, v any) {
	t.Helper()
	task7Must(t, os.WriteFile(name, task7JSON(t, v), 0600))
}
func task7Load(t *testing.T, name string, v any) {
	t.Helper()
	b, e := os.ReadFile(name)
	task7Must(t, e)
	task7Must(t, json.Unmarshal(b, v))
}

func task7Open(t *testing.T, ctx context.Context, directory string) *task7Fixture {
	t.Helper()
	paths, e := NewRuntimePaths(directory)
	task7Must(t, e)
	gate, e := offlinehold.New(offlinehold.Config{MaxHeldRequests: 8, MaxHeldBytes: 1 << 20, MaxHoldDuration: time.Second, ReleaseConcurrency: 2})
	task7Must(t, e)
	options := testOptionsWithPaths(t, paths, hostcontract.Desktop(), gate)
	r, e := Start(ctx, options)
	task7Must(t, e)
	f := &task7Fixture{t: t, ctx: ctx, runtime: r, options: options, response: "ordinary-reply"}
	t.Cleanup(func() { f.close() })
	return f
}
func (f *task7Fixture) close() {
	if f.runtime == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if f.transport != nil {
		f.transport.CloseIdleConnections()
	}
	if f.server != nil {
		f.server.Close()
	}
	if f.proxy != nil {
		task7Must(f.t, f.proxy.Shutdown(ctx))
	}
	if f.pipeline != nil {
		task7Must(f.t, f.pipeline.Shutdown(ctx))
	}
	if f.provider != nil {
		task7Must(f.t, f.provider.Shutdown(ctx))
	}
	if f.annotations != nil {
		f.annotations.Destroy()
	}
	task7Must(f.t, f.runtime.Shutdown(ctx))
	f.runtime = nil
	if closer, ok := f.options.Secrets.(interface{ Close() error }); ok {
		task7Must(f.t, closer.Close())
	}
}
func (f *task7Fixture) configure() {
	t, ctx, r := f.t, f.ctx, f.runtime
	material, e := providerauth.NewMaterial("synthetic-task7-provider", nil, nil)
	task7Must(t, e)
	defer material.Destroy()
	encoded, e := material.MarshalBinary()
	task7Must(t, e)
	defer clear(encoded)
	secret, e := secretstore.NewValue(encoded)
	task7Must(t, e)
	defer secret.Destroy()
	account, e := r.accounts.Create(ctx, provideraccount.CreateCommand{ID: "task7-account", DisplayName: "Task7 Synthetic", UpstreamEndpointID: upstreamendpoint.ChatGPTOfficialID, Driver: providerauth.StaticHeaderDriverRef(), Secret: secret})
	task7Must(t, e)
	endpoint, e := r.endpoints.Get(ctx, upstreamendpoint.ChatGPTOfficialID)
	task7Must(t, e)
	for _, mode := range []environment.ContentRecordingMode{environment.ContentRecordingFull, environment.ContentRecordingMetadataOnly, environment.ContentRecordingOff} {
		aggregate, _, _ := compatibilityEnvironment(t, environment.ClientProtocolOpenAIResponses, protocolspec.DialectOpenAIResponses)
		aggregate.ID = environment.EnvironmentID("task7-" + string(mode))
		aggregate.Name = "Task7 " + string(mode)
		aggregate.ContentRecording.Mode = mode
		if mode == environment.ContentRecordingOff {
			aggregate.ContentRecording = environment.ContentRecordingPolicy{Mode: mode}
		}
		route := &aggregate.ClientEndpoints[0].ProtocolPlans[0].Destination.Upstream.Routes[0]
		route.BackendProtocol = string(environment.ClientProtocolOpenAIResponses)
		route.ProviderTarget = environment.ProviderTarget{ID: endpoint.ID.String(), Revision: environment.Revision(endpoint.Revision), Origin: endpoint.Origin, RealmID: endpoint.RealmID, Capabilities: endpoint.Capabilities}
		route.AccountPolicy.FixedAccountID = account.Account.ID.String()
		route.AccountPolicy.Accounts = []environment.RouteAccountReference{{ID: account.Account.ID.String(), Revision: environment.Revision(account.Account.Revision), DisplayName: account.Account.DisplayName}}
		// The real Codex profile scopes the client origin to chatgpt.com.
		aggregate.ClientEndpoints[0].ClientOrigin, e = originidentity.ParseClientOrigin("https://chatgpt.com")
		task7Must(t, e)
		draft, e := r.environments.SaveDraft(ctx, environment.DraftCommand{Candidate: aggregate})
		task7Must(t, e)
		preview, e := r.environments.Preview(ctx, aggregate.ID, draft.Revision)
		task7Must(t, e)
		result, e := r.environments.Publish(ctx, preview)
		task7Must(t, e)
		if result.Outcome != environment.CommitOutcomeCommitted {
			t.Fatal("publish rejected")
		}
	}
}
func (f *task7Fixture) capture(mode environment.ContentRecordingMode) (string, string) {
	t, r := f.t, f.runtime
	grant, e := r.manualCaptures.Create(f.ctx, manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "Task7 " + string(mode), ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	task7Must(t, e)
	capture, e := captureidentity.New(captureidentity.KindManualCapture, grant.Capture.ID)
	task7Must(t, e)
	_, e = r.assignments.Create(f.ctx, captureassignment.CreateCommand{Capture: capture, EnvironmentID: environment.EnvironmentID("task7-" + string(mode)), Source: captureassignment.SourceManualCreate})
	task7Must(t, e)
	return grant.Capture.ID, grant.Credential.Value()
}

type task7PrivateTransport struct{ target *url.URL }

func (p task7PrivateTransport) RoundTrip(r *http.Request, _ providertransport.TransportDispatch) (*http.Response, transportprofile.Evidence, error) {
	if r.URL.Host != "chatgpt.com" {
		return nil, transportprofile.Evidence{}, fmt.Errorf("private fixture rejected destination %s", r.URL.Host)
	}
	c := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = p.target.Scheme
	u.Host = p.target.Host
	c.URL = &u
	c.Host = u.Host
	response, e := http.DefaultTransport.RoundTrip(c)
	return response, transportprofile.Evidence{}, e
}
func (f *task7Fixture) bind(upstream http.Handler) *url.URL {
	t, ctx, r := f.t, f.ctx, f.runtime
	private := httptest.NewServer(upstream)
	t.Cleanup(private.Close)
	target, e := url.Parse(private.URL)
	task7Must(t, e)
	auth, e := providertransport.NewStaticBearerAuthenticator(f.options.Secrets)
	task7Must(t, e)
	f.provider, e = providertransport.NewClient(providertransport.ClientOptions{Coordinator: r.offlineHold, Authenticators: []providertransport.Authenticator{auth}, Transport: task7PrivateTransport{target}, InstanceIDs: NewCryptographicInstanceIDSource(), Audit: r.egressCompletion})
	task7Must(t, e)
	accountReads, e := accountoperation.New(accountoperation.Options{Accounts: r.accounts, Transport: f.provider})
	task7Must(t, e)
	f.annotations, e = clientannotation.Open(ctx, f.options.Secrets, rand.Reader)
	task7Must(t, e)
	decisions, e := toolpolicy.New(r.approvals)
	task7Must(t, e)
	request := exchangeBuildRequest{ownerContext: ctx, actions: r.offlineHold, accounts: r.accounts, provider: f.provider, toolDecisions: decisions, activities: r.activities, identities: r.conversationIDs, contents: r.contents, usage: r.storage.UsageRepository(), clock: SystemClock{}, hold: exchange.DefaultHoldPolicy(), annotations: f.annotations, reportObservationFailure: func(stage string, e error) { f.diagnostics.Add(1); t.Errorf("diagnostic %s: %v", stage, e) }}
	admission, e := captureadmission.NewAuthorizer(r.captureRuns, r.manualCaptures)
	task7Must(t, e)
	policy, e := (connectionpolicy.Snapshot{Revision: 1, Mode: connectionpolicy.ModeDenyUnknown, Rules: []connectionpolicy.Rule{{ID: "task7.chatgpt", Priority: 1, Decision: connectionpolicy.DecisionAllow, Match: connectionpolicy.MatchExactHostPort("chatgpt.com", 443)}}}).Compile()
	task7Must(t, e)
	blind, e := newBlindTunnelDialer(r.offlineHold)
	task7Must(t, e)
	proxyRequest := proxyBuildRequest{ownerContext: ctx, admissions: admission, assignments: r.assignments, original: accountFixtureOriginal{}, accountReads: accountReads, certificates: r.localCA, connections: r.connections, policy: connectionpolicy.NewLive(policy), approvals: r.approvals, blindTunnels: blind, egressAudit: r.egressCompletion, random: rand.Reader}
	task7BindAdmission(r, &request, &proxyRequest)
	f.pipeline, e = buildExchange(request)
	task7Must(t, e)
	proxyRequest.exchanges = f.pipeline
	f.proxy, e = buildProxy(proxyRequest)
	task7Must(t, e)
	f.server = httptest.NewServer(f.proxy)
	proxyURL, e := url.Parse(f.server.URL)
	task7Must(t, e)
	return proxyURL
}
func (f *task7Fixture) ordinaryUpstream(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	if r.Header.Get("Authorization") != "Bearer synthetic-task7-provider" {
		f.t.Error("wrong provider credential")
	}
	var body struct {
		Input []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"input"`
	}
	task7Must(f.t, json.NewDecoder(r.Body).Decode(&body))
	if len(body.Input) != len(f.inputs) {
		f.t.Errorf("upstream count=%d want=%d", len(body.Input), len(f.inputs))
		return
	}
	for i, item := range body.Input {
		if item.Role != "user" || item.Content != f.inputs[i] {
			f.t.Errorf("upstream history %d", i)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	// Literal wire ampersands expand only in stored canonical JSON.
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	task7Must(f.t, encoder.Encode(map[string]any{"id": "resp_task7", "object": "response", "model": "task7", "status": "completed", "output": []any{map[string]any{"id": "msg_task7", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": f.response, "annotations": []any{}}}}}, "usage": map[string]int{"input_tokens": 3, "output_tokens": 2, "total_tokens": 5}}))
}
func (f *task7Fixture) send(proxy *url.URL, credential string, inputs []string, response string) {
	f.inputs, f.response = inputs, response
	p := *proxy
	p.User = url.UserPassword("capture", credential)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(f.runtime.LocalRootCertificate().CertificatePEM()) {
		f.t.Fatal("bad CA")
	}
	transport := &http.Transport{Proxy: http.ProxyURL(&p), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	items := make([]map[string]string, len(inputs))
	for i, v := range inputs {
		items[i] = map[string]string{"role": "user", "content": v}
	}
	request, e := http.NewRequestWithContext(f.ctx, "POST", "https://chatgpt.com/v1/responses", bytes.NewReader(task7JSON(f.t, map[string]any{"model": "task7", "input": items, "stream": false})))
	task7Must(f.t, e)
	request.Header.Set("Content-Type", "application/json")
	result, e := (&http.Client{Transport: transport}).Do(request)
	task7Must(f.t, e)
	body, e := io.ReadAll(result.Body)
	result.Body.Close()
	task7Must(f.t, e)
	if result.StatusCode != 200 {
		f.t.Fatalf("generation status %d: %s", result.StatusCode, body)
	}
	task7Must(f.t, f.pipeline.Drain(f.ctx))
	if f.diagnostics.Load() != 0 {
		f.t.Fatal("recording diagnostics")
	}
}

// Snapshots encode SQLite values with their runtime type before sorting. They
// compare complete stopped rows, including blobs, NULLs, schema and all refs.
func task7Snapshot(t *testing.T, directory string) map[string][]string {
	t.Helper()
	db, e := sql.Open("sqlite", "file:"+filepath.Join(directory, "runtime.db")+"?mode=ro")
	task7Must(t, e)
	defer db.Close()
	rows, e := db.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	task7Must(t, e)
	var tables []string
	for rows.Next() {
		var name string
		task7Must(t, rows.Scan(&name))
		tables = append(tables, name)
	}
	task7Must(t, rows.Err())
	rows.Close()
	result := map[string][]string{}
	for _, table := range append(tables, "sqlite_master") {
		rows, e := db.Query(`SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`)
		task7Must(t, e)
		cols, e := rows.Columns()
		task7Must(t, e)
		result[table] = []string{string(task7JSON(t, cols))}
		for rows.Next() {
			values := make([]any, len(cols))
			args := make([]any, len(cols))
			for i := range args {
				args[i] = &values[i]
			}
			task7Must(t, rows.Scan(args...))
			typed := make([]string, len(cols))
			for i, v := range values {
				typed[i] = fmt.Sprintf("%T:%s", v, task7JSON(t, v))
			}
			result[table] = append(result[table], string(task7JSON(t, typed)))
		}
		task7Must(t, rows.Err())
		rows.Close()
		sort.Strings(result[table][1:])
	}
	return result
}
func task7IDs(t *testing.T, directory string) []string {
	t.Helper()
	db, e := sql.Open("sqlite", "file:"+filepath.Join(directory, "runtime.db")+"?mode=ro")
	task7Must(t, e)
	defer db.Close()
	rows, e := db.Query(`SELECT exchange_id FROM runtime_exchange_contents ORDER BY exchange_id`)
	task7Must(t, e)
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		task7Must(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	task7Must(t, rows.Err())
	return ids
}
func task7VerifyReceipt(receipt task7Receipt, snapshot map[string][]string) error {
	if receipt.Source != task7OldSource {
		return errors.New("wrong formal old source")
	}
	if !reflect.DeepEqual(receipt.Snapshot, snapshot) {
		return errors.New("stopped old snapshot mismatch")
	}
	return nil
}

func task7PrimaryKeyHashes(t *testing.T, directory string, snapshot map[string][]string) map[string]map[string]string {
	t.Helper()
	db, e := sql.Open("sqlite", "file:"+filepath.Join(directory, "runtime.db")+"?mode=ro")
	task7Must(t, e)
	defer db.Close()
	result := map[string]map[string]string{}
	for table, rows := range snapshot {
		var indices []int
		info, e := db.Query(`PRAGMA table_info("` + strings.ReplaceAll(table, `"`, `""`) + `")`)
		task7Must(t, e)
		for info.Next() {
			var cid, notnull, pk int
			var name, typ string
			var defaultValue any
			task7Must(t, info.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk))
			if pk > 0 {
				indices = append(indices, cid)
			}
		}
		task7Must(t, info.Err())
		info.Close()
		if len(indices) == 0 {
			if table == "sqlite_sequence" {
				indices = []int{0}
			} else if table == "sqlite_master" {
				indices = []int{0, 1}
			} else {
				t.Fatalf("fixture table lacks primary key: %s", table)
			}
		}
		result[table] = map[string]string{}
		for _, row := range rows[1:] {
			var cells []string
			task7Must(t, json.Unmarshal([]byte(row), &cells))
			key := make([]string, len(indices))
			for i, j := range indices {
				key[i] = cells[j]
			}
			encoded := string(task7JSON(t, key))
			if _, exists := result[table][encoded]; exists {
				t.Fatal("duplicate snapshot primary key")
			}
			result[table][encoded] = task7Hash([]byte(row))
		}
	}
	return result
}
func task7VerifyStopped(t *testing.T, directory string, receipt task7Receipt) {
	snapshot := task7Snapshot(t, directory)
	task7Must(t, task7VerifyReceipt(receipt, snapshot))
	if receipt.SchemaHash != task7Hash(task7JSON(t, snapshot["sqlite_master"])) || !reflect.DeepEqual(receipt.PrimaryKeyHashes, task7PrimaryKeyHashes(t, directory, snapshot)) {
		t.Fatal("stopped primary-key/schema hashes changed")
	}
}
func task7CheckOldPopulation(t *testing.T, directory string) {
	db, e := sql.Open("sqlite", "file:"+filepath.Join(directory, "runtime.db")+"?mode=ro")
	task7Must(t, e)
	defer db.Close()
	for _, codec := range []string{"identity", "zstd"} {
		var n int
		task7Must(t, db.QueryRow(`SELECT count(*) FROM runtime_exchange_content_blocks WHERE codec=?`, codec).Scan(&n))
		if n == 0 {
			t.Fatalf("old producer missing actual %s rows", codec)
		}
	}
	for _, table := range []string{"manual_captures", "runtime_usage_observations", "runtime_egress_attempts"} {
		var n int
		task7Must(t, db.QueryRow(`SELECT count(*) FROM `+table).Scan(&n))
		if n != 4 {
			t.Fatalf("old population %s=%d", table, n)
		}
	}
	var refs int
	task7Must(t, db.QueryRow(`SELECT count(*) FROM provider_accounts WHERE account_id='task7-account' AND secret_reference<>''`).Scan(&refs))
	if refs != 1 {
		t.Fatal("fake account secret reference missing")
	}
}
func task7Read(t *testing.T, directory string, ids []string) map[string]string {
	t.Helper()
	ctx := context.Background()
	s, e := runtimepersistence.Open(ctx, runtimepersistence.Options{DatabasePath: filepath.Join(directory, "runtime.db"), BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	task7Must(t, e)
	defer func() { task7Must(t, s.Shutdown(ctx)) }()
	result := map[string]string{}
	for _, id := range ids {
		record, e := s.ExchangeContentRepository().Get(ctx, id, time.Now().UTC())
		task7Must(t, e)
		data, e := exchangecontent.CanonicalJSON(record)
		task7Must(t, e)
		result[id] = task7Hash(data)
	}
	return result
}
func task7Backup(t *testing.T, ctx context.Context, root, name string) {
	task7Must(t, runtimedata.Backup(ctx, filepath.Join(root, "live"), filepath.Join(root, name), time.Now().UTC()))
	task7Must(t, runtimedata.ValidateBackup(ctx, filepath.Join(root, name)))
	var manifest struct {
		Recoverable struct{ ProviderCredentials, HostKeychain bool }
	}
	task7Load(t, filepath.Join(root, name, "backup-manifest.json"), &manifest)
	if manifest.Recoverable.ProviderCredentials || manifest.Recoverable.HostKeychain {
		t.Fatal("credential boundary changed")
	}
	if _, e := os.Stat(filepath.Join(root, name, "server-secrets")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("provider secrets exported")
	}
}
func TestUpgradeDataAcceptancePhase(t *testing.T) {
	root, phase := os.Getenv("TASK7_ROOT"), os.Getenv("TASK7_PHASE")
	if phase == "" {
		t.Skip("explicit isolated phase driver required")
	}
	if !filepath.IsAbs(root) || !strings.HasPrefix(filepath.Base(root), "vibermate-task7.") {
		t.Fatal("invalid disposable root")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	live := filepath.Join(root, "live")
	receiptPath := filepath.Join(root, "old-receipt.json")
	switch phase {
	case "backup-pre":
		if task7ProducerSource != task7OldSource {
			t.Fatal("old executable required for PRE backup")
		}
		var receipt task7Receipt
		task7Load(t, receiptPath, &receipt)
		task7VerifyStopped(t, live, receipt)
		task7Backup(t, ctx, root, "pre-upgrade-backup")
	case "produce":
		if task7ProducerSource != task7OldSource {
			t.Fatal("producer must be built from actual formal old archive")
		}
		if _, e := os.Stat(live); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("producer target exists")
		}
		f := task7Open(t, ctx, live)
		f.configure()
		proxy := f.bind(http.HandlerFunc(f.ordinaryUpstream))
		receipt := task7Receipt{Source: task7ProducerSource}
		for i, mode := range []environment.ContentRecordingMode{environment.ContentRecordingFull, environment.ContentRecordingFull, environment.ContentRecordingMetadataOnly, environment.ContentRecordingOff} {
			id, credential := f.capture(mode)
			receipt.Captures = append(receipt.Captures, id)
			receipt.Credentials = append(receipt.Credentials, credential)
			input := fmt.Sprintf("old-prefix-%d", i)
			if i == 1 {
				input += strings.Repeat("ordinary-compressible-", 512)
			}
			f.send(proxy, credential, []string{input}, "ordinary-reply")
		}
		if f.calls.Load() != 4 {
			t.Fatal("old traffic incomplete")
		}
		f.close()
		_, e := runtimepersistence.ValidateOfflineDatabase(ctx, filepath.Join(live, "runtime.db"))
		task7Must(t, e)
		receipt.Exchanges = task7IDs(t, live)
		if len(receipt.Exchanges) != 3 {
			t.Fatalf("Full/MetadataOnly/Off rows %d", len(receipt.Exchanges))
		}
		receipt.Records = task7Read(t, live, receipt.Exchanges)
		receipt.Snapshot = task7Snapshot(t, live)
		receipt.PrimaryKeyHashes = task7PrimaryKeyHashes(t, live, receipt.Snapshot)
		receipt.SchemaHash = task7Hash(task7JSON(t, receipt.Snapshot["sqlite_master"]))
		task7CheckOldPopulation(t, live)
		task7Write(t, receiptPath, receipt)
		task7Backup(t, ctx, root, "pre-upgrade-backup")
	case "upgrade", "verify-upgrade":
		task7Upgrade(t, ctx, root)
	case "verify-restores":
		if task7ProducerSource != task7OldSource {
			t.Fatal("old reader identity required")
		}
		var receipt task7Receipt
		task7Load(t, receiptPath, &receipt)
		restored := filepath.Join(root, "old-restored")
		task7VerifyStopped(t, restored, receipt)
		if !reflect.DeepEqual(task7Read(t, restored, receipt.Exchanges), receipt.Records) {
			t.Fatal("PRE restored ordinary hashes changed")
		}
		post := filepath.Join(root, "post-for-old-reader")
		if !reflect.DeepEqual(task7Read(t, post, receipt.Exchanges), receipt.Records) {
			t.Fatal("POST ordinary hashes changed")
		}
		var ids []string
		task7Load(t, filepath.Join(root, "new-exchanges.json"), &ids)
		s, e := runtimepersistence.Open(ctx, runtimepersistence.Options{DatabasePath: filepath.Join(post, "runtime.db"), BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
		task7Must(t, e)
		defer s.Shutdown(ctx)
		for _, id := range ids {
			record, e := s.ExchangeContentRepository().Get(ctx, id, time.Now().UTC())
			if !errors.Is(e, exchangecontent.ErrInvalidEvidence) || !reflect.DeepEqual(record, exchangecontent.Record{}) {
				t.Fatalf("old reader failed to explicitly refuse %s: %v", id, e)
			}
			t.Logf("old reader refused new-only exchange=%s error=%v empty=true", id, e)
		}
	default:
		t.Fatal("unknown phase")
	}
}
func TestUpgradeReceiptControls(t *testing.T) {
	good := task7Receipt{Source: task7OldSource, Snapshot: map[string][]string{"rows": {"one", "two"}}}
	if task7VerifyReceipt(good, good.Snapshot) != nil {
		t.Fatal("valid receipt rejected")
	}
	wrong := good
	wrong.Source = "candidate"
	if task7VerifyReceipt(wrong, good.Snapshot) == nil {
		t.Fatal("wrong-source control accepted")
	}
	if task7VerifyReceipt(good, map[string][]string{"rows": {"one"}}) == nil {
		t.Fatal("missing history control accepted")
	}
}
