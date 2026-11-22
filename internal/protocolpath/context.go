package protocolpath

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// ProviderTargetKind names the kind of upstream a provider side encodes for.
// The exchange maps the frozen target origin onto it; this package never
// inspects origins itself.
type ProviderTargetKind uint8

const (
	// TargetGeneric is any other origin, including compatible services.
	TargetGeneric ProviderTargetKind = iota
	// TargetOpenAIPlatform is the public OpenAI API.
	TargetOpenAIPlatform
	// TargetChatGPTCodex is the ChatGPT Codex backend (read only by stage 2).
	TargetChatGPTCodex
)

// ClientCredentialAccount is the account identity a reasoning binding uses
// when the request travels on the client's own credential.
const ClientCredentialAccount = "client-credential"

// ProviderContext carries frozen target facts to a provider side. The
// exchange builds it once per attempt from the frozen selection and
// credential. The zero value is a generic target with no binding and no
// rejected-seal memory.
type ProviderContext struct {
	Target ProviderTargetKind
	// ReasoningBinding is NewReasoningBinding(endpointID, accountID); a seal
	// minted under one binding is replayed only under the same binding.
	ReasoningBinding string
	// RejectedSeal reports whether the provider already refused a seal with
	// this digest; nil means there is no memory.
	RejectedSeal func(digest [32]byte) bool
}

// SealRejected reports whether the rejected-seal memory holds seal.
func (providerContext ProviderContext) SealRejected(seal protocolcore.ReasoningSeal) bool {
	return providerContext.RejectedSeal != nil && providerContext.RejectedSeal(protocolcore.SealDigest(seal))
}

// NewReasoningBinding derives the binding that ties a reasoning seal to one
// upstream endpoint and account: the first 32 hex digits of
// SHA-256("vibermate/reasoning\x00" + endpointID + "\x00" + accountID). A
// client-passthrough credential uses ClientCredentialAccount; a dry run uses
// the account it would select, or "" when it resolves none. The binding is a
// hash, so no account identifier reaches the client.
func NewReasoningBinding(endpointID, accountID string) string {
	digest := sha256.Sum256([]byte("vibermate/reasoning\x00" + endpointID + "\x00" + accountID))
	return hex.EncodeToString(digest[:])[:32]
}
