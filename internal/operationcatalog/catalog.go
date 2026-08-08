// Package operationcatalog owns the explicit, versioned client API operation
// definitions used by both Environment compilation and ingress path
// classification. It owns no codec implementation, transport, or registry.
package operationcatalog

import (
	"errors"
	"net/http"
	"slices"

	"github.com/vibe-agi/vibermate/internal/protocolspec"
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

	// Observed non-model operations on the Anthropic model origin. Real Claude
	// Code 2.1.220 reaches these before and around a Messages request; while
	// they stay uncatalogued they classify as unknown, which is the class the
	// ingress gate can reason about least.
	AnthropicClaudeCodeSettingsID     = "anthropic-claude-code-settings"
	AnthropicClaudeCodePolicyLimitsID = "anthropic-claude-code-policy-limits"
	AnthropicHelloProbeID             = "anthropic-hello-probe"

	// Observed operations in the Codex ChatGPT-login shape. Classifying by
	// host alone would send this control plane into the model pipeline.
	OpenAICodexResponsesCreateID = "openai-codex-responses-create"
	OpenAICodexModelsProbeID     = "openai-codex-models-probe"
	OpenAIPluginsFeaturedProbeID = "openai-plugins-featured-probe"
	OpenAIPluginsInstalledID     = "openai-ps-plugins-installed-probe"
	OpenAIPluginsListID          = "openai-ps-plugins-list-probe"
	OpenAIPluginsSuggestedID     = "openai-ps-plugins-suggested-probe"

	MaxJSONBodyBytes   = 16 << 20
	MaxOpaqueBodyBytes = 16 << 20
)

type Catalog struct {
	definitions []protocolspec.ClientOperationDefinition
}

