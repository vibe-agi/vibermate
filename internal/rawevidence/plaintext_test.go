package rawevidence

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestOpenNeedsNoSecretStore(t *testing.T) {
	t.Parallel()

	manager, err := Open(context.Background(), Options{
		Repository: &memoryRepository{},
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x41}, 4096)),
		Clock:      fixedClock{value: time.Unix(1_790_000_000, 0).UTC()},
		Config:     redactionTestConfig(),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Body bytes must leave the writer as bytes. They used to be a []byte field
// inside a struct that was JSON-marshalled, which base64-expanded every body by
// a third before it was stored — 164 MB of the measured corpus.
func TestStoredEnvelopeKeepsBodyBytesOutOfItsPayloadMetadata(t *testing.T) {
	t.Parallel()

	repository := &memoryRepository{}
	manager, err := Open(context.Background(), Options{
		Repository: repository,
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x41}, 4096)),
		Clock:      fixedClock{value: time.Unix(1_790_000_000, 0).UTC()},
		Config:     redactionTestConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Shutdown(context.Background()) }()

	observation := testObservation()
	observation.Body = bytes.Repeat([]byte{0x00, 0xFF, 0x10}, 4096)
	observation.ContentType = "application/octet-stream"
	watermark, err := manager.Observe(context.Background(), observation)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Flush(context.Background(), watermark); err != nil {
		t.Fatal(err)
	}

	record := repository.snapshot()[0]
	if !bytes.Equal(record.Body, observation.Body) {
		t.Fatalf("stored body = %d bytes, observed %d",
			len(record.Body), len(observation.Body))
	}
	if len(record.PayloadMetadata) > 4096 {
		t.Fatalf(
			"payload metadata is %d bytes for a %d byte body; the body is still inside it",
			len(record.PayloadMetadata), len(observation.Body),
		)
	}
}

func TestStoredPayloadIsReadableWithoutAKey(t *testing.T) {
	t.Parallel()

	repository := &memoryRepository{}
	manager, err := Open(context.Background(), Options{
		Repository: repository,
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x41}, 4096)),
		Clock:      fixedClock{value: time.Unix(1_790_000_000, 0).UTC()},
		Config:     redactionTestConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Shutdown(context.Background()) }()

	watermark, err := manager.Observe(context.Background(), testObservation())
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Flush(context.Background(), watermark); err != nil {
		t.Fatal(err)
	}

	records := repository.snapshot()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	payload, err := DecodePayload(
		records[0].PayloadMetadata, records[0].Body,
	)
	if err != nil {
		t.Fatalf("DecodePayload() error = %v", err)
	}
	if string(payload.Body) != `{"secret":"private"}` {
		t.Fatalf("stored body = %q", payload.Body)
	}
	// The credential is gone because it was redacted, not because the bytes
	// were sealed. Nothing here holds a key.
	if bytes.Contains(records[0].PayloadMetadata, []byte("Bearer private-token")) {
		t.Fatal("stored payload retained a credential value")
	}
}
