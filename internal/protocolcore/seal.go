package protocolcore

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
)

// A reasoning seal travels through a foreign client as an opaque string the
// client stores and echoes: Claude Code keeps it as a thinking block's
// signature (stage 1), a Responses client as a reasoning item's
// encrypted_content (stage 3). One textual form serves both directions:
//
//	vmr1.<base64url-nopad(JSON header)>.<payload verbatim>   Responses seals
//	vma1.<base64url-nopad(JSON header)>.<payload verbatim>   Anthropic seals
//
// The payload is the provider's opaque value byte for byte, so a seal is only
// its header longer and nothing is encoded twice. There is no MAC: a seal is
// minted and consumed on the user's own machine, and a forged one can only
// make the provider refuse that same user's request. The binding is a hash,
// so no account identifier enters the client's transcript.
const (
	responsesSealPrefix = "vmr1."
	anthropicSealPrefix = "vma1."
	// MaxReasoningSealHeaderBytes bounds the decoded JSON header.
	MaxReasoningSealHeaderBytes = 1 << 10
)

var sealHeaderEncoding = base64.RawURLEncoding.Strict()

// The header members, in their one canonical order.
type responsesSealHeader struct {
	ItemID     string `json:"i"`
	Model      string `json:"m"`
	Binding    string `json:"b"`
	TextSource string `json:"t,omitempty"`
}

type anthropicSealHeader struct {
	Model   string `json:"m"`
	Binding string `json:"b"`
	Kind    string `json:"k"`
}

const (
	anthropicSealKindThinking = "thinking"
	anthropicSealKindRedacted = "redacted"
)

// FormatReasoningSeal renders a seal as the string a foreign client stores.
// source is the TextSource of the reasoning the seal belongs to; a Responses
// seal records ReasoningTextContent so a replay rebuilds raw reasoning text
// rather than a summary. Anthropic seals carry no text source.
func FormatReasoningSeal(seal ReasoningSeal, source ReasoningTextSource) (string, error) {
	if seal.IsZero() {
		return "", errors.New("reasoning seal is empty")
	}
	if err := seal.Validate(); err != nil {
		return "", err
	}
	var prefix string
	var header any
	switch seal.Format {
	case SealResponsesEncrypted:
		value := responsesSealHeader{ItemID: seal.ItemID, Model: seal.Model, Binding: seal.Binding}
		switch source {
		case "", ReasoningTextSummary:
		case ReasoningTextContent:
			value.TextSource = string(ReasoningTextContent)
		default:
			return "", errors.New("reasoning text source is unsupported")
		}
		prefix, header = responsesSealPrefix, value
	case SealAnthropicSignature, SealAnthropicRedacted:
		if source != "" && source != ReasoningTextContent {
			return "", errors.New("Anthropic reasoning seal text source is unsupported")
		}
		kind := anthropicSealKindThinking
		if seal.Format == SealAnthropicRedacted {
			kind = anthropicSealKindRedacted
		}
		prefix, header = anthropicSealPrefix, anthropicSealHeader{Model: seal.Model, Binding: seal.Binding, Kind: kind}
	default:
		return "", errors.New("reasoning seal format is unsupported")
	}
	encoded, err := canonicalSealHeader(header)
	if err != nil {
		return "", err
	}
	if len(encoded) > MaxReasoningSealHeaderBytes {
		return "", errors.New("reasoning seal header exceeds its limit")
	}
	var builder strings.Builder
	builder.Grow(len(prefix) + sealHeaderEncoding.EncodedLen(len(encoded)) + 1 + len(seal.Opaque))
	builder.WriteString(prefix)
	builder.WriteString(sealHeaderEncoding.EncodeToString(encoded))
	builder.WriteByte('.')
	builder.WriteString(seal.Opaque)
	return builder.String(), nil
}

// ParseReasoningSeal recognises only seals FormatReasoningSeal produced. Any
// other value, including a genuine provider signature and every malformed
// seal, is foreign: ok is false and the value must be treated as opaque.
// Only the first two dots delimit, so the payload may contain dots.
func ParseReasoningSeal(value string) (ReasoningSeal, ReasoningTextSource, bool) {
	var format ReasoningSealFormat
	var rest string
	switch {
	case strings.HasPrefix(value, responsesSealPrefix):
		format, rest = SealResponsesEncrypted, value[len(responsesSealPrefix):]
	case strings.HasPrefix(value, anthropicSealPrefix):
		rest = value[len(anthropicSealPrefix):]
	default:
		return ReasoningSeal{}, "", false
	}
	encodedHeader, payload, found := strings.Cut(rest, ".")
	if !found || encodedHeader == "" ||
		sealHeaderEncoding.DecodedLen(len(encodedHeader)) > MaxReasoningSealHeaderBytes ||
		!base64URLAlphabet(encodedHeader) {
		return ReasoningSeal{}, "", false
	}
	header, err := sealHeaderEncoding.DecodeString(encodedHeader)
	if err != nil || len(header) > MaxReasoningSealHeaderBytes {
		return ReasoningSeal{}, "", false
	}
	var seal ReasoningSeal
	var source ReasoningTextSource
	if format == SealResponsesEncrypted {
		var decoded responsesSealHeader
		if !decodeCanonicalSealHeader(header, &decoded) {
			return ReasoningSeal{}, "", false
		}
		switch decoded.TextSource {
		case "":
		case string(ReasoningTextContent):
			source = ReasoningTextContent
		default:
			return ReasoningSeal{}, "", false
		}
		seal = ReasoningSeal{Format: format, ItemID: decoded.ItemID, Model: decoded.Model, Binding: decoded.Binding, Opaque: payload}
	} else {
		var decoded anthropicSealHeader
		if !decodeCanonicalSealHeader(header, &decoded) {
			return ReasoningSeal{}, "", false
		}
		switch decoded.Kind {
		case anthropicSealKindThinking:
			seal.Format = SealAnthropicSignature
		case anthropicSealKindRedacted:
			seal.Format = SealAnthropicRedacted
		default:
			return ReasoningSeal{}, "", false
		}
		seal.Model, seal.Binding, seal.Opaque = decoded.Model, decoded.Binding, payload
	}
	if seal.Validate() != nil {
		return ReasoningSeal{}, "", false
	}
	return seal, source, true
}

// SealDigest identifies a seal for the rejected-seal memory. It covers the
// format, item ID, model, binding and payload, each length-prefixed so no two
// different seals share a digest input.
func SealDigest(seal ReasoningSeal) [32]byte {
	hash := sha256.New()
	_, _ = hash.Write([]byte("vibermate/reasoning-seal\x00"))
	var length [8]byte
	for _, field := range []string{string(seal.Format), seal.ItemID, seal.Model, seal.Binding, seal.Opaque} {
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(field))
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

// base64URLAlphabet admits only the unpadded URL alphabet. The decoder itself
// skips line breaks, which would let two spellings carry one header.
func base64URLAlphabet(value string) bool {
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func canonicalSealHeader(header any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(header); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

// decodeCanonicalSealHeader accepts a header only when re-encoding it gives
// the same bytes: exactly the known members, in order, with no whitespace,
// duplicates or alternative escapes.
func decodeCanonicalSealHeader(header []byte, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(header))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.More() {
		return false
	}
	canonical, err := canonicalSealHeader(target)
	return err == nil && bytes.Equal(canonical, header)
}
