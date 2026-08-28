// Package clientannotation issues and removes authenticated, client-visible
// annotations without granting JavaScript access to the signing secret.
package clientannotation

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/secretstore"
)

const (
	signingKeyBytes       = 32
	maximumKindBytes      = 64
	maximumTextBytes      = 16 << 10
	maximumJSONDepth      = 128
	maximumJSONValues     = 200_000
	markerPrefix          = "<!--vibermate:annotation:v1:"
	markerHeaderEnd       = "-->"
	markerSuffix          = "<!--/vibermate:annotation-->"
	signingReferenceValue = "secret://runtime/client-annotation-signing-v1"
)

var ErrUnavailable = errors.New("client annotation signer is unavailable")

// Signer owns one runtime copy of the persistent annotation signing key.
type Signer struct {
	mu        sync.RWMutex
	key       []byte
	destroyed bool
}

func NewSigner(key []byte) (*Signer, error) {
	if len(key) != signingKeyBytes {
		return nil, ErrUnavailable
	}
	return &Signer{key: bytes.Clone(key)}, nil
}

// Open loads or creates the one host-protected key shared across runtime
// restarts. The persisted representation is base64 because SecretStore values
// deliberately reject binary header control bytes.
func Open(
	ctx context.Context,
	store secretstore.Store,
	random io.Reader,
) (*Signer, error) {
	if ctx == nil || store == nil || random == nil {
		return nil, ErrUnavailable
	}
	reference, err := secretstore.ParseReference(signingReferenceValue)
	if err != nil {
		return nil, ErrUnavailable
	}
	key, err := readKey(ctx, store, reference)
	if err == nil {
		return signerFromOwnedKey(key)
	}
	if !errors.Is(err, secretstore.ErrNotFound) {
		return nil, fmt.Errorf("%w: read signing key: %w", ErrUnavailable, err)
	}
	key = make([]byte, signingKeyBytes)
	if _, err := io.ReadFull(random, key); err != nil {
		clear(key)
		return nil, fmt.Errorf("%w: create signing key", ErrUnavailable)
	}
	encoded := []byte(base64.RawURLEncoding.EncodeToString(key))
	value, err := secretstore.NewValue(encoded)
	clear(encoded)
	if err != nil {
		clear(key)
		return nil, ErrUnavailable
	}
	_, replaceErr := store.Replace(ctx, secretstore.ReplaceCommand{
		Reference: reference,
		Value:     value,
	})
	value.Destroy()
	if replaceErr == nil {
		return signerFromOwnedKey(key)
	}
	clear(key)
	if !errors.Is(replaceErr, secretstore.ErrRevisionConflict) {
		return nil, fmt.Errorf("%w: persist signing key: %w", ErrUnavailable, replaceErr)
	}
	key, err = readKey(ctx, store, reference)
	if err != nil {
		return nil, fmt.Errorf("%w: read concurrently created signing key: %w", ErrUnavailable, err)
	}
	return signerFromOwnedKey(key)
}

func signerFromOwnedKey(key []byte) (*Signer, error) {
	defer clear(key)
	return NewSigner(key)
}

func readKey(
	ctx context.Context,
	store secretstore.Store,
	reference secretstore.Reference,
) ([]byte, error) {
	value, err := store.Read(ctx, reference)
	value, err = secretstore.ValidateReaderResult(value, err)
	if err != nil {
		return nil, err
	}
	defer value.Destroy()
	encoded, err := value.CopyBytes()
	if err != nil {
		return nil, err
	}
	defer clear(encoded)
	key, err := base64.RawURLEncoding.DecodeString(string(encoded))
	if err != nil || len(key) != signingKeyBytes {
		clear(key)
		return nil, ErrUnavailable
	}
	return key, nil
}

func (signer *Signer) Issue(kind, text string) (string, error) {
	if !validKind(kind) || !validText(text) {
		return "", errors.New("client annotation is invalid")
	}
	signer.mu.RLock()
	defer signer.mu.RUnlock()
	if signer.destroyed {
		return "", ErrUnavailable
	}
	signature := sign(signer.key, kind, text)
	return markerPrefix + kind + ":" + signature + markerHeaderEnd + text + markerSuffix, nil
}

