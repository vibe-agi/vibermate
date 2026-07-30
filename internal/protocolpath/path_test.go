package protocolpath

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
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

	identifier, err := access.NewCodecPairID("client-to-provider")
	if err != nil {
		t.Fatal(err)
	}
	path, err := New(Options{
		ID:        identifier,
		Revision:  1,
		Client:    clientCodecFixture{},
		Backend:   backendCodecFixture{},
		Streaming: streamingFixture{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path.Client().Dialect() != access.DialectAnthropicMessages ||
		path.Backend().Dialect() != access.DialectOpenAIChat {
		t.Fatal("path did not retain both typed wire edges")
	}
	if err := path.ValidatePlan(access.CodecPlan{}); err == nil {
		t.Fatal("path accepted an unmatched Access codec plan")
	}
	if _, err := New(Options{ID: identifier, Revision: 1}); err == nil {
		t.Fatal("path accepted missing codec edges")
	}
}

type clientCodecFixture struct{}

func (clientCodecFixture) Dialect() access.Dialect {
	return access.DialectAnthropicMessages
}

func (clientCodecFixture) DecodeRequest(
	[]byte,
) (protocolcore.Request, protocolcore.TranslationReport, error) {
	return protocolcore.Request{}, protocolcore.TranslationReport{}, nil
}

func (clientCodecFixture) EncodeResponse(protocolcore.Response) ([]byte, error) {
	return nil, nil
}

type backendCodecFixture struct{}

func (backendCodecFixture) Dialect() access.Dialect {
	return access.DialectOpenAIChat
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
