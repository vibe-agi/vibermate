package localca

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadRootCertificateExportsOnlyTheInitializedPublicRoot(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "local-ca")
	options := DefaultOptions(directory, context.Background())
	authority, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	want := authority.Certificate().CertificatePEM()
	if err := authority.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	exported, err := ReadRootCertificate(directory, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !exported.Valid() || !bytes.Equal(exported.CertificatePEM(), want) ||
		bytes.Contains(exported.CertificatePEM(), []byte("PRIVATE KEY")) {
		t.Fatal("server-local export did not return exactly one public Root")
	}
	if err := os.Chmod(filepath.Join(directory, rootCertFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRootCertificate(directory, time.Now()); err == nil {
		t.Fatal("public Root export accepted weakened file permissions")
	}
}
