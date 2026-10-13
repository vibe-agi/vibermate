package desktopcontrol

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"strings"

	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

const maximumImageBytes = 4 << 20
const maximumImagePixels = 16 << 20

var errImageUnavailable = errors.New("image is not retained")
var errImageUnsupported = errors.New("image format cannot be previewed")
var errImageTooLarge = errors.New("image exceeds preview limits")

type imagePreview struct {
	Schema      string `json:"schema"`
	Fingerprint string `json:"fingerprint"`
	MimeType    string `json:"mimeType"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Data        []byte `json:"dataBase64"`
}

// This is a view of already-authorized evidence, never a URL fetch or file read.
// Match the exact retained fragment, not a caller-supplied path or blob digest.
func retainedImagePreview(revealed rawevidence.RevealedEnvelope, fingerprint string) (imagePreview, error) {
	var result imagePreview
	if revealed.Metadata.PayloadState != rawevidence.PayloadCaptured ||
		(revealed.Metadata.Layer != rawevidence.LayerClientIngress && revealed.Metadata.Layer != rawevidence.LayerProviderEgress) {
		return result, errImageUnavailable
	}
	body := revealed.Payload.Body
	if decoded := rawevidence.DecodeBodyForDisplay(revealed); decoded != nil {
		defer clear(decoded.Body)
		if decoded.State != "decoded" {
			return result, errImageUnavailable
		}
		body = decoded.Body
	}
	if len(body) > 16<<20 || !json.Valid(body) {
		return result, errImageUnavailable
	}
	budget := 20000
	part := findImageFragment(body, fingerprint, 0, &budget)
	if part == nil {
		return result, errImageUnavailable
	}
	var source struct {
		Type   string `json:"type"`
		URL    string `json:"image_url"`
		Source struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if json.Unmarshal(part, &source) != nil {
		return result, errImageUnsupported
	}
	var mime, encoded string
	if source.Type == "input_image" {
		prefix, data, ok := strings.Cut(source.URL, ",")
		if !ok || !strings.HasPrefix(prefix, "data:") || !strings.HasSuffix(prefix, ";base64") {
			return result, errImageUnavailable
		}
		mime, encoded = strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64"), data
	} else if source.Type == "image" && source.Source.Type == "base64" {
		mime, encoded = source.Source.MediaType, source.Source.Data
	} else {
		return result, errImageUnsupported
	}
	if mime != "image/png" && mime != "image/jpeg" {
		return result, errImageUnsupported
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(maximumImageBytes) {
		return result, errImageTooLarge
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return result, errImageUnsupported
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || "image/"+format != mime {
		clear(data)
		return result, errImageUnsupported
	}
	if config.Width > maximumImagePixels/config.Height {
		clear(data)
		return result, errImageTooLarge
	}
	result = imagePreview{Schema: "vibermate-image-preview/v1", Fingerprint: fingerprint, MimeType: mime, Width: config.Width, Height: config.Height, Data: data}
	return result, nil
}

func findImageFragment(raw json.RawMessage, fingerprint string, depth int, budget *int) json.RawMessage {
	if depth > 24 || *budget <= 0 {
		return nil
	}
	*budget--
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	digest := sha256.Sum256(raw)
	if fmt.Sprintf("sha256:%x", digest) == fingerprint {
		return raw
	}
	if raw[0] == '[' {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return nil
		}
		for _, item := range items {
			if found := findImageFragment(item, fingerprint, depth+1, budget); found != nil {
				return found
			}
		}
	} else if raw[0] == '{' {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil {
			return nil
		}
		// Visit structural fields only. Never interpret a quoted script or text as JSON.
		for _, key := range []string{"input", "messages", "content"} {
			if value, ok := object[key]; ok {
				if found := findImageFragment(value, fingerprint, depth+1, budget); found != nil {
					return found
				}
			}
		}
	}
	return nil
}

func writeImagePreview(writer http.ResponseWriter, revealed rawevidence.RevealedEnvelope, fingerprint string) {
	preview, err := retainedImagePreview(revealed, fingerprint)
	switch {
	case errors.Is(err, errImageTooLarge):
		writeProblem(writer, http.StatusRequestEntityTooLarge, "image_too_large")
	case errors.Is(err, errImageUnsupported):
		writeProblem(writer, http.StatusUnsupportedMediaType, "image_format_unsupported")
	case err != nil:
		writeProblem(writer, http.StatusGone, "image_unavailable")
	default:
		defer clear(preview.Data)
		writeJSON(writer, http.StatusOK, preview)
	}
}