// StripJSON removes authentic annotations from JSON string values. It returns
// the original bytes exactly when nothing authentic was found.
func (signer *Signer) StripJSON(body []byte) ([]byte, bool, error) {
	signer.mu.RLock()
	defer signer.mu.RUnlock()
	if signer.destroyed {
		return nil, false, ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return bytes.Clone(body), false, nil
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return bytes.Clone(body), false, nil
	}
	values := 0
	changed, bounded := stripValue(value, signer.key, 0, &values)
	if !bounded || !changed {
		return bytes.Clone(body), false, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, false, ErrUnavailable
	}
	return encoded, true, nil
}

func stripValue(value any, key []byte, depth int, values *int) (bool, bool) {
	*values = *values + 1
	if depth > maximumJSONDepth || *values > maximumJSONValues {
		return false, false
	}
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		for name, child := range typed {
			if text, ok := child.(string); ok {
				cleaned, childChanged := stripText(text, key)
				if childChanged {
					typed[name] = cleaned
					changed = true
				}
				continue
			}
			childChanged, bounded := stripValue(child, key, depth+1, values)
			if !bounded {
				return false, false
			}
			changed = changed || childChanged
		}
		return changed, true
	case []any:
		changed := false
		for index, child := range typed {
			if text, ok := child.(string); ok {
				cleaned, childChanged := stripText(text, key)
				if childChanged {
					typed[index] = cleaned
					changed = true
				}
				continue
			}
			childChanged, bounded := stripValue(child, key, depth+1, values)
			if !bounded {
				return false, false
			}
			changed = changed || childChanged
		}
		return changed, true
	default:
		return false, true
	}
}

func stripText(text string, key []byte) (string, bool) {
	searchAt := 0
	keptAt := 0
	var output strings.Builder
	changed := false
	for searchAt < len(text) {
		relativeStart := strings.Index(text[searchAt:], markerPrefix)
		if relativeStart < 0 {
			break
		}
		start := searchAt + relativeStart
		headerStart := start + len(markerPrefix)
		relativeHeaderEnd := strings.Index(text[headerStart:], markerHeaderEnd)
		if relativeHeaderEnd < 0 {
			break
		}
		headerEnd := headerStart + relativeHeaderEnd
		kind, signature, found := strings.Cut(text[headerStart:headerEnd], ":")
		contentStart := headerEnd + len(markerHeaderEnd)
		relativeEnd := strings.Index(text[contentStart:], markerSuffix)
		if relativeEnd < 0 {
			break
		}
		contentEnd := contentStart + relativeEnd
		end := contentEnd + len(markerSuffix)
		content := text[contentStart:contentEnd]
		if found && validKind(kind) && verify(key, kind, content, signature) {
			output.WriteString(text[keptAt:start])
			keptAt = end
			searchAt = end
			changed = true
			continue
		}
		searchAt = start + len(markerPrefix)
	}
	if !changed {
		return text, false
	}
	output.WriteString(text[keptAt:])
	return output.String(), true
}

func sign(key []byte, kind, text string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("v1\x00"))
	_, _ = mac.Write([]byte(kind))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(text))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func verify(key []byte, kind, text, encodedSignature string) bool {
	provided, err := base64.RawURLEncoding.DecodeString(encodedSignature)
	if err != nil {
		return false
	}
	expected, err := base64.RawURLEncoding.DecodeString(sign(key, kind, text))
	return err == nil && hmac.Equal(provided, expected)
}

func validKind(value string) bool {
	if value == "" || len(value) > maximumKindBytes {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			(index > 0 && (character == '-' || character == '_' || character == '.')) {
			continue
		}
		return false
	}
	return true
}

func validText(value string) bool {
	if value == "" || len(value) > maximumTextBytes || !utf8.ValidString(value) ||
		strings.Contains(value, markerPrefix) || strings.Contains(value, markerSuffix) {
		return false
	}
	for _, character := range value {
		if character == unicode.ReplacementChar ||
			(unicode.IsControl(character) && character != '\n' && character != '\t') {
			return false
		}
	}
	return true
}

func (signer *Signer) Destroy() {
	if signer == nil {
		return
	}
	signer.mu.Lock()
	defer signer.mu.Unlock()
	if signer.destroyed {
		return
	}
	clear(signer.key)
	signer.key = nil
	signer.destroyed = true
}
