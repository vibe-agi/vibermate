// Package nativetool owns native tool evidence and frozen authority contracts.
// Digests and observed IDs never constitute an approval or a send capability.
package nativetool

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

var (
	ErrInvalid     = errors.New("native tool input is invalid")
	ErrNotFound    = errors.New("native tool evidence is unavailable")
	ErrConflict    = errors.New("native tool revision conflicts")
	ErrDenied      = errors.New("native tool authority denied")
	ErrReplay      = errors.New("native tool replay denied")
	ErrUnavailable = errors.New("native tool store is unavailable")
)

const (
	MaxRPCBytes        = 1 << 20
	MaxObservedIDBytes = 512
	MaxIntentGroup     = 128
)

type ID string
type OperationID = ID
type Digest [32]byte

func NewID(value string) (ID, error) {
	if len(value) != 32 {
		return "", ErrInvalid
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", ErrInvalid
		}
	}
	return ID(value), nil
}
func NewOperationID(value string) (OperationID, error) { return NewID(value) }
func (id ID) String() string                           { return string(id) }
func (digest Digest) String() string                   { return hex.EncodeToString(digest[:]) }

type Decision string

const (
	DecisionAllowOnce Decision = "allow-once"
	DecisionDeny      Decision = "deny"
)

type RequestKind string

const (
	RequestUnclassified  RequestKind = "unclassified"
	RequestInitialize    RequestKind = "initialize"
	RequestInitialized   RequestKind = "initialized"
	RequestToolsList     RequestKind = "tools_list"
	RequestToolsCall     RequestKind = "tools_call"
	RequestPing          RequestKind = "ping"
	RequestCancel        RequestKind = "cancel"
	RequestServerReply   RequestKind = "server_reply"
	RequestGetStream     RequestKind = "get_stream"
	RequestDeleteSession RequestKind = "delete_session"
)

func (kind RequestKind) Validate() error {
	switch kind {
	case RequestUnclassified, RequestInitialize, RequestInitialized, RequestToolsList, RequestToolsCall, RequestPing, RequestCancel, RequestServerReply, RequestGetStream, RequestDeleteSession:
		return nil
	}
	return ErrInvalid
}

type BindingInput struct {
	Kind                               RequestKind
	Plan                               environment.ClientToolPlan
	Capture                            captureidentity.Reference
	AssignmentRevision, OwnerEpoch     uint64
	SessionID                          ID
	SessionRevision, CatalogGeneration uint64
	CatalogDigest                      *Digest
	ParentOperationID                  *ID
	ServerRequestDigest                *Digest
	ObservedCallID, ExactRPC           []byte
	Intents                            []protocolcore.ToolIntent
	PublicToolDefinition               *protocolcore.ToolDefinition
	CatalogEvidence                    *CatalogEvidence
	SensitiveObservation               *SensitiveObservation
	SensitiveContext                   *SensitiveBindingContext
	SensitiveToolContext               *SensitiveToolContext
}
type Binding struct {
	kind                                                    RequestKind
	plan                                                    environment.ClientToolPlan
	capture                                                 captureidentity.Reference
	assignmentRevision, ownerEpoch                          uint64
	sessionID                                               ID
	sessionRevision, catalogGeneration                      uint64
	catalogDigest                                           *Digest
	parentOperationID                                       *ID
	serverRequestDigest                                     *Digest
	requestDigest, paramsDigest, groupDigest, bindingDigest Digest
	replayKey                                               *Digest
	safeCallRefs                                            []string
	toolName                                                string
	rpcIDDigest                                             *Digest
	cancelledRPCIDDigest                                    *Digest
	sensitiveTool                                           bool
}

