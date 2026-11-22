package clientadapter_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

// The identity proofs of ADR 0026. A recognized-basis protection is proven on
// Linux with a signer-shaped grant, because Linux has no signature tier. These
// tests are the link that makes those proofs apply to a Mac user: they show
// that a release, as published for darwin/arm64, is still recognized by its
// publisher's signature as the client the policy describes. They read the
// file on disk and never run it.

// requireSignerRecognitionVariable turns every reason these tests would skip
// into a failure. The macOS jobs set it, so a host without the recognized tier
// or a job that named no binary cannot pass by proving nothing.
const requireSignerRecognitionVariable = "VIBERMATE_REQUIRE_SIGNER_RECOGNITION"

type installedIdentity struct {
	// variable names the absolute path of the installed darwin binary.
	variable string
	// label is the invocation label recognition is evaluated against and a
	// run stores.
	label  string
	client string
	recipe clientadapter.LaunchRecipe
	// withheld is what the index's shape rule removes from the policy for
	// this label. A label with a signer that signs a child rather than the
	// launched file gets no authority-bearing protection.
	withheld clientadapter.Feature
}

func TestInstalledClaudeIsRecognizedByItsSigner(t *testing.T) {
	t.Parallel()

	requireRecognizedBySigner(t, installedIdentity{
		variable: "VIBERMATE_CLAUDE_ACCEPTANCE",
		label:    "claude",
		client:   "claude-code",
		recipe:   clientadapter.LaunchNodeEnvProxy,
	})
}

// The Codex binary is the native vendor/aarch64-apple-darwin/bin/codex, not
// the codex.js wrapper: the wrapper signer resolves only the nested npm
// layout, and npm installs the flat one.
func TestInstalledCodexIsRecognizedByItsSigner(t *testing.T) {
	t.Parallel()

	requireRecognizedBySigner(t, installedIdentity{
		variable: "VIBERMATE_CODEX_ACCEPTANCE",
		label:    "codex",
		client:   "codex-cli",
		recipe:   clientadapter.LaunchCodexResponsesHTTP,
		withheld: clientadapter.FeatureCoreOwnedStreamingFallback |
			clientadapter.FeatureStructuredWorkspaceTools,
	})
}

func requireRecognizedBySigner(t *testing.T, identity installedIdentity) {
	t.Helper()

	required := signerRecognitionRequired(t)
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		if required {
			t.Fatalf(
				"%s=1, but this host is %s/%s; the recognized tier exists only on darwin/arm64",
				requireSignerRecognitionVariable,
				runtime.GOOS,
				runtime.GOARCH,
			)
		}
		t.Skip("the recognized tier exists only on darwin/arm64")
	}
	binary := os.Getenv(identity.variable)
	if binary == "" {
		if required {
			t.Fatalf(
				"%s=1, but %s names no installed binary",
				requireSignerRecognitionVariable,
				identity.variable,
			)
		}
		t.Skipf("requires %s to name an installed binary", identity.variable)
	}
	if !filepath.IsAbs(binary) {
		t.Fatalf("%s must be an absolute path", identity.variable)
	}

	verifier := builtInVerifier(t)
	detection := verifyInstalled(t, verifier, identity.label, binary)
	t.Logf(
		"%s: recognition=%s status=%s catalog revision %d, %s",
		identity.label,
		detection.Recognition,
		detection.Status,
		detection.CatalogRevision,
		detection.CanonicalPath,
	)
	if detection.ExecutableLabel != identity.label {
		t.Fatalf(
			"the detection is for label %q, want %q",
			detection.ExecutableLabel,
			identity.label,
		)
	}
	if detection.CatalogRevision != clientadapter.BuiltInCatalog().Revision() {
		t.Fatalf(
			"the detection is at catalog revision %d, want the built-in %d",
			detection.CatalogRevision,
			clientadapter.BuiltInCatalog().Revision(),
		)
	}
	switch detection.Recognition {
	case clientadapter.RecognitionRecognized:
		requireSignerEvidence(t, identity, detection)
	case clientadapter.RecognitionVerified:
		requireReleaseEvidence(t, identity, detection)
	default:
		t.Fatalf(
			"the installed %s is neither recognized by its publisher's signature nor a catalogued release: recognition=%q status=%q",
			identity.label,
			detection.Recognition,
			detection.Status,
		)
	}

	// A run stores this label and catalog revision, and the index built from
	// the host's own catalog is what names its client.
	index, err := clientadapter.NewRecognizedClients(
		clientadapter.BuiltInCatalog(),
		clientadapter.ProtectionsOn,
	)
	if err != nil {
		t.Fatal(err)
	}
	client, found := index.Client(
		detection.CatalogRevision,
		detection.ExecutableLabel,
	)
	want := clientadapter.RecognizedPolicyFeatures(identity.client) &^
		identity.withheld
	if !found || client != (clientadapter.RecognizedClient{
		ID:          identity.client,
		Policy:      want,
		Protections: want,
	}) {
		t.Fatalf(
			"the index names %+v (found %t) for %q, want %q with protections %#x",
			client,
			found,
			detection.ExecutableLabel,
			identity.client,
			uint64(want),
		)
	}
	t.Logf("%s: the index names %s with protections %#x", identity.label, client.ID, uint64(client.Protections))

	// codesign --strict refuses a file whose contents no longer match its
	// signature, and a modified file matches no catalogued digest.
	t.Run("refuses/modified_copy", func(t *testing.T) {
		// The copy must keep the label and be executable, or it would be
		// refused for its name or mode and the refusal would prove nothing.
		unmodified := copyInstalled(t, detection.CanonicalPath, identity.label)
		copied := verifyInstalled(t, verifier, identity.label, unmodified)
		if copied.Recognition != clientadapter.RecognitionRecognized &&
			copied.Recognition != clientadapter.RecognitionVerified {
			t.Fatalf(
				"an unmodified copy was %q, so the refusal of a modified one proves nothing",
				copied.Recognition,
			)
		}

		modified := copyInstalled(t, detection.CanonicalPath, identity.label)
		flipMiddleByte(t, modified)
		refused := verifyInstalled(t, verifier, identity.label, modified)
		t.Logf(
			"%s: a modified copy is recognition=%s status=%s",
			identity.label,
			refused.Recognition,
			refused.Status,
		)
		if refused.Recognition == clientadapter.RecognitionRecognized ||
			refused.Recognition == clientadapter.RecognitionVerified ||
			refused.Signer != nil ||
			refused.Evidence != nil {
			t.Fatalf(
				"a modified copy of the installed %s was accepted: recognition=%q",
				identity.label,
				refused.Recognition,
			)
		}
	})
}

