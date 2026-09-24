package loopbackproxy_test

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
)

func TestGenericUploadRepresentationsRemainUninspected(t *testing.T) {
	t.Parallel()
	canaryPath := filepath.Join(t.TempDir(), "canary.txt")
	if err := os.WriteFile(canaryPath, []byte("VIBERMATE_SYNTHETIC_CANARY"), 0o600); err != nil {
		t.Fatal(err)
	}
	canary, err := os.ReadFile(canaryPath)
	if err != nil {
		t.Fatal(err)
	}

	var multipartBody bytes.Buffer
	multipartWriter := multipart.NewWriter(&multipartBody)
	part, err := multipartWriter.CreateFormFile("file", "canary.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(canary); err != nil {
		t.Fatal(err)
	}
	if err := multipartWriter.Close(); err != nil {
		t.Fatal(err)
	}

	var gzipBody bytes.Buffer
	gzipWriter := gzip.NewWriter(&gzipBody)
	if _, err := gzipWriter.Write(canary); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	zstdWriter, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	zstdBody := zstdWriter.EncodeAll(canary, nil)
	zstdWriter.Close()

	var zipBody bytes.Buffer
	zipWriter := zip.NewWriter(&zipBody)
	zipPart, err := zipWriter.Create("canary.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zipPart.Write(canary); err != nil {
		t.Fatal(err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	cipherBody := aead.Seal(nil, make([]byte, aead.NonceSize()), canary, nil)

	observer := &rawObserver{}
	fixture := newProxyFixtureWithRawEvidence(t, observer)
	defer fixture.Close(t)
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		sum := sha256.Sum256(body)
		writer.Header().Set("X-Body-Sha256", hex.EncodeToString(sum[:]))
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()
	proxy := &url.URL{
		Scheme: "http",
		Host:   fixture.listener.Addr().String(),
		User:   url.UserPassword("capture", fixture.grant.ProxyCapability.Value()),
	}
	client := &http.Client{Transport: &http.Transport{
		Proxy:             http.ProxyURL(proxy),
		DisableKeepAlives: true,
	}, Timeout: 5 * time.Second}
	samples := []struct {
		name, contentType, contentEncoding string
		body                               []byte
	}{
		{"plain", "text/plain", "", canary},
		{"multipart", multipartWriter.FormDataContentType(), "", multipartBody.Bytes()},
		{"gzip", "application/octet-stream", "gzip", gzipBody.Bytes()},
		{"zstd", "application/octet-stream", "zstd", zstdBody},
		{"base64", "text/plain", "", []byte(base64.StdEncoding.EncodeToString(canary))},
		{"zip", "application/zip", "", zipBody.Bytes()},
		{"client_cipher", "application/octet-stream", "", cipherBody},
	}
	for _, sample := range samples {
		t.Run(sample.name, func(t *testing.T) {
			if (sha256.Sum256(sample.body) == sha256.Sum256(canary)) != (sample.name == "plain") {
				t.Fatal("transformed request body unexpectedly matches the file digest")
			}
			request, err := http.NewRequest(http.MethodPost, origin.URL+"/upload", bytes.NewReader(sample.body))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", sample.contentType)
			if sample.contentEncoding != "" {
				request.Header.Set("Content-Encoding", sample.contentEncoding)
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			sum := sha256.Sum256(sample.body)
			if response.StatusCode != http.StatusNoContent ||
				response.Header.Get("X-Body-Sha256") != hex.EncodeToString(sum[:]) {
				t.Fatalf("forward status=%d digest=%q", response.StatusCode, response.Header.Get("X-Body-Sha256"))
			}
		})
	}
	if len(observer.snapshot()) != 0 || len(fixture.exchanges.Requests()) != 0 {
		t.Fatal("generic uploads were misrepresented as inspected HTTP evidence")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		connections, connectionErr := fixture.connections.List(context.Background(), connectionevent.PageRequest{Limit: 100})
		attempts, attemptErr := fixture.egress.List(context.Background(), egressaudit.PageRequest{Limit: 100})
		if connectionErr != nil || attemptErr != nil {
			t.Fatalf("read upload evidence: connection=%v egress=%v", connectionErr, attemptErr)
		}
		closed := 0
		for _, record := range connections.Items {
			if record.Decision == connectionevent.DecisionAllow && record.Decryption != connectionevent.DecryptionBlind {
				t.Fatalf("generic upload claimed content inspection: %+v", record)
			}
			if record.Phase == connectionevent.PhaseClosed {
				closed++
				if record.BytesUp < uint64(len(canary)) {
					t.Fatalf("completed upload counted fewer bytes than its smallest body: %d", record.BytesUp)
				}
			}
		}
		terminal := 0
		for _, record := range attempts.Items {
			if record.Attempt.Terminal() {
				terminal++
				if !strings.HasPrefix(record.Attempt.TargetOrigin(), "http://") {
					t.Fatalf("cleartext upload claimed HTTPS: %q", record.Attempt.TargetOrigin())
				}
				if record.Attempt.BytesOut() < int64(len(canary)) {
					t.Fatalf("completed egress attempt counted fewer bytes than its smallest body: %d", record.Attempt.BytesOut())
				}
			}
		}
		if closed == len(samples) && terminal == len(samples) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal upload evidence incomplete: connections=%d attempts=%d", closed, terminal)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDirectUploadDoesNotCreateProxyEvidence(t *testing.T) {
	t.Parallel()
	observer := &rawObserver{}
	fixture := newProxyFixtureWithRawEvidence(t, observer)
	defer fixture.Close(t)
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	response, err := client.Post(origin.URL+"/upload", "text/plain", bytes.NewReader([]byte("synthetic canary")))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	page, err := fixture.connections.List(context.Background(), connectionevent.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 0 || len(observer.snapshot()) != 0 {
		t.Fatalf("direct request created proxy evidence: records=%d raw=%d error=%v", len(page.Items), len(observer.snapshot()), err)
	}
}