func NewBinding(input BindingInput) (Binding, error) {
	// Sensitive callbacks must select the keyed branch before any raw digest.
	sensitive := input.SensitiveObservation != nil || input.SensitiveContext != nil || input.SensitiveToolContext != nil
	if input.Kind == RequestServerReply && !sensitive {
		var response map[string]json.RawMessage
		var result map[string]json.RawMessage
		_ = json.Unmarshal(input.ExactRPC, &response)
		_ = json.Unmarshal(response["result"], &result)
		if _, recognized := result["action"]; recognized {
			return Binding{}, ErrDenied
		}
	}
	if input.Kind.Validate() != nil || input.Kind == RequestUnclassified || input.Plan.Validate() != nil || input.Capture.Validate() != nil ||
		!revision(input.AssignmentRevision) || !revision(input.OwnerEpoch) || !revision(input.SessionRevision) || input.CatalogGeneration > math.MaxInt64 ||
		len(input.ExactRPC) > MaxRPCBytes || len(input.ObservedCallID) > MaxObservedIDBytes || len(input.Intents) > MaxIntentGroup {
		return Binding{}, ErrInvalid
	}
	if _, err := NewID(string(input.SessionID)); err != nil {
		return Binding{}, ErrInvalid
	}
	if (input.CatalogGeneration == 0) != (input.CatalogDigest == nil) || (input.ParentOperationID == nil) != (input.ServerRequestDigest == nil) {
		return Binding{}, ErrInvalid
	}
	if input.ParentOperationID != nil {
		if _, err := NewID(string(*input.ParentOperationID)); err != nil {
			return Binding{}, ErrInvalid
		}
	}
	if input.Kind == RequestServerReply {
		if input.ParentOperationID == nil {
			return Binding{}, ErrInvalid
		}
	} else if input.ParentOperationID != nil {
		return Binding{}, ErrInvalid
	}
	if (input.Kind == RequestInitialize || input.Kind == RequestInitialized) && input.CatalogGeneration != 0 {
		return Binding{}, ErrInvalid
	}
	if input.Kind == RequestToolsCall {
		if input.CatalogGeneration == 0 || len(input.Intents) != 1 {
			return Binding{}, ErrInvalid
		}
	} else if len(input.Intents) != 0 || len(input.ObservedCallID) != 0 || input.PublicToolDefinition != nil || input.CatalogEvidence != nil {
		return Binding{}, ErrInvalid
	}
	params, err := validateEnvelope(input)
	if err != nil {
		return Binding{}, ErrInvalid
	}
	group, refs, err := intentEvidence(input.Intents)
	if err != nil {
		return Binding{}, ErrInvalid
	}
	if input.PublicToolDefinition != nil {
		definition := input.PublicToolDefinition.Clone()
		if definition.Validate() != nil || definition.Name != input.Intents[0].Call.Name || definition.EffectiveKind() != input.Intents[0].Call.EffectiveKind() {
			return Binding{}, ErrInvalid
		}
		// A caller-supplied definition cannot prove membership in CatalogDigest.
		// Keep public metadata empty until a reviewed catalog-proof contract exists.
	}
	var requestDigest, paramsDigest, groupDigest, sensitiveRPC, sensitiveParent Digest
	if sensitive {
		if input.Kind == RequestToolsCall {
			requestDigest, paramsDigest, groupDigest, sensitiveRPC, err = sensitiveToolBindingDigests(input, params, group)
		} else {
			requestDigest, paramsDigest, groupDigest, sensitiveRPC, sensitiveParent, err = sensitiveBindingDigests(input)
		}
		if err != nil {
			return Binding{}, err
		}
		if input.Kind == RequestServerReply {
			input.ServerRequestDigest = &sensitiveParent
		}
	} else {
		requestDigest, paramsDigest, groupDigest = Digest(sha256.Sum256(input.ExactRPC)), Digest(sha256.Sum256(params)), Digest(sha256.Sum256(group))
	}
	binding := Binding{kind: input.Kind, plan: input.Plan, capture: input.Capture, assignmentRevision: input.AssignmentRevision, ownerEpoch: input.OwnerEpoch,
		sessionID: input.SessionID, sessionRevision: input.SessionRevision, catalogGeneration: input.CatalogGeneration, catalogDigest: copyPointer(input.CatalogDigest),
		parentOperationID: copyPointer(input.ParentOperationID), serverRequestDigest: copyPointer(input.ServerRequestDigest),
		requestDigest: requestDigest, paramsDigest: paramsDigest, groupDigest: groupDigest, safeCallRefs: refs, sensitiveTool: sensitive && input.Kind == RequestToolsCall}
	if input.CatalogEvidence != nil {
		binding.toolName, err = input.CatalogEvidence.member(input)
		if err != nil {
			return Binding{}, err
		}
	}
	if sensitive {
		binding.rpcIDDigest = &sensitiveRPC
	} else if input.Kind != RequestGetStream && input.Kind != RequestDeleteSession {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal(input.ExactRPC, &envelope)
		identity := envelope["id"]
		if len(identity) == 0 {
			binding.rpcIDDigest = copyPointer(&binding.requestDigest)
		} else {
			decoder := json.NewDecoder(bytes.NewReader(identity))
			decoder.UseNumber()
			var value any
			_ = decoder.Decode(&value)
			digest := hashJSON(struct {
				Domain string
				ID     any
			}{"native-rpc-id/v1", value})
			binding.rpcIDDigest = &digest
		}
		if input.Kind == RequestCancel {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(envelope["params"], &fields)
			digest := rpcIdentityDigest(fields["requestId"])
			binding.cancelledRPCIDDigest = &digest
		}
	}
	if len(input.ObservedCallID) > 0 {
		// Scope correlation to the actual service, independently of mutable
		// Environment/route/owner/session generations. The parser supplies an
		// opaque type-sensitive observed identity; it is never send authority.
		key := hashJSON(struct {
			Domain       string
			Capture      captureidentity.Reference
			Origin, Path string
			Contract     environment.ClientToolContract
			Observed     []byte
		}{"native-observed-call/v1", input.Capture, input.Plan.Route().TargetOrigin.String(), input.Plan.Route().Path, input.Plan.Route().Contract, slices.Clone(input.ObservedCallID)})
		binding.replayKey = &key
	}
	route := input.Plan.Route()
	binding.bindingDigest = bindingDigestV1(BindingEvidence{input.Kind, input.Capture, input.Plan.EnvironmentID(), input.Plan.EnvironmentRevision(), input.Plan.EnvironmentDigest(), route, input.Plan.ContentRecording(), input.Plan.PolicySet(), input.AssignmentRevision, input.OwnerEpoch, input.SessionID, input.SessionRevision, input.CatalogGeneration, binding.catalogDigest, binding.parentOperationID, binding.serverRequestDigest, binding.requestDigest, binding.paramsDigest, binding.groupDigest, binding.replayKey, binding.toolName, binding.rpcIDDigest, binding.cancelledRPCIDDigest})
	return binding, nil
}
func revision(value uint64) bool { return value > 0 && value <= math.MaxInt64 }
func copyPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
func hashJSON(value any) Digest {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("native evidence encoding failed")
	}
	return Digest(sha256.Sum256(encoded))
}

