package desktopcontrol

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"github.com/vibe-agi/vibermate/internal/nativemcp"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"net/http"
	"net/url"
	"unicode/utf8"
)

const nativeContentPageLimit = 128 << 10

type nativeContentCursor struct {
	Query   string `json:"q"`
	Offset  int    `json:"s"`
	Expires int64  `json:"e"`
}

func nativeContentQuery(op nativetool.InspectionOperation, part string, descriptor *nativetool.InspectionContentDescriptor) string {
	b, _ := json.Marshal(struct {
		Version                  string
		Capture, Operation, Part string
		Revision                 uint64
		Reference                string
	}{nativetool.InspectionVersion(op.Token), op.Operation.Capture.Key(), op.Operation.ID.String(), part, op.Operation.Revision, descriptor.Reference})
	digest := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

type NativeContentBlock struct {
	Kind      string  `json:"kind"`
	Text      *string `json:"text,omitempty"`
	MIMEType  string  `json:"mimeType,omitempty"`
	ImageData *string `json:"imageData,omitempty"`
	Offset    int     `json:"offset"`
	Total     int     `json:"total"`
	Complete  bool    `json:"complete"`
}
type NativeContentPageDTO struct {
	InspectionVersion string               `json:"inspectionVersion"`
	OperationID       string               `json:"operationId"`
	CaptureKey        string               `json:"captureKey"`
	Part              string               `json:"part"`
	BodyDigest        string               `json:"bodyDigest"`
	Blocks            []NativeContentBlock `json:"blocks"`
	NextCursor        string               `json:"nextCursor,omitempty"`
}

func (h *Handler) signNativeCursor(c nativeContentCursor) string {
	payload, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, h.nativeContentKey[:])
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(append(payload, mac.Sum(nil)...))
}
func (h *Handler) readNativeCursor(raw string) (nativeContentCursor, error) {
	var c nativeContentCursor
	if len(raw) == 0 || len(raw) > 512 {
		return c, nativetool.ErrInvalid
	}
	signed, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || len(signed) <= 32 {
		return c, nativetool.ErrInvalid
	}
	payload, signature := signed[:len(signed)-32], signed[len(signed)-32:]
	mac := hmac.New(sha256.New, h.nativeContentKey[:])
	mac.Write(payload)
	// No parsing or offsets from caller-controlled bytes before authentication.
	if !hmac.Equal(signature, mac.Sum(nil)) || decodeStrictJSON(payload, &c) != nil || c.Offset < 0 || c.Expires <= h.clock.Now().UTC().Unix() || c.Expires > h.clock.Now().UTC().Unix()+300 {
		return c, nativetool.ErrInvalid
	}
	return c, nil
}
func (h *Handler) getNativeContent(w http.ResponseWriter, r *http.Request) {
	capture, ok := h.nativeCapture(w, r, false)
	if !ok {
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		nativeProblem(w, nativetool.ErrInvalid)
		return
	}
	for key, values := range query {
		if len(values) != 1 || key != "part" && key != "cursor" {
			nativeProblem(w, nativetool.ErrInvalid)
			return
		}
	}
	part := query.Get("part")
	if part != "tool_arguments" && part != "tool_result" {
		nativeProblem(w, nativetool.ErrInvalid)
		return
	}
	op, err := h.nativeOperation(r.Context(), capture, r.PathValue("operationId"))
	if err != nil {
		nativeProblem(w, err)
		return
	}
	descriptor := h.nativePart(op, part)
	if descriptor.State != "available" {
		writeProblem(w, http.StatusGone, ReasonCode("native_content_"+descriptor.State))
		return
	}
	bodyDescriptor := op.Result
	if part == "tool_arguments" {
		bodyDescriptor = op.Arguments
	}
	binding := nativeContentQuery(op, part, bodyDescriptor)
	cursor := nativeContentCursor{Query: binding, Expires: h.clock.Now().UTC().Unix() + 300}
	if raw, has := query["cursor"]; has {
		cursor, err = h.readNativeCursor(raw[0])
		if err != nil || cursor.Query != binding {
			nativeProblem(w, nativetool.ErrInvalid)
			return
		}
	}

	// Repository reads are bounded to128KiB, but digest/codec verification may
	// require the full retained body (at most16MiB). No smaller I/O claim.
	body := []byte{}
	defer func() { clear(body) }()
	for offset := 0; ; {
		chunk, err := h.nativeInspection.ReadContent(r.Context(), nativetool.InspectionContentRequest{Token: op.Token, Capture: capture, OperationID: op.Operation.ID, Part: nativetool.ContentPart(part), Descriptor: *bodyDescriptor, Offset: offset, Limit: nativeContentPageLimit})
		if err != nil || len(body)+len(chunk) > 16<<20 {
			clear(chunk)
			writeProblem(w, 410, "native_content_unavailable")
			return
		}
		body = append(body, chunk...)
		offset += len(chunk)
		done := len(chunk) < nativeContentPageLimit
		clear(chunk)
		if done {
			break
		}
	}
	digest := nativetool.Digest(sha256.Sum256(body))
	if digest.String() != descriptor.Digest {
		writeProblem(w, 410, "native_content_unavailable")
		return
	}
	projection, err := nativemcp.DecodeRecordedToolContent(part, body)
	if err != nil {
		writeProblem(w, 410, "native_content_unavailable")
		return
	}
	defer func() {
		for _, block := range projection.Blocks {
			clear(block.ImageBytes)
		}
	}()
	result := NativeContentPageDTO{InspectionVersion: nativetool.InspectionVersion(op.Token), OperationID: op.Operation.ID.String(), CaptureKey: capture.Key(), Part: part, BodyDigest: descriptor.Digest, Blocks: []NativeContentBlock{}}
	position := 0
	start := cursor.Offset
	remaining := false
	for _, block := range projection.Blocks {
		value := block.Text
		if block.Kind == "image" {
			value = base64.StdEncoding.EncodeToString(block.ImageBytes)
		}
		length := len(value)
		if start >= position+length {
			position += length
			continue
		}
		local := start - position
		if local < 0 || local > length || block.Kind == "image" && local%4 != 0 || !utf8.ValidString(value[local:]) {
			nativeProblem(w, nativetool.ErrInvalid)
			return
		}
		for local < length {
			end := min(length, local+(64<<10))
			for end > local && !utf8.ValidString(value[local:end]) {
				end--
			}
			if block.Kind == "image" {
				end -= (end - local) % 4
			}
			for {
				fragment := value[local:end]
				next := NativeContentBlock{Kind: block.Kind, Offset: local, Total: length, Complete: end == length}
				if block.Kind == "image" {
					next.MIMEType = block.MIMEType
					next.ImageData = &fragment
				} else {
					next.Text = &fragment
				}
				candidate := result
				candidate.Blocks = append(append([]NativeContentBlock{}, result.Blocks...), next)
				encoded, _ := json.Marshal(candidate)
				if len(encoded) <= nativeContentPageLimit-600 && len(candidate.Blocks) <= 24 {
					result = candidate
					start = position + end
					local = end
					break
				}
				if len(result.Blocks) > 0 {
					remaining = true
					break
				}
				end = local + (end-local)/2
				for end > local && !utf8.ValidString(value[local:end]) {
					end--
				}
				if block.Kind == "image" {
					end -= (end - local) % 4
				}
				if end == local {
					writeProblem(w, 410, "native_content_unavailable")
					return
				}
			}
			if remaining {
				break
			}
		}
		if remaining {
			break
		}
		position += length
	}
	if cursor.Offset > position && !remaining || len(result.Blocks) == 0 && cursor.Offset != 0 {
		nativeProblem(w, nativetool.ErrInvalid)
		return
	}
	if remaining {
		cursor.Offset = start
		result.NextCursor = h.signNativeCursor(cursor)
		if len(result.NextCursor) > 512 {
			nativeProblem(w, nativetool.ErrInvalid)
			return
		}
	}
	// Clear/delete, retention expiry and late operation updates cannot publish
	// a projection after the read started with an older descriptor/revision.
	current, err := h.nativeOperation(r.Context(), capture, op.Operation.ID.String())
	nowPart := h.nativePart(current, part)
	if err != nil || current.Operation.Revision != op.Operation.Revision || current.Token != op.Token || nowPart.State != "available" || nowPart.Digest != descriptor.Digest {
		writeProblem(w, 410, "native_content_unavailable")
		return
	}
	currentDescriptor := current.Result
	if part == "tool_arguments" {
		currentDescriptor = current.Arguments
	}
	if currentDescriptor == nil || nativeContentQuery(current, part, currentDescriptor) != binding {
		writeProblem(w, 410, "native_content_unavailable")
		return
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > nativeContentPageLimit {
		writeProblem(w, 410, "native_content_unavailable")
		return
	}
	if err = h.nativeInspection.Check(r.Context(), op.Token); err != nil {
		writeProblem(w, 410, "native_content_unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(encoded)
}
