// Package operationcatalog owns the explicit, versioned client API operation
// definitions used by both Access compilation and ingress path
// classification. It owns no codec implementation, transport, or registry.
package operationcatalog

import (
	"errors"
	"net/http"
	"slices"

	"github.com/vibe-agi/vibermate/internal/access"
)

const (
	AnthropicMessagesCreateID             = "anthropic-messages-create"
	AnthropicMessagesCountTokensID        = "anthropic-messages-count-tokens"
	OpenAIResponsesCreateID               = "openai-responses-create"
	OpenAIResponsesWebSocketUnsupportedID = "openai-responses-websocket-unsupported"
	OpenAIResponsesManagementID           = "openai-responses-management"
	OpenAIFilesUnsupportedID              = "openai-files-unsupported"
	OpenAIUploadsUnsupportedID            = "openai-uploads-unsupported"
	OpenAIBatchesUnsupportedID            = "openai-batches-unsupported"
	OpenAIAudioUnsupportedID              = "openai-audio-unsupported"
	OpenAIImagesUnsupportedID             = "openai-images-unsupported"
	OpenAIVideosUnsupportedID             = "openai-videos-unsupported"
	OpenAIRealtimeUnsupportedID           = "openai-realtime-unsupported"
	OpenAIChatUnsupportedID               = "openai-chat-unsupported"
	OpenAICompletionsUnsupportedID        = "openai-completions-unsupported"
	OpenAIEmbeddingsUnsupportedID         = "openai-embeddings-unsupported"

	MaxJSONBodyBytes   = 16 << 20
	MaxOpaqueBodyBytes = 16 << 20
)

type Catalog struct {
	definitions []access.ClientOperationDefinition
}

func M0() (Catalog, error) {
	var definitions []access.ClientOperationDefinition
	add := func(options access.ClientOperationOptions) error {
		definition, err := access.NewClientOperationDefinition(options)
		if err != nil {
			return err
		}
		definitions = append(definitions, definition)
		return nil
	}
	semantic := func(
		id string,
		dialect access.Dialect,
		path string,
		feature access.CodecFeature,
		queries []string,
	) error {
		identifier, err := access.NewClientOperationID(id)
		if err != nil {
			return err
		}
		return add(access.ClientOperationOptions{
			ID:             identifier,
			Revision:       1,
			ClientDialect:  dialect,
			Methods:        []string{http.MethodPost},
			PathPattern:    path,
			PathMatch:      access.ClientOperationPathExact,
			Kind:           access.ClientOperationSemantic,
			Transport:      access.ClientOperationTransportHTTP,
			BodyKind:       access.ClientOperationBodyJSON,
			ReplayClass:    access.ClientReplayGenerationCostOnly,
			CodecFeature:   feature,
			MaxBodyBytes:   MaxJSONBodyBytes,
			AllowedQueries: queries,
			EgressBearing:  true,
		})
	}
	if err := semantic(
		AnthropicMessagesCreateID,
		access.DialectAnthropicMessages,
		"/v1/messages",
		"messages",
		[]string{"beta=true"},
	); err != nil {
		return Catalog{}, err
	}
	if err := addOperation(
		add,
		AnthropicMessagesCountTokensID,
		access.DialectAnthropicMessages,
		[]string{http.MethodPost},
		"/v1/messages/count_tokens",
		access.ClientOperationPathExact,
		access.ClientOperationAuxiliary,
		access.ClientOperationBodyJSON,
		access.ClientReplaySafe,
		"token_count",
		MaxJSONBodyBytes,
		[]string{"beta=true"},
		true,
	); err != nil {
		return Catalog{}, err
	}
	if err := semantic(
		OpenAIResponsesCreateID,
		access.DialectOpenAIResponses,
		"/v1/responses",
		"responses",
		nil,
	); err != nil {
		return Catalog{}, err
	}
	webSocketID, err := access.NewClientOperationID(
		OpenAIResponsesWebSocketUnsupportedID,
	)
	if err != nil {
		return Catalog{}, err
	}
	if err := add(access.ClientOperationOptions{
		ID:            webSocketID,
		Revision:      1,
		ClientDialect: access.DialectOpenAIResponses,
		Methods:       []string{http.MethodGet},
		PathPattern:   "/v1/responses",
		PathMatch:     access.ClientOperationPathExact,
		Kind:          access.ClientOperationUnsupported,
		Transport:     access.ClientOperationTransportWebSocket,
		BodyKind:      access.ClientOperationBodyNone,
		ReplayClass:   access.ClientReplayNonReplayable,
	}); err != nil {
		return Catalog{}, err
	}

	// Response retrieval, input-item listing, cancellation, and deletion are
	// stateful management operations. The fixed HTTP slice only supports create.
	if err := addUnsupportedPrefix(
		add,
		OpenAIResponsesManagementID,
		"/v1/responses",
	); err != nil {
		return Catalog{}, err
	}
	for _, unsupported := range []struct {
		id   string
		path string
	}{
		{OpenAIFilesUnsupportedID, "/v1/files"},
		{OpenAIUploadsUnsupportedID, "/v1/uploads"},
		{OpenAIBatchesUnsupportedID, "/v1/batches"},
		{OpenAIAudioUnsupportedID, "/v1/audio"},
		{OpenAIImagesUnsupportedID, "/v1/images"},
		{OpenAIVideosUnsupportedID, "/v1/videos"},
		{OpenAIRealtimeUnsupportedID, "/v1/realtime"},
	} {
		if err := addUnsupportedPrefix(
			add,
			unsupported.id,
			unsupported.path,
		); err != nil {
			return Catalog{}, err
		}
	}
	for _, unsupported := range []struct {
		id   string
		path string
	}{
		{OpenAIChatUnsupportedID, "/v1/chat/completions"},
		{OpenAICompletionsUnsupportedID, "/v1/completions"},
		{OpenAIEmbeddingsUnsupportedID, "/v1/embeddings"},
	} {
		if err := addUnsupportedExact(
			add,
			unsupported.id,
			unsupported.path,
		); err != nil {
			return Catalog{}, err
		}
	}
	return newCatalog(definitions)
}

