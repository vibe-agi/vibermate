package rawevidence

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
)

// RedactionSaltBytes is the length of the per-database redaction salt.
const RedactionSaltBytes = 32

// RedactedValue is what remains of a credential header value: proof that the
// field carried a value, how long it was, and whether it is the same value the
// same database saw before. The salt keeps that comparison local, so a digest
// recorded here cannot be matched against a corpus assembled elsewhere.
type RedactedValue struct {
	Digest string `json:"digest"`
	Bytes  int    `json:"bytes"`
}

// Redactor replaces a credential header value with evidence that the field was
// present, without retaining the value.
type Redactor struct {
	salt []byte
}

// NewRedactor binds one per-database salt.
func NewRedactor(salt []byte) (Redactor, error) {
	if len(salt) != RedactionSaltBytes {
		return Redactor{}, errors.New("raw evidence redaction salt is invalid")
	}
	return Redactor{salt: slices.Clone(salt)}, nil
}

func (redactor Redactor) bound() bool {
	return len(redactor.salt) == RedactionSaltBytes
}

// field returns the stored form of one observed header. A credential field
// keeps its name, position and multiplicity and loses its values.
func (redactor Redactor) field(name string, values []string) HeaderField {
	if !NameIsCredential(name) {
		return HeaderField{Name: name, Values: slices.Clone(values)}
	}
	redacted := make([]RedactedValue, 0, len(values))
	for _, value := range values {
		mac := hmac.New(sha256.New, redactor.salt)
		_, _ = mac.Write([]byte(value))
		redacted = append(redacted, RedactedValue{
			Digest: hex.EncodeToString(mac.Sum(nil)),
			Bytes:  len(value),
		})
	}
	return HeaderField{Name: name, Redacted: redacted}
}

// NameIsCredential selects the header fields whose values never reach storage.
//
// The match stays deliberately broad, because the two mistakes are not
// symmetric: a credential this predicate misses is a security failure, while a
// field it over-matches only loses evidence. Breadth therefore wins by default.
//
// The one exception is rate-limit accounting. Both providers name those fields
// after the thing they count — `anthropic-ratelimit-input-tokens-limit`,
// `x-ratelimit-remaining-tokens` — so a bare "token" substring match destroys
// usage evidence that is the whole point of retaining response headers. Those
// names measure a credential's budget; they never carry one.
func NameIsCredential(name string) bool {
	normalized := strings.ToLower(name)
	if strings.Contains(normalized, "ratelimit") ||
		strings.Contains(normalized, "rate-limit") {
		return false
	}
	return normalized == "authorization" ||
		normalized == "proxy-authorization" ||
		normalized == "cookie" || normalized == "set-cookie" ||
		strings.Contains(normalized, "api-key") ||
		strings.Contains(normalized, "apikey") ||
		strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "password") ||
		strings.Contains(normalized, "credential")
}
