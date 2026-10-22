package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/vibe-agi/vibermate/internal/cyberaccess"
	"github.com/vibe-agi/vibermate/internal/operationcatalog"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

type accountCyberCatalog interface {
	AccountCyberPrograms(context.Context, providertransport.Request) (cyberaccess.Catalog, error)
}

// Failure to obtain trustworthy evidence is distinct from a directory that
// successfully confirms the final model cannot use the selected preference.
type cyberDiscoveryFailure struct{ cause error }

func (*cyberDiscoveryFailure) Error() string {
	return "account model capability discovery is unavailable"
}
func (failure *cyberDiscoveryFailure) Unwrap() error { return failure.cause }

func newProviderRequestFailure(exchangeID string, cause error) *Failure {
	reason := ReasonProviderRequestInvalid
	var discovery *cyberDiscoveryFailure
	if errors.As(cause, &discovery) {
		reason = ReasonCyberDiscoveryUnavailable
	} else if errors.Is(cause, cyberaccess.ErrProgramUnavailable) {
		reason = ReasonCyberAccessProgramUnavailable
	}
	return newFailure(reason, exchangeID, 0, cause)
}

// finalizeAccountCyberMode runs after model mapping and request transforms, but
// before the final provider representation is published. The validated draft
// supplies the exact frozen credential/egress scope for an optional model read.
func (pipeline *Pipeline) finalizeAccountCyberMode(ctx context.Context, request ClientRequest, options providertransport.RequestOptions) (providertransport.Request, error) {
	draft, err := providertransport.NewRequest(options)
	if err != nil {
		return providertransport.Request{}, err
	}
	account, managed := draft.AccountRef()
	if !managed || !upstreamendpoint.SupportsCodexCatalog(draft.Target().Origin(), draft.AuthDriverRef()) ||
		draft.RelativePath() != upstreamendpoint.ProviderRelativePath(draft.Target().Origin(), "v1/responses") {
		return draft, nil
	}
	body, err := decodeBoundedContent(options.Body, strings.Join(options.Headers.Values("Content-Encoding"), ","))
	if err != nil {
		return providertransport.Request{}, err
	}
	guardian, err := nativeGuardianRequest(request, options.Headers, body)
	if err != nil {
		return providertransport.Request{}, err
	}
	mode := account.CyberAccessProgram
	if guardian && mode == cyberaccess.Red {
		mode = cyberaccess.Blue
	}
	var programs cyberaccess.Catalog
	if mode != cyberaccess.Standard {
		reader, ok := pipeline.provider.(accountCyberCatalog)
		if !ok {
			return providertransport.Request{}, &cyberDiscoveryFailure{cause: errors.New("account capability reader is unavailable")}
		}
		programs, err = reader.AccountCyberPrograms(ctx, draft)
		if err != nil {
			if ctx.Err() != nil {
				return providertransport.Request{}, ctx.Err()
			}
			return providertransport.Request{}, &cyberDiscoveryFailure{cause: err}
		}
	}
	updated, changed, err := cyberaccess.ApplyPreference(body, programs, mode)
	if err != nil {
		return providertransport.Request{}, err
	}
	if !changed {
		return draft, nil
	}
	options.Body = updated
	options.Headers = options.Headers.Clone()
	options.Headers.Del("Content-Encoding")
	options.Headers.Del("Content-Length")
	return providertransport.NewRequest(options)
}

// Codex rust-v0.163.0 derives Blue from either Daybreak parent mode for Guardian
// (ext/guardian-reviewer/src/settings.rs). The role identifies that derivation;
// it never grants a program or exempts internal aliases from model capability
// checks. The frozen account remains unchanged for discovery and transport.
func nativeGuardianRequest(request ClientRequest, headers http.Header, body []byte) (bool, error) {
	if request.operation.id.String() != operationcatalog.OpenAICodexResponsesCreateID && request.operation.id.String() != operationcatalog.OpenAIResponsesCreateID {
		return false, nil
	}
	original, _ := request.OriginalHeaders()
	originalGuardian, originalOther, err := guardianHeaderSignals(original)
	if err != nil {
		return false, err
	}
	guardian, other, err := guardianHeaderSignals(headers)
	if err != nil {
		return false, err
	}
	var root struct {
		Metadata map[string]json.RawMessage `json:"client_metadata"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return false, errors.New("Responses Guardian metadata is invalid")
	}
	if raw, present := root.Metadata["x-openai-subagent"]; present {
		var subagent *string
		if err := json.Unmarshal(raw, &subagent); err != nil || subagent == nil {
			return false, errors.New("Responses Guardian subagent metadata is invalid")
		}
		guardian = guardian || *subagent == "guardian"
		other = other || *subagent != "guardian"
	}
	if (originalGuardian || guardian) && (originalOther || other) {
		return false, errors.New("Responses Guardian role signals disagree")
	}
	// Original headers validate ambiguity before the native whitelist drops a
	// malformed marker. Only final headers/metadata establish the outgoing role;
	// a removed or hop-by-hop marker must not be resurrected.
	return guardian, nil
}

func guardianHeaderSignals(headers http.Header) (guardian, other bool, err error) {
	if values := headerValuesFold(headers, "X-Codex-Guardian"); len(values) != 0 {
		if len(values) != 1 || (values[0] != "reviewer" && values[0] != "classifier") {
			return false, false, errors.New("Responses Guardian role header is invalid")
		}
		guardian = true
	}
	if values := headerValuesFold(headers, "X-Openai-Subagent"); len(values) != 0 {
		if len(values) != 1 {
			return false, false, errors.New("Responses subagent header is ambiguous")
		}
		guardian = guardian || values[0] == "guardian"
		other = values[0] != "guardian"
	}
	return guardian, other, nil
}