func addOperation(
	add func(access.ClientOperationOptions) error,
	id string,
	dialect access.Dialect,
	methods []string,
	path string,
	match access.ClientOperationPathMatch,
	kind access.ClientOperationKind,
	bodyKind access.ClientOperationBodyKind,
	replay access.ClientReplayClass,
	feature access.CodecFeature,
	maxBodyBytes int64,
	queries []string,
	egressBearing bool,
) error {
	identifier, err := access.NewClientOperationID(id)
	if err != nil {
		return err
	}
	return add(access.ClientOperationOptions{
		ID:             identifier,
		Revision:       1,
		ClientDialect:  dialect,
		Methods:        methods,
		PathPattern:    path,
		PathMatch:      match,
		Kind:           kind,
		Transport:      access.ClientOperationTransportHTTP,
		BodyKind:       bodyKind,
		ReplayClass:    replay,
		CodecFeature:   feature,
		MaxBodyBytes:   maxBodyBytes,
		AllowedQueries: queries,
		EgressBearing:  egressBearing,
	})
}

func addUnsupportedPrefix(
	add func(access.ClientOperationOptions) error,
	id string,
	path string,
) error {
	return addOperation(
		add,
		id,
		access.DialectOpenAIResponses,
		[]string{
			http.MethodDelete,
			http.MethodGet,
			http.MethodPatch,
			http.MethodPost,
			http.MethodPut,
		},
		path,
		access.ClientOperationPathPrefix,
		access.ClientOperationUnsupported,
		access.ClientOperationBodyBytes,
		access.ClientReplayNonReplayable,
		"",
		MaxJSONBodyBytes,
		nil,
		false,
	)
}

func addUnsupportedExact(
	add func(access.ClientOperationOptions) error,
	id string,
	path string,
) error {
	return addOperation(
		add,
		id,
		access.DialectOpenAIResponses,
		[]string{http.MethodPost},
		path,
		access.ClientOperationPathExact,
		access.ClientOperationUnsupported,
		access.ClientOperationBodyJSON,
		access.ClientReplayNonReplayable,
		"",
		MaxJSONBodyBytes,
		nil,
		false,
	)
}

func newCatalog(
	definitions []access.ClientOperationDefinition,
) (Catalog, error) {
	if len(definitions) == 0 {
		return Catalog{}, errors.New("operation catalog is empty")
	}
	seen := make(map[access.ClientOperationID]struct{}, len(definitions))
	for _, definition := range definitions {
		if err := definition.Validate(); err != nil {
			return Catalog{}, err
		}
		if _, duplicate := seen[definition.ID()]; duplicate {
			return Catalog{}, errors.New("operation catalog ID is duplicated")
		}
		seen[definition.ID()] = struct{}{}
	}
	return Catalog{definitions: slices.Clone(definitions)}, nil
}

func (catalog Catalog) Definitions() []access.ClientOperationDefinition {
	return slices.Clone(catalog.definitions)
}

func (catalog Catalog) SemanticOperationIDs(
	dialect access.Dialect,
) []access.ClientOperationID {
	var identifiers []access.ClientOperationID
	for _, definition := range catalog.definitions {
		if definition.ClientDialect() == dialect &&
			definition.Kind() == access.ClientOperationSemantic {
			identifiers = append(identifiers, definition.ID())
		}
	}
	return identifiers
}