func validateEnvelope(input BindingInput) ([]byte, error) {
	if input.Kind == RequestGetStream || input.Kind == RequestDeleteSession {
		if len(input.ExactRPC) != 0 {
			return nil, ErrInvalid
		}
		return nil, nil
	}
	// NewJSONObject enforces one complete object and duplicate-name rejection
	// recursively. The selected legacy protocol rejects batches.
	if _, err := protocolcore.NewJSONObject(input.ExactRPC, MaxRPCBytes); err != nil {
		return nil, err
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(input.ExactRPC, &envelope); err != nil {
		return nil, err
	}
	var version, method string
	if json.Unmarshal(envelope["jsonrpc"], &version) != nil || version != "2.0" {
		return nil, ErrInvalid
	}
	params := envelope["params"]
	if len(params) > 0 {
		if _, err := protocolcore.NewJSONObject(params, MaxRPCBytes); err != nil {
			return nil, err
		}
	}
	if input.Kind == RequestServerReply {
		_, result := envelope["result"]
		_, failure := envelope["error"]
		if len(envelope["method"]) != 0 || len(params) != 0 || result == failure || !validRPCID(envelope["id"]) {
			return nil, ErrInvalid
		}
		// Whole exact response (including result/error/meta) remains in RequestDigest.
		return nil, nil
	}
	if json.Unmarshal(envelope["method"], &method) != nil || len(envelope["result"]) != 0 || len(envelope["error"]) != 0 {
		return nil, ErrInvalid
	}
	expected := map[RequestKind]string{RequestInitialize: "initialize", RequestInitialized: "notifications/initialized", RequestToolsList: "tools/list", RequestToolsCall: "tools/call", RequestPing: "ping", RequestCancel: "notifications/cancelled"}[input.Kind]
	if method != expected || expected == "" {
		return nil, ErrInvalid
	}
	if input.Kind == RequestInitialized || input.Kind == RequestCancel {
		if len(envelope["id"]) != 0 {
			return nil, ErrInvalid
		}
	} else if !validRPCID(envelope["id"]) {
		return nil, ErrInvalid
	}
	var fields map[string]json.RawMessage
	if len(params) > 0 {
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, err
		}
	}
	switch input.Kind {
	case RequestInitialize:
		var protocol string
		if json.Unmarshal(fields["protocolVersion"], &protocol) != nil || protocol != "2025-06-18" {
			return nil, ErrInvalid
		}
	case RequestCancel:
		if !validRPCID(fields["requestId"]) {
			return nil, ErrInvalid
		}
	case RequestToolsCall:
		intent := input.Intents[0]
		if intent.Validate() != nil || intent.Call.EffectiveKind() != protocolcore.ToolKindFunction || intent.Call.Namespace != "" {
			return nil, ErrInvalid
		}
		var name string
		if json.Unmarshal(fields["name"], &name) != nil || name != intent.Call.Name {
			return nil, ErrInvalid
		}
		actual, err := canonicalObject(fields["arguments"])
		if err != nil {
			return nil, err
		}
		wanted, err := canonicalObject(intent.Call.Arguments.Bytes())
		if err != nil || !bytes.Equal(actual, wanted) {
			return nil, ErrInvalid
		}
	}
	if len(params) == 0 {
		return nil, nil
	}
	return canonicalObject(params)
}
func validRPCID(raw []byte) bool {
	if len(raw) == 0 || len(raw) > 6*MaxObservedIDBytes+2 || !utf8.Valid(raw) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return false
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return false
	}
	switch v := value.(type) {
	case string:
		return len(v) > 0 && len(v) <= MaxObservedIDBytes && utf8.ValidString(v)
	case json.Number:
		_, err := v.Int64()
		return err == nil
	}
	return false
}
func canonicalObject(raw []byte) ([]byte, error) {
	if _, err := protocolcore.NewJSONObject(raw, MaxRPCBytes); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

type intentRecord struct {
	Response                                            string
	Ordinal                                             int
	Kind                                                protocolcore.ToolKind
	Source, WireID, ItemSource, ItemID, Namespace, Name string
	Arguments                                           json.RawMessage
	Input                                               string
}

func intentEvidence(intents []protocolcore.ToolIntent) ([]byte, []string, error) {
	records := make([]intentRecord, len(intents))
	refs := make([]string, len(intents))
	seen := map[string]bool{}
	for n, intent := range intents {
		if intent.Validate() != nil {
			return nil, nil, ErrInvalid
		}
		call := intent.Call
		key := call.Key.Source() + "\x00" + call.Key.WireID()
		if seen[key] {
			return nil, nil, ErrInvalid
		}
		seen[key] = true
		var args []byte
		var err error
		if !call.Arguments.IsZero() {
			args, err = canonicalObject(call.Arguments.Bytes())
			if err != nil {
				return nil, nil, ErrInvalid
			}
		}
		records[n] = intentRecord{intent.ResponseID, intent.Ordinal, call.EffectiveKind(), call.Key.Source(), call.Key.WireID(), call.ItemKey.Source(), call.ItemKey.WireID(), call.Namespace, call.Name, args, call.Input}
		refs[n] = fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	}
	encoded, err := json.Marshal(records)
	return encoded, refs, err
}
func (binding Binding) Digest() Digest                     { return binding.bindingDigest }
func (binding Binding) Capture() captureidentity.Reference { return binding.capture }
func (binding Binding) Plan() environment.ClientToolPlan   { return binding.plan }
func (binding Binding) Kind() RequestKind                  { return binding.kind }
func (binding Binding) AssignmentRevision() uint64         { return binding.assignmentRevision }
func (binding Binding) OwnerEpoch() uint64                 { return binding.ownerEpoch }
func (binding Binding) SessionID() ID                      { return binding.sessionID }
func (binding Binding) SessionRevision() uint64            { return binding.sessionRevision }
func (binding Binding) CatalogGeneration() uint64          { return binding.catalogGeneration }
func (binding Binding) CatalogDigest() (Digest, bool) {
	if binding.catalogDigest == nil {
		return Digest{}, false
	}
	return *binding.catalogDigest, true
}
func (binding Binding) ParentOperationID() (ID, bool) {
	if binding.parentOperationID == nil {
		return "", false
	}
	return *binding.parentOperationID, true
}
func (binding Binding) ServerRequestDigest() (Digest, bool) {
	if binding.serverRequestDigest == nil {
		return Digest{}, false
	}
	return *binding.serverRequestDigest, true
}
func (binding Binding) RequestDigest() Digest { return binding.requestDigest }
func (binding Binding) ParamsDigest() Digest  { return binding.paramsDigest }
func (binding Binding) GroupDigest() Digest   { return binding.groupDigest }
func (binding Binding) ReplayKey() (Digest, bool) {
	if binding.replayKey == nil {
		return Digest{}, false
	}
	return *binding.replayKey, true
}
func (binding Binding) SafeCallRefs() []string { return slices.Clone(binding.safeCallRefs) }
func (binding Binding) ToolName() string       { return binding.toolName }
func (binding Binding) RPCIDDigest() (Digest, bool) {
	if binding.rpcIDDigest == nil {
		return Digest{}, false
	}
	return *binding.rpcIDDigest, true
}
func (binding Binding) MatchesIntents(intents []protocolcore.ToolIntent) bool {
	if binding.sensitiveTool {
		return false
	}
	// Reply group digests may be scoped MACs. Cardinality alone is checked
	// here; the decision union separately requires the registered witness.
	if binding.kind == RequestServerReply {
		return binding.bindingDigest != (Digest{}) && len(intents) == 0
	}
	if binding.bindingDigest == (Digest{}) || len(intents) > MaxIntentGroup {
		return false
	}
	encoded, _, err := intentEvidence(intents)
	return err == nil && Digest(sha256.Sum256(encoded)) == binding.groupDigest
}
func (binding Binding) CancelledRPCIDDigest() (Digest, bool) {
	if binding.cancelledRPCIDDigest == nil {
		return Digest{}, false
	}
	return *binding.cancelledRPCIDDigest, true
}
func rpcIdentityDigest(raw []byte) Digest {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	_ = decoder.Decode(&value)
	return hashJSON(struct {
		Domain string
		ID     any
	}{"native-rpc-id/v1", value})
}
