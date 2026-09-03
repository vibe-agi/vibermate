package protocolpath

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

func TestProviderRequestOwnsWireValues(t *testing.T) {
	t.Parallel()

	headers := http.Header{"Accept": []string{"application/json"}}
	body := []byte(`{"model":"provider"}`)
	request, err := NewProviderRequest(
		http.MethodPost,
		"chat/completions",
		headers,
		body,
	)
	if err != nil {
		t.Fatal(err)
	}
	headers.Set("Accept", "mutated")
	body[0] = '!'
	firstHeaders := request.Headers()
	firstBody := request.Body()
	firstHeaders.Set("Accept", "mutated-again")
	firstBody[0] = '?'
	if request.Method() != http.MethodPost ||
		request.RelativePath() != "chat/completions" ||
		request.Headers().Get("Accept") != "application/json" ||
		!bytes.Equal(request.Body(), []byte(`{"model":"provider"}`)) {
		t.Fatalf("provider request changed through an alias: %+v", request)
	}
}

func TestPathRequiresTwoTypedEdgesAndRejectsAnUnmatchedPlan(t *testing.T) {
	t.Parallel()

	identifier, err := protocolspec.NewCodecPairID("client-to-provider")
	if err != nil {
		t.Fatal(err)
	}
	operationID, err := protocolspec.NewClientOperationID("client-create")
	if err != nil {
		t.Fatal(err)
	}
	path, err := New(Options{
		ID:                 identifier,
		Revision:           1,
		ClientOperationIDs: []protocolspec.ClientOperationID{operationID},
		Client:             clientCodecFixture{},
		Backend:            backendCodecFixture{},
		Streaming:          streamingFixture{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path.Client().Dialect() != protocolspec.DialectAnthropicMessages ||
		path.Backend().Dialect() != protocolspec.DialectOpenAIChat {
		t.Fatal("path did not retain both typed wire edges")
	}
	if err := path.ValidatePlan(protocolspec.CodecPlan{}); err == nil {
		t.Fatal("path accepted an unmatched Access codec plan")
	}
	operations := path.ClientOperationIDs()
	operations[0] = protocolspec.ClientOperationID{}
	if !path.SupportsClientOperation(operationID) ||
		path.SupportsClientOperation(protocolspec.ClientOperationID{}) ||
		path.ClientOperationIDs()[0] != operationID {
		t.Fatal("path did not own its typed client operation identities")
	}
	selector, err := NewSelector(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := selector.Select(protocolspec.CodecPlan{}, operationID); err == nil {
		t.Fatal("selector accepted an unmatched Access codec plan")
	}
	if _, err := NewSelector(path, path); err == nil {
		t.Fatal("selector accepted a duplicate typed protocol path")
	}
	if _, err := New(Options{ID: identifier, Revision: 1}); err == nil {
		t.Fatal("path accepted missing codec edges")
	}
}

type clientCodecFixture struct{}

func (clientCodecFixture) Dialect() protocolspec.Dialect {
	return protocolspec.DialectAnthropicMessages
}

func (clientCodecFixture) DecodeRequest(
	[]byte,
) (protocolcore.Request, protocolcore.TranslationReport, error) {
	return protocolcore.Request{}, protocolcore.TranslationReport{}, nil
}

func (clientCodecFixture) EncodeResponse(
	protocolcore.Request,
	protocolcore.Response,
) ([]byte, protocolcore.TranslationReport, error) {
	return nil, protocolcore.TranslationReport{}, nil
}

type backendCodecFixture struct{}

func (backendCodecFixture) Dialect() protocolspec.Dialect {
	return protocolspec.DialectOpenAIChat
}

func (backendCodecFixture) EncodeRequest(
	protocolcore.Request,
) (ProviderRequest, protocolcore.TranslationReport, error) {
	return ProviderRequest{}, protocolcore.TranslationReport{}, nil
}

func (backendCodecFixture) DecodeResponse(
	protocolcore.Request,
	[]byte,
) (protocolcore.Response, protocolcore.TranslationReport, error) {
	return protocolcore.Response{}, protocolcore.TranslationReport{}, nil
}

type streamingFixture struct{}

func (streamingFixture) NewStream(protocolcore.Request) (Stream, error) {
	return streamFixture{}, nil
}

type streamFixture struct{}

func (streamFixture) Feed(context.Context, []byte) ([]byte, error) {
	return nil, nil
}

func (streamFixture) SemanticProgress() uint64 {
	return 0
}

func (streamFixture) FinishDecoded(
	context.Context,
) (PendingTerminal, error) {
	return nil, nil
}