func BuiltIn() (Catalog, error) {
	var definitions []protocolspec.ClientOperationDefinition
	add := func(options protocolspec.ClientOperationOptions) error {
		definition, err := protocolspec.NewClientOperationDefinition(options)
		if err != nil {
			return err
		}
		definitions = append(definitions, definition)
		return nil
	}
	semantic := func(
		id string,
		dialect protocolspec.Dialect,
		path string,
		feature protocolspec.CodecFeature,
		queries []string,
	) error {
		identifier, err := protocolspec.NewClientOperationID(id)
		if err != nil {
			return err
		}
		return add(protocolspec.ClientOperationOptions{
			ID:             identifier,
			Revision:       1,
			ClientDialect:  dialect,
			Methods:        []string{http.MethodPost},
			PathPattern:    path,
			PathMatch:      protocolspec.ClientOperationPathExact,
			Kind:           protocolspec.ClientOperationSemantic,
			Transport:      protocolspec.ClientOperationTransportHTTP,
			BodyKind:       protocolspec.ClientOperationBodyJSON,
			ReplayClass:    protocolspec.ClientReplayGenerationCostOnly,
			CodecFeature:   feature,
			MaxBodyBytes:   MaxJSONBodyBytes,
			AllowedQueries: queries,
			PayloadClass:   protocolspec.OperationPayloadClientSemantic,
			EgressBearing:  true,
		})
	}
	if err := semantic(
		AnthropicMessagesCreateID,
		protocolspec.DialectAnthropicMessages,
		"/v1/messages",
		"messages",
		[]string{"beta=true"},
	); err != nil {
		return Catalog{}, err
	}
	if err := addOperation(
		add,
		AnthropicMessagesCountTokensID,
		protocolspec.DialectAnthropicMessages,
		[]string{http.MethodPost},
		"/v1/messages/count_tokens",
		protocolspec.ClientOperationPathExact,
		protocolspec.ClientOperationAuxiliary,
		protocolspec.ClientOperationBodyJSON,
		protocolspec.ClientReplaySafe,
		"token_count",
		MaxJSONBodyBytes,
		[]string{"beta=true"},
		// The request body is the complete messages, system text, and tool
		// schema, so it can never be forwarded to the original origin with the
		// client's own credentials.
		protocolspec.OperationPayloadClientSemantic,
		true,
	); err != nil {
		return Catalog{}, err
	}
	if err := semantic(
		OpenAIResponsesCreateID,
		protocolspec.DialectOpenAIResponses,
		"/v1/responses",
		"responses",
		nil,
	); err != nil {
		return Catalog{}, err
	}
	if err := semantic(
		OpenAICodexResponsesCreateID,
		protocolspec.DialectOpenAIResponses,
		"/backend-api/codex/responses",
		"responses",
		nil,
	); err != nil {
		return Catalog{}, err
	}
	// Bodyless probes. The observed MCP and analytics POSTs are deliberately
	// absent: they carry a request body and nothing verifies it holds no
	// prompt or tool data, so declaring them control would assert exactly
	// that. They stay unclassified and fail closed.
	for _, probe := range []struct {
		id   string
		path string
	}{
		{OpenAICodexModelsProbeID, "/backend-api/codex/models"},
		{OpenAIPluginsFeaturedProbeID, "/backend-api/plugins/featured"},
		{OpenAIPluginsInstalledID, "/backend-api/ps/plugins/installed"},
		{OpenAIPluginsListID, "/backend-api/ps/plugins/list"},
		{OpenAIPluginsSuggestedID, "/backend-api/ps/plugins/suggested"},
	} {
		if err := addOperation(
			add,
			probe.id,
			protocolspec.DialectOpenAIResponses,
			[]string{http.MethodGet},
			probe.path,
			protocolspec.ClientOperationPathExact,
			protocolspec.ClientOperationOpaque,
			protocolspec.ClientOperationBodyNone,
			protocolspec.ClientReplaySafe,
			"",
			0,
			nil,
			protocolspec.OperationPayloadNone,
			true,
		); err != nil {
			return Catalog{}, err
		}
	}
	webSocketID, err := protocolspec.NewClientOperationID(
		OpenAIResponsesWebSocketUnsupportedID,
	)
	if err != nil {
		return Catalog{}, err
	}
	if err := add(protocolspec.ClientOperationOptions{
		ID:            webSocketID,
		Revision:      1,
		ClientDialect: protocolspec.DialectOpenAIResponses,
		Methods:       []string{http.MethodGet},
		PathPattern:   "/v1/responses",
		PathMatch:     protocolspec.ClientOperationPathExact,
		Kind:          protocolspec.ClientOperationUnsupported,
		Transport:     protocolspec.ClientOperationTransportWebSocket,
		BodyKind:      protocolspec.ClientOperationBodyNone,
		ReplayClass:   protocolspec.ClientReplayNonReplayable,
		PayloadClass:  protocolspec.OperationPayloadNone,
	}); err != nil {
		return Catalog{}, err
	}

	// These carry no request body at all, so they are proven no-payload probes
	// that may keep the client's own credentials on the way back to the inbound
	// origin. Cataloguing a path does not open it to other methods.
	for _, probe := range []struct {
		id      string
		path    string
		methods []string
	}{
		{
			id:      AnthropicClaudeCodeSettingsID,
			path:    "/api/claude_code/settings",
			methods: []string{http.MethodGet},
		},
		{
			id:      AnthropicClaudeCodePolicyLimitsID,
			path:    "/api/claude_code/policy_limits",
			methods: []string{http.MethodGet},
		},
		{
			id:      AnthropicHelloProbeID,
			path:    "/api/hello",
			methods: []string{http.MethodGet, http.MethodHead},
		},
	} {
		if err := addOperation(
			add,
			probe.id,
			protocolspec.DialectAnthropicMessages,
			probe.methods,
			probe.path,
			protocolspec.ClientOperationPathExact,
			protocolspec.ClientOperationOpaque,
			protocolspec.ClientOperationBodyNone,
			protocolspec.ClientReplaySafe,
			"",
			0,
			nil,
			protocolspec.OperationPayloadNone,
			true,
		); err != nil {
			return Catalog{}, err
		}
	}

	// Response retrieval, input-item listing, cancellation, and deletion are
	// stateful management operations; the fixed HTTP slice supports only
	// create. A prefix groups several methods, so it declares the highest class
	// any of them can carry.
	if err := addUnsupportedPrefix(
		add,
		OpenAIResponsesManagementID,
		"/v1/responses",
		protocolspec.OperationPayloadClientData,
	); err != nil {
		return Catalog{}, err
	}
	for _, unsupported := range []struct {
		id           string
		path         string
		payloadClass protocolspec.OperationPayloadClass
	}{
		{OpenAIFilesUnsupportedID, "/v1/files", protocolspec.OperationPayloadClientData},
		{OpenAIUploadsUnsupportedID, "/v1/uploads", protocolspec.OperationPayloadClientData},
		{OpenAIBatchesUnsupportedID, "/v1/batches", protocolspec.OperationPayloadClientData},
		{OpenAIAudioUnsupportedID, "/v1/audio", protocolspec.OperationPayloadClientData},
		{OpenAIImagesUnsupportedID, "/v1/images", protocolspec.OperationPayloadClientData},
		{OpenAIVideosUnsupportedID, "/v1/videos", protocolspec.OperationPayloadClientData},
		{OpenAIRealtimeUnsupportedID, "/v1/realtime", protocolspec.OperationPayloadClientData},
	} {
		if err := addUnsupportedPrefix(
			add,
			unsupported.id,
			unsupported.path,
			unsupported.payloadClass,
		); err != nil {
			return Catalog{}, err
		}
	}
	for _, unsupported := range []struct {
		id           string
		path         string
		payloadClass protocolspec.OperationPayloadClass
	}{
		{OpenAIChatUnsupportedID, "/v1/chat/completions", protocolspec.OperationPayloadClientSemantic},
		{OpenAICompletionsUnsupportedID, "/v1/completions", protocolspec.OperationPayloadClientSemantic},
		{OpenAIEmbeddingsUnsupportedID, "/v1/embeddings", protocolspec.OperationPayloadClientSemantic},
	} {
		if err := addUnsupportedExact(
			add,
			unsupported.id,
			unsupported.path,
			unsupported.payloadClass,
		); err != nil {
			return Catalog{}, err
		}
	}
	return newCatalog(definitions)
}