func signerRecognitionRequired(t *testing.T) bool {
	t.Helper()

	switch value := os.Getenv(requireSignerRecognitionVariable); value {
	case "":
		return false
	case "1":
		return true
	default:
		t.Fatalf(
			"%s must be 1 or unset, not %q",
			requireSignerRecognitionVariable,
			value,
		)
		return false
	}
}

func verifyInstalled(
	t *testing.T,
	verifier *clientadapter.ReleaseVerifier,
	label string,
	binary string,
) clientadapter.Detection {
	t.Helper()

	detection, err := verifier.Verify(context.Background(), clientadapter.Request{
		Command:        []string{label},
		CWD:            "/",
		ExecutablePath: binary,
	})
	if err != nil {
		t.Fatalf("verify %s: %v", label, err)
	}
	return detection
}

func requireSignerEvidence(
	t *testing.T,
	identity installedIdentity,
	detection clientadapter.Detection,
) {
	t.Helper()

	signer := detection.Signer
	if signer == nil || detection.Evidence != nil {
		t.Fatalf(
			"a recognized detection must carry signer evidence and no release evidence: signer=%v evidence=%v",
			signer != nil,
			detection.Evidence != nil,
		)
	}
	t.Logf(
		"%s: signer %s revision %d, %s, %s, signed %s",
		identity.label,
		signer.ID,
		signer.Revision,
		signer.InstallShape,
		signer.LaunchRecipe,
		signer.SignedPath,
	)
	if signer.ID != identity.client {
		t.Fatalf("signed by %q, want %q", signer.ID, identity.client)
	}
	// The launched file itself carries the signature. Only this shape may
	// receive an authority-bearing protection.
	if signer.InstallShape != clientadapter.InstallNativeSingleBinary {
		t.Fatalf(
			"install shape is %q, want %q",
			signer.InstallShape,
			clientadapter.InstallNativeSingleBinary,
		)
	}
	if signer.SignedPath != detection.CanonicalPath {
		t.Fatalf(
			"the platform checked %q, want the launched file %q",
			signer.SignedPath,
			detection.CanonicalPath,
		)
	}
	if signer.LaunchRecipe != identity.recipe {
		t.Fatalf("recipe is %q, want %q", signer.LaunchRecipe, identity.recipe)
	}
	if signer.CatalogRevision != detection.CatalogRevision {
		t.Fatalf(
			"signer evidence is at catalog revision %d, the detection at %d",
			signer.CatalogRevision,
			detection.CatalogRevision,
		)
	}
}

func requireReleaseEvidence(
	t *testing.T,
	identity installedIdentity,
	detection clientadapter.Detection,
) {
	t.Helper()

	evidence := detection.Evidence
	if evidence == nil || detection.Signer != nil {
		t.Fatalf(
			"a verified detection must carry release evidence and no signer evidence: evidence=%v signer=%v",
			evidence != nil,
			detection.Signer != nil,
		)
	}
	t.Logf(
		"%s: catalogued release %s %s revision %d, %s",
		identity.label,
		evidence.ID,
		evidence.Version,
		evidence.Revision,
		evidence.InstallShape,
	)
	if evidence.ID != identity.client {
		t.Fatalf("the release is %q, want %q", evidence.ID, identity.client)
	}
}

// copyInstalled copies the file at source to a fresh directory under label,
// executable, without holding the whole file in memory.
func copyInstalled(t *testing.T, source, label string) string {
	t.Helper()

	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	destination := filepath.Join(t.TempDir(), label)
	output, err := os.OpenFile(
		destination,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o755,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	return destination
}

// flipMiddleByte inverts the byte at len/2, as TestAModifiedCopyOfASignedClientIsRefused
// does, which lies inside the pages a signature covers.
func flipMiddleByte(t *testing.T, path string) {
	t.Helper()

	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("the installed binary is empty")
	}
	offset := info.Size() / 2
	middle := make([]byte, 1)
	if _, err := file.ReadAt(middle, offset); err != nil {
		t.Fatal(err)
	}
	middle[0] ^= 0xFF
	if _, err := file.WriteAt(middle, offset); err != nil {
		t.Fatal(err)
	}
}
