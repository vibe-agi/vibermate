package rawevidence

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/providerauth"
)

type headerSourceFixture struct {
	value   string
	err     error
	lookups []providerauth.HeaderLookup
}

func (source *headerSourceFixture) ReadOverwriteHeader(_ context.Context, lookup providerauth.HeaderLookup) (string, error) {
	source.lookups = append(source.lookups, lookup)
	return source.value, source.err
}

type failingHeaderAudit struct{ *memoryRepository }

func (*failingHeaderAudit) AppendRevealAudit(context.Context, RevealAudit) error {
	return errors.New("audit offline")
}

func TestHeaderRevealRequiresFrozenFingerprintAndLeavesStoredEvidenceRedacted(t *testing.T) {
	ctx := context.Background()
	repository := &memoryRepository{}
	manager, err := Open(ctx, Options{Repository: repository, Random: rand.Reader, Clock: fixedClock{value: testObservation().ObservedAt}, Config: DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	observation := testObservation()
	observation.Layer = LayerProviderEgress
	observation.AttemptID = "attempt-1"
	observation.AccountID = "account-1"
	observation.AccountRevision = 2
	observation.CredentialEpoch = 3
	observation.UpstreamEndpointID = "upstream-1"
	observation.UpstreamEndpointRevision = 4
	observation.Headers = http.Header{"User-Agent": {"private-overwrite-agent"}}
	observation.ProtectedHeaderNames = []string{"User-Agent"}
	mark, err := manager.Observe(ctx, observation)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Flush(ctx, mark); err != nil {
		t.Fatal(err)
	}
	record := repository.snapshot()[0]
	request := HeaderRevealRequest{RevealRequest: RevealRequest{EnvelopeID: record.EnvelopeID, ActorID: "owner-1"}, Name: "user-agent"}
	source := &headerSourceFixture{value: "private-overwrite-agent"}
	revealed, err := manager.RevealHeader(ctx, request, source)
	if err != nil || revealed.Value != source.value || revealed.Name != "User-Agent" {
		t.Fatalf("reveal=%#v err=%v", revealed, err)
	}
	if len(source.lookups) != 1 || source.lookups[0].CredentialEpoch != 3 || source.lookups[0].AccountRevision != 2 || source.lookups[0].UpstreamEndpointRevision != 4 {
		t.Fatalf("unfrozen lookup: %#v", source.lookups)
	}
	if len(repository.audits) != 1 || repository.audits[0].Outcome != RevealSucceeded {
		t.Fatal("missing success audit")
	}
	ordinary, err := manager.Reveal(ctx, request.RevealRequest)
	if err != nil {
		t.Fatal(err)
	}
	if len(ordinary.Payload.Headers[0].Values) != 0 || len(ordinary.Payload.Headers[0].Redacted) != 1 || bytes.Contains(repository.snapshot()[0].PayloadMetadata, []byte(source.value)) {
		t.Fatal("explicit header reveal modified stored / ordinary evidence")
	}
	for _, value := range []string{"same-length-but-wrong!!", "current-overwrite-after-rotation", ""} {
		source.value = value
		result, err := manager.RevealHeader(ctx, request, source)
		if !errors.Is(err, ErrPayloadUnavailable) || result.Value != "" {
			t.Fatalf("mismatched value revealed: %#v err=%v", result, err)
		}
	}
	if repository.audits[len(repository.audits)-1].Outcome != RevealUnavailable {
		t.Fatal("missing denied audit")
	}
	source.value = "private-overwrite-agent"
	manager.repository = &failingHeaderAudit{memoryRepository: repository}
	if result, err := manager.RevealHeader(ctx, request, source); err == nil || result.Value != "" {
		t.Fatal("plaintext returned without durable audit")
	}
	manager.repository = repository
	manager.clock = fixedClock{value: record.ExpiresAt.Add(time.Second)}
	before := len(source.lookups)
	if _, err := manager.RevealHeader(ctx, request, source); !errors.Is(err, ErrPayloadUnavailable) || len(source.lookups) != before {
		t.Fatal("expired evidence accessed credentials")
	}
	manager.clock = fixedClock{value: observation.ObservedAt}
	repository.mu.Lock()
	repository.records[0].Layer = LayerClientIngress
	repository.mu.Unlock()
	if _, err := manager.RevealHeader(ctx, request, source); !errors.Is(err, ErrPayloadUnavailable) || len(source.lookups) != before {
		t.Fatal("client ingress accessed account credentials")
	}
}
