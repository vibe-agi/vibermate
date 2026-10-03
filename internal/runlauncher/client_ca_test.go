package runlauncher

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/serverconnection"
	"github.com/vibe-agi/vibermate/internal/servercontrol"
)

func TestRemoteCAExportUsesExistingLoginAndRejectsChangedMaterial(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	clock := fixedRemoteClock{now: now}
	options := localca.DefaultOptions(filepath.Join(t.TempDir(), "root"), ctx)
	root, err := localca.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Shutdown(ctx) })
	cert := root.Certificate().CertificatePEM()
	fingerprint := root.Identity().Fingerprint()
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{33}, 32))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != servercontrol.RuntimeRootCAPath || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", 401)
			return
		}
		_ = json.NewEncoder(w).Encode(servercontrol.RuntimeRootCA{Schema: servercontrol.RuntimeRootCASchema, CertificatePEM: string(cert), Fingerprint: fingerprint})
	}))
	t.Cleanup(server.Close)
	target, _ := serverconnection.ParseTarget(server.URL)
	directory := t.TempDir()
	store, err := serverconnection.OpenLoginStore(filepath.Join(directory, "login"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePublicCA(cert, fingerprint, now); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePublicCA(append(cert, []byte("PRIVATE KEY")...), fingerprint, now); err == nil {
		t.Fatal("CA with extra material accepted")
	}
	if _, err := ValidatePublicCA(append([]byte("unexpected material\n"), cert...), fingerprint, now); err == nil {
		t.Fatal("CA with leading material accepted")
	}
	if _, err := ValidatePublicCA(cert, strings.Repeat("0", 64), now); err == nil {
		t.Fatal("mismatched CA fingerprint accepted")
	}
	if _, err := ValidatePublicCA(cert, fingerprint, now.AddDate(30, 0, 0)); err == nil {
		t.Fatal("expired CA accepted")
	}
	if _, err := RemoteRootCertificate(ctx, RemoteConfig{Target: target, StateDirectory: directory, Clock: clock, Random: rand.Reader}, time.Second); err == nil {
		t.Fatal("public CA export bypassed login")
	}
	credential, err := serverconnection.NewLoginCredential(serverconnection.LoginCredentialInput{Target: target, InstanceID: "instance.test", UserID: "user.test", Username: "alice", SessionID: "session.test", SessionToken: token, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(credential); err != nil {
		t.Fatal(err)
	}
	material, err := RemoteRootCertificate(ctx, RemoteConfig{Target: target, StateDirectory: directory, Clock: clock, Random: rand.Reader}, time.Second)
	if err != nil || material.Fingerprint != fingerprint || material.CertificatePEM != string(cert) {
		t.Fatalf("authenticated CA export failed: %v", err)
	}
	digest := sha256.Sum256(root.Certificate().CertificateDER())
	if hex.EncodeToString(digest[:]) != fingerprint {
		t.Fatal("CA fingerprint inconsistent")
	}
}