func addOperation(
	add func(protocolspec.ClientOperationOptions) error,
	id string,
	dialect protocolspec.Dialect,
	methods []string,
	path string,
	match protocolspec.ClientOperationPathMatch,
	kind protocolspec.ClientOperationKind,
	bodyKind protocolspec.ClientOperationBodyKind,
	replay protocolspec.ClientReplayClass,
	feature protocolspec.CodecFeature,
	maxBodyBytes int64,
	queries []string,
	payloadClass protocolspec.OperationPayloadClass,
	egressBearing bool,
) error {
	identifier, err := protocolspec.NewClientOperationID(id)
	if err != nil {
		return err
	}
	return add(protocolspec.ClientOperationOptions{
		ID:             identifier,
		Revision:       1,
		ClientDialect:  dialect,
		Methods:        methods,
		PathPattern:    path,
		PathMatch:      match,
		Kind:           kind,
		Transport:      protocolspec.ClientOperationTransportHTTP,
		BodyKind:       bodyKind,
		ReplayClass:    replay,
		CodecFeature:   feature,
		MaxBodyBytes:   maxBodyBytes,
		AllowedQueries: queries,
		PayloadClass:   payloadClass,
		EgressBearing:  egressBearing,
	})
}

func addUnsupportedPrefix(
	add func(protocolspec.ClientOperationOptions) error,
	id string,
	path string,
	payloadClass protocolspec.OperationPayloadClass,
) error {
	return addOperation(
		add,
		id,
		protocolspec.DialectOpenAIResponses,
		[]string{
			http.MethodDelete,
			http.MethodGet,
			http.MethodPatch,
			http.MethodPost,
			http.MethodPut,
		},
		path,
		protocolspec.ClientOperationPathPrefix,
		protocolspec.ClientOperationUnsupported,
		protocolspec.ClientOperationBodyBytes,
		protocolspec.ClientReplayNonReplayable,
		"",
		MaxJSONBodyBytes,
		nil,
		payloadClass,
		false,
	)
}

func addUnsupportedExact(
	add func(protocolspec.ClientOperationOptions) error,
	id string,
	path string,
	payloadClass protocolspec.OperationPayloadClass,
) error {
	return addOperation(
		add,
		id,
		protocolspec.DialectOpenAIResponses,
		[]string{http.MethodPost},
		path,
		protocolspec.ClientOperationPathExact,
		protocolspec.ClientOperationUnsupported,
		protocolspec.ClientOperationBodyJSON,
		protocolspec.ClientReplayNonReplayable,
		"",
		MaxJSONBodyBytes,
		nil,
		payloadClass,
		false,
	)
}

func newCatalog(
	definitions []protocolspec.ClientOperationDefinition,
) (Catalog, error) {
	if len(definitions) == 0 {
		return Catalog{}, errors.New("operation catalog is empty")
	}
	seen := make(map[protocolspec.ClientOperationID]struct{}, len(definitions))
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

func (catalog Catalog) Definitions() []protocolspec.ClientOperationDefinition {
	return slices.Clone(catalog.definitions)
}

func (catalog Catalog) SemanticOperationIDs(
	dialect protocolspec.Dialect,
) []protocolspec.ClientOperationID {
	var identifiers []protocolspec.ClientOperationID
	for _, definition := range catalog.definitions {
		if definition.ClientDialect() == dialect &&
			definition.Kind() == protocolspec.ClientOperationSemantic {
			identifiers = append(identifiers, definition.ID())
		}
	}
	return identifiers
}
