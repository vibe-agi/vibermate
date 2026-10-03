package desktopcontrol

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

func TestRetainedImagePreviewRejectsUnretainedAndUnsafeImages(t *testing.T) {
	var small bytes.Buffer
	if err := png.Encode(&small, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	bomb := bytes.Clone(small.Bytes())
	binary.BigEndian.PutUint32(bomb[16:20], maximumImagePixels+1)
	binary.BigEndian.PutUint32(bomb[29:33], crc32.ChecksumIEEE(bomb[12:29]))
	for _, test := range []struct {
		name, url string
		want      error
	}{
		{"remote URL is never fetched", "https://example.invalid/private.png", errImageUnavailable},
		{"local path is never read", "file:///private/example.png", errImageUnavailable},
		{"unsupported format", "data:image/svg+xml;base64,PHN2Zy8+", errImageUnsupported},
		{"wrong declared format", "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(small.Bytes()), errImageUnsupported},
		{"invalid base64", "data:image/png;base64,%%%%", errImageUnsupported},
		{"pixel limit", "data:image/png;base64," + base64.StdEncoding.EncodeToString(bomb), errImageTooLarge},
		{"byte limit", "data:image/png;base64," + strings.Repeat("A", base64.StdEncoding.EncodedLen(maximumImageBytes)+4), errImageTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			fragment, _ := json.Marshal(map[string]string{"type": "input_image", "image_url": test.url})
			revealed := rawevidence.RevealedEnvelope{
				Metadata: rawevidence.EnvelopeMetadata{Layer: rawevidence.LayerClientIngress, PayloadState: rawevidence.PayloadCaptured},
				Payload:  rawevidence.Payload{Body: []byte(`{"input":[{"content":[` + string(fragment) + `]}]}`)},
			}
			digest := sha256.Sum256(fragment)
			preview, err := retainedImagePreview(revealed, fmt.Sprintf("sha256:%x", digest))
			if !errors.Is(err, test.want) || len(preview.Data) != 0 {
				t.Fatalf("unsafe image accepted: %v", err)
			}
			if _, err := retainedImagePreview(revealed, "sha256:"+strings.Repeat("0", 64)); !errors.Is(err, errImageUnavailable) {
				t.Fatal("image lookup accepted a different fragment")
			}
		})
	}
}
