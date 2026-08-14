package openairesponses

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

type requestWire struct {
	Model              string            `json:"model"`
	Instructions       *string           `json:"instructions,omitempty"`
	Input              []json.RawMessage `json:"input"`
	MaxOutputTokens    *int64            `json:"max_output_tokens,omitempty"`
	ToolChoice         json.RawMessage   `json:"tool_choice,omitempty"`
	ParallelToolCalls  *bool             `json:"parallel_tool_calls,omitempty"`
	Reasoning          json.RawMessage   `json:"reasoning,omitempty"`
	Background         *bool             `json:"background,omitempty"`
	Store              *bool             `json:"store,omitempty"`
	Stream             bool              `json:"stream,omitempty"`
	Include            []string          `json:"include,omitempty"`
	PromptCacheKey     string            `json:"prompt_cache_key,omitempty"`
	PreviousResponseID *string           `json:"previous_response_id,omitempty"`
	Text               json.RawMessage   `json:"text,omitempty"`
	ClientMetadata     json.RawMessage   `json:"client_metadata,omitempty"`
	Tools              []json.RawMessage `json:"tools,omitempty"`
}

type inputTypeWire struct {
	Type string `json:"type"`
}

type additionalToolsWire struct {
	Type  string            `json:"type"`
	Role  string            `json:"role"`
	Tools []json.RawMessage `json:"tools"`
	ID    string            `json:"id,omitempty"`
}

type inputMessageWire struct {
	Type             string          `json:"type"`
	ID               string          `json:"id,omitempty"`
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content"`
	Phase            string          `json:"phase,omitempty"`
	InternalMetadata json.RawMessage `json:"internal_chat_message_metadata_passthrough,omitempty"`
	Agent            *agentItemWire  `json:"agent,omitempty"`
}

type internalMessageMetadataWire struct {
	TurnID string `json:"turn_id"`
}

type inputContentWire struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Refusal string `json:"refusal,omitempty"`
}

type functionCallWire struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	CallID           string          `json:"call_id"`
	Name             string          `json:"name"`
	Namespace        string          `json:"namespace,omitempty"`
	Arguments        string          `json:"arguments"`
	Status           string          `json:"status,omitempty"`
	InternalMetadata json.RawMessage `json:"internal_chat_message_metadata_passthrough,omitempty"`
	Agent            *agentItemWire  `json:"agent,omitempty"`
}

type functionCallOutputWire struct {
	Type             string          `json:"type"`
	ID               string          `json:"id,omitempty"`
	CallID           string          `json:"call_id"`
	Output           json.RawMessage `json:"output"`
	Status           string          `json:"status,omitempty"`
	InternalMetadata json.RawMessage `json:"internal_chat_message_metadata_passthrough,omitempty"`
	Agent            *agentItemWire  `json:"agent,omitempty"`
}

type customToolCallWire struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	CallID           string          `json:"call_id"`
	Name             string          `json:"name"`
	Namespace        string          `json:"namespace,omitempty"`
	Input            string          `json:"input"`
	InternalMetadata json.RawMessage `json:"internal_chat_message_metadata_passthrough,omitempty"`
	Agent            *agentItemWire  `json:"agent,omitempty"`
}

type customToolCallOutputWire struct {
	Type             string          `json:"type"`
	ID               string          `json:"id,omitempty"`
	CallID           string          `json:"call_id"`
	Output           json.RawMessage `json:"output"`
	InternalMetadata json.RawMessage `json:"internal_chat_message_metadata_passthrough,omitempty"`
	Agent            *agentItemWire  `json:"agent,omitempty"`
}

type responseReasoningItemWire struct {
	ID               string            `json:"id"`
	Type             string            `json:"type"`
	Summary          []json.RawMessage `json:"summary"`
	Content          []json.RawMessage `json:"content,omitempty"`
	EncryptedContent json.RawMessage   `json:"encrypted_content"`
	Status           string            `json:"status,omitempty"`
	Agent            *agentItemWire    `json:"agent,omitempty"`
}

type responseReasoningTextWire struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type agentItemWire struct {
	AgentName string `json:"agent_name"`
}

type agentMessageWire struct {
	Type      string            `json:"type"`
	ID        string            `json:"id,omitempty"`
	Author    string            `json:"author"`
	Recipient string            `json:"recipient"`
	Content   []json.RawMessage `json:"content"`
	Agent     *agentItemWire    `json:"agent,omitempty"`
}

type agentMessageContentWire struct {
	Type             string `json:"type"`
	Text             string `json:"text,omitempty"`
	Refusal          string `json:"refusal,omitempty"`
	EncryptedContent string `json:"encrypted_content,omitempty"`
}

type multiAgentCallWire struct {
	Type      string         `json:"type"`
	ID        string         `json:"id,omitempty"`
	Action    string         `json:"action"`
	Arguments string         `json:"arguments"`
	CallID    string         `json:"call_id"`
	Agent     *agentItemWire `json:"agent,omitempty"`
}

type multiAgentCallOutputWire struct {
	Type   string            `json:"type"`
	ID     string            `json:"id,omitempty"`
	Action string            `json:"action"`
	CallID string            `json:"call_id"`
	Output []json.RawMessage `json:"output"`
	Agent  *agentItemWire    `json:"agent,omitempty"`
}

type multiAgentOutputTextWire struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolOutputContentWire struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolTypeWire struct {
	Type string `json:"type"`
}

type functionToolWire struct {
	Type           string          `json:"type"`
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	Parameters     json.RawMessage `json:"parameters"`
	Strict         *bool           `json:"strict,omitempty"`
	OutputSchema   json.RawMessage `json:"output_schema,omitempty"`
	DeferLoading   *bool           `json:"defer_loading,omitempty"`
	AllowedCallers []string        `json:"allowed_callers,omitempty"`
}

type customToolWire struct {
	Type           string          `json:"type"`
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	Format         json.RawMessage `json:"format,omitempty"`
	DeferLoading   *bool           `json:"defer_loading,omitempty"`
	AllowedCallers []string        `json:"allowed_callers,omitempty"`
}

type customFormatWire struct {
	Type       string `json:"type"`
	Syntax     string `json:"syntax,omitempty"`
	Definition string `json:"definition,omitempty"`
}

type namespaceToolWire struct {
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Tools       []json.RawMessage `json:"tools"`
}

type webSearchToolWire struct {
	Type              string `json:"type"`
	ExternalWebAccess *bool  `json:"external_web_access,omitempty"`
}

type reasoningWire struct {
	Context         string `json:"context,omitempty"`
	Effort          string `json:"effort,omitempty"`
	Summary         string `json:"summary,omitempty"`
	Mode            string `json:"mode,omitempty"`
	GenerateSummary string `json:"generate_summary,omitempty"`
}

type textWire struct {
	Verbosity string          `json:"verbosity,omitempty"`
	Format    json.RawMessage `json:"format,omitempty"`
}

func (codec *Codec) DecodeClientRequest(
	body []byte,
) (protocolcore.Request, protocolcore.TranslationReport, error) {
	return codec.decodeClientRequest(body, true)
}

// DecodeCompatibleClientRequest validates the fields needed for the neutral
// inspection view while allowing additional fields on a same-dialect path
// whose original Responses wire remains authoritative.
func (codec *Codec) DecodeCompatibleClientRequest(
	body []byte,
) (protocolcore.Request, protocolcore.TranslationReport, error) {
	return codec.decodeClientRequest(body, false)
}

func (codec *Codec) decodeClientRequest(
	body []byte,
	strictRoot bool,
) (protocolcore.Request, protocolcore.TranslationReport, error) {
	if codec == nil ||
		len(body) == 0 ||
		len(body) > codec.options.MaxRequestBytes {
		return protocolcore.Request{}, protocolcore.TranslationReport{},
			invalidClient("$", errors.New("request body has an invalid size"))
	}

	var wire requestWire
	var decodeErr error
	if strictRoot {
		decodeErr = decodeStrict(body, &wire)
	} else {
		decodeErr = rejectDuplicateNames(body)
		if decodeErr == nil {
			decodeErr = json.Unmarshal(body, &wire)
		}
	}
	if decodeErr != nil {
		return protocolcore.Request{}, protocolcore.TranslationReport{},
			invalidClient("$", decodeErr)
	}
	if wire.Store != nil && *wire.Store {
		return protocolcore.Request{}, protocolcore.TranslationReport{},
			invalidClient(
				"$.store",
				errors.New("stored Responses state is unsupported"),
			)
	}
	if wire.Background != nil && *wire.Background {
		return protocolcore.Request{}, protocolcore.TranslationReport{},
			invalidClient(
				"$.background",
				errors.New("background Responses execution is unsupported"),
			)
	}
	maxOutputTokens := 0
	if wire.MaxOutputTokens != nil {
		if *wire.MaxOutputTokens <= 0 || *wire.MaxOutputTokens > int64(math.MaxInt) {
			return protocolcore.Request{}, protocolcore.TranslationReport{},
				invalidClient(
					"$.max_output_tokens",
					errors.New("maximum output token count is invalid"),
				)
		}
		maxOutputTokens = int(*wire.MaxOutputTokens)
	}

	report := protocolcore.TranslationReport{}
	system := make([]protocolcore.ContentBlock, 0, 1)
	if wire.Instructions != nil {
		block, err := protocolcore.NewTextBlock(*wire.Instructions)
		if err != nil {
			return protocolcore.Request{}, report,
				invalidClient("$.instructions", err)
		}
		system = append(system, block)
	}
	messages := make([]protocolcore.Message, 0, len(wire.Input))
	tools := make([]protocolcore.ToolDefinition, 0, len(wire.Tools))
	namespaces := make([]protocolcore.ToolNamespace, 0)
	for index, raw := range wire.Input {
		decodedMessages, decodedTools, decodedNamespaces, itemReport, err :=
			codec.decodeInputItem(index, raw, !strictRoot)
		if err != nil {
			return protocolcore.Request{}, report, err
		}
		messages = append(messages, decodedMessages...)
		tools = append(tools, decodedTools...)
		namespaces = append(namespaces, decodedNamespaces...)
		report = report.Merge(itemReport)
	}
	for index, raw := range wire.Tools {
		path := fmt.Sprintf("$.tools[%d]", index)
		kind, err := peekType(raw)
		if err != nil {
			return protocolcore.Request{}, report, invalidClient(path, err)
		}
		if kind == "web_search" {
			var hosted webSearchToolWire
			if err := decodeStrict(raw, &hosted); err != nil {
				return protocolcore.Request{}, report, invalidClient(path, err)
			}
			report = report.Merge(notice(
				protocolcore.NoticeHostedToolNotForwarded,
				path,
			))
			continue
		}
		tool, namespace, err := codec.decodeTool(
			raw,
			path,
			true,
			!strictRoot,
		)
		if err != nil {
			return protocolcore.Request{}, report, err
		}
		if namespace != nil {
			namespaces = append(namespaces, *namespace)
		} else {
			tools = append(tools, tool)
		}
	}

	toolChoice, err := decodeToolChoice(
		wire.ToolChoice,
		wire.ParallelToolCalls,
	)
	if err != nil {
		return protocolcore.Request{}, report, err
	}
	reasoning, reasoningReport, err := decodeReasoning(wire.Reasoning)
	if err != nil {
		return protocolcore.Request{}, report, err
	}
	report = report.Merge(reasoningReport)
	verbosity, textReport, err := decodeText(wire.Text)
	if err != nil {
		return protocolcore.Request{}, report, err
	}
	report = report.Merge(textReport)
	includeReport, err := decodeInclude(wire.Include)
	if err != nil {
		return protocolcore.Request{}, report, err
	}
	report = report.Merge(includeReport)
	if wire.PromptCacheKey != "" {
		if err := validateBoundedString(
			wire.PromptCacheKey,
			512,
			false,
		); err != nil {
			return protocolcore.Request{}, report,
				invalidClient("$.prompt_cache_key", err)
		}
		report = report.Merge(notice(
			protocolcore.NoticePromptCacheKeyNotForwarded,
			"$.prompt_cache_key",
		))
	}
	protocolEvidence, err := decodeRequestProtocolEvidence(
		wire.ClientMetadata,
		wire.PreviousResponseID,
		wire.Input,
	)
	if err != nil {
		return protocolcore.Request{}, report, err
	}
	if rawPresent(wire.ClientMetadata) {
		report = report.Merge(notice(
			protocolcore.NoticeClientMetadataNotForwarded,
			"$.client_metadata",
		))
	}
	if wire.PreviousResponseID != nil {
		report = report.Merge(notice(
			protocolcore.NoticePreviousResponseIDNotForwarded,
			"$.previous_response_id",
		))
	}

	request := protocolcore.Request{
		RequestedModel:   wire.Model,
		EffectiveModel:   wire.Model,
		MaxOutputTokens:  maxOutputTokens,
		Stream:           wire.Stream,
		System:           system,
		Messages:         messages,
		Tools:            tools,
		ToolNamespaces:   namespaces,
		ToolChoice:       toolChoice,
		Reasoning:        reasoning,
		OutputVerbosity:  verbosity,
		ProtocolEvidence: protocolEvidence,
	}
	if err := request.Validate(); err != nil {
		return protocolcore.Request{}, report, invalidClient("$", err)
	}
	return request.Clone(), report, nil
}

func decodeRequestProtocolEvidence(
	clientMetadata json.RawMessage,
	previousResponseID *string,
	input []json.RawMessage,
) ([]protocolcore.ProtocolEvidenceValue, error) {
	byName := make(map[string]string, 4)
	if previousResponseID != nil {
		if err := validateBoundedString(
			*previousResponseID,
			protocolcore.MaxProtocolEvidenceValueBytes,
			false,
		); err != nil {
			return nil, invalidClient("$.previous_response_id", err)
		}
		byName["openai_responses.previous_response_id"] = *previousResponseID
	}
	if rawPresent(clientMetadata) {
		if _, err := protocolcore.NewJSONObject(clientMetadata, MaxMetadataBytes); err != nil {
			return nil, invalidClient("$.client_metadata", err)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(clientMetadata, &object); err != nil {
			return nil, invalidClient("$.client_metadata", err)
		}
		for _, field := range []struct {
			wireName     string
			evidenceName string
		}{
			{wireName: "session_id", evidenceName: "openai_responses.session_id"},
			{wireName: "thread_id", evidenceName: "openai_responses.thread_id"},
			{wireName: "turn_id", evidenceName: "openai_responses.turn_id"},
		} {
			raw, exists := object[field.wireName]
			if !exists {
				continue
			}
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, invalidClient(
					"$.client_metadata."+field.wireName,
					errors.New("identifier must be a string"),
				)
			}
			if err := validateBoundedString(
				value,
				protocolcore.MaxProtocolEvidenceValueBytes,
				false,
			); err != nil {
				return nil, invalidClient("$.client_metadata."+field.wireName, err)
			}
			byName[field.evidenceName] = value
		}
	}
	// Current Codex clients put the native turn identity on each input item,
	// rather than in top-level client_metadata. The last tagged input belongs to
	// the request being issued; earlier tags are retained history from older
	// turns. Raw HTTP remains the authority for that full history, while this
	// bounded value is the exact join into Codex's local rollout.
	if _, explicit := byName["openai_responses.turn_id"]; !explicit {
		for index, raw := range input {
			var carrier struct {
				InternalMetadata json.RawMessage `json:"internal_chat_message_metadata_passthrough,omitempty"`
			}
			if err := json.Unmarshal(raw, &carrier); err != nil || !rawPresent(carrier.InternalMetadata) {
				continue
			}
			var metadata internalMessageMetadataWire
			if err := json.Unmarshal(carrier.InternalMetadata, &metadata); err != nil {
				return nil, invalidClient(
					fmt.Sprintf("$.input[%d].internal_chat_message_metadata_passthrough", index),
					err,
				)
			}
			if err := validateBoundedString(
				metadata.TurnID,
				protocolcore.MaxProtocolEvidenceValueBytes,
				false,
			); err != nil {
				return nil, invalidClient(
					fmt.Sprintf("$.input[%d].internal_chat_message_metadata_passthrough.turn_id", index),
					err,
				)
			}
			byName["openai_responses.turn_id"] = metadata.TurnID
		}
	}
	evidence := make([]protocolcore.ProtocolEvidenceValue, 0, len(byName))
	for name, value := range byName {
		evidence = append(evidence, protocolcore.ProtocolEvidenceValue{Name: name, Value: value})
	}
	slices.SortFunc(evidence, func(left, right protocolcore.ProtocolEvidenceValue) int {
		return strings.Compare(left.Name, right.Name)
	})
	if err := protocolcore.ValidateProtocolEvidence(evidence); err != nil {
		return nil, invalidClient("$", err)
	}
	return evidence, nil
}

func (codec *Codec) decodeInputItem(
	index int,
	raw json.RawMessage,
	compatible bool,
) (
	[]protocolcore.Message,
	[]protocolcore.ToolDefinition,
	[]protocolcore.ToolNamespace,
	protocolcore.TranslationReport,
	error,
) {
	path := fmt.Sprintf("$.input[%d]", index)
	kind, err := peekType(raw)
	if err != nil {
		return nil, nil, nil, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	switch kind {
	case "agent_message":
		message, report, err := decodeAgentMessage(raw, path, compatible)
		return singleton(message), nil, nil, report, err
	case "multi_agent_call":
		message, report, err := decodeMultiAgentCall(raw, path, compatible)
		return singleton(message), nil, nil, report, err
	case "multi_agent_call_output":
		message, report, err := decodeMultiAgentCallOutput(raw, path, compatible)
		return singleton(message), nil, nil, report, err
	case "reasoning":
		message, err := decodeResponsesReasoningItem(raw, path, true)
		return singleton(message), nil, nil, protocolcore.TranslationReport{}, err
	case "message":
		message, report, err := decodeInputMessage(raw, path, compatible)
		return singleton(message), nil, nil, report, err
	case "function_call":
		message, report, err := decodeFunctionCall(raw, path, compatible)
		return singleton(message), nil, nil, report, err
	case "function_call_output":
		message, report, err := decodeFunctionCallOutput(raw, path, compatible)
		return singleton(message), nil, nil, report, err
	case "custom_tool_call":
		message, report, err := decodeCustomToolCall(raw, path, compatible)
		return singleton(message), nil, nil, report, err
	case "custom_tool_call_output":
		message, report, err := decodeCustomToolCallOutput(raw, path, compatible)
		return singleton(message), nil, nil, report, err
	case "additional_tools":
		var wire additionalToolsWire
		if err := decodeClientWire(raw, &wire, compatible); err != nil {
			return nil, nil, nil, protocolcore.TranslationReport{},
				invalidClient(path, err)
		}
		if wire.Role != "developer" || len(wire.Tools) == 0 {
			return nil, nil, nil, protocolcore.TranslationReport{},
				invalidClient(path, errors.New("additional tools item is invalid"))
		}
		tools := make([]protocolcore.ToolDefinition, 0, len(wire.Tools))
		namespaces := make([]protocolcore.ToolNamespace, 0)
		for toolIndex, rawTool := range wire.Tools {
			tool, namespace, err := codec.decodeTool(
				rawTool,
				fmt.Sprintf("%s.tools[%d]", path, toolIndex),
				true,
				compatible,
			)
			if err != nil {
				return nil, nil, nil, protocolcore.TranslationReport{}, err
			}
			if namespace != nil {
				namespaces = append(namespaces, *namespace)
			} else {
				tools = append(tools, tool)
			}
		}
		return nil, tools, namespaces, notice(
			protocolcore.NoticeToolPlacementNormalized,
			path,
		), nil
	default:
		return nil, nil, nil, protocolcore.TranslationReport{},
			invalidClient(
				path+".type",
				errors.New("Responses input item type is unsupported"),
			)
	}
}

func decodeAgentMessage(
	raw json.RawMessage,
	path string,
	compatible bool,
) (protocolcore.Message, protocolcore.TranslationReport, error) {
	if !compatible {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path+".type", errors.New("agent messages are only supported on a same-dialect path"))
	}
	message, id, err := decodeAgentMessageItem(raw, path, false)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{}, err
	}
	return message, agentIdentityReport(id, path), nil
}

func decodeAgentMessageItem(
	raw json.RawMessage,
	path string,
	provider bool,
) (protocolcore.Message, string, error) {
	failure := func(failurePath string, cause error) error {
		if provider {
			return invalidProvider(failurePath, cause)
		}
		return invalidClient(failurePath, cause)
	}
	var wire agentMessageWire
	if err := rejectDuplicateNames(raw); err != nil {
		return protocolcore.Message{}, "", failure(path, err)
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return protocolcore.Message{}, "", failure(path, err)
	}
	if wire.Type != "agent_message" || len(wire.Content) == 0 {
		return protocolcore.Message{}, "", failure(path, errors.New("agent message is invalid"))
	}
	context, err := decodeAgentContext(wire.Agent, wire.Author, wire.Recipient)
	if err != nil {
		return protocolcore.Message{}, "", failure(path, err)
	}
	blocks := make([]protocolcore.ContentBlock, 0, len(wire.Content))
	for index, item := range wire.Content {
		itemPath := fmt.Sprintf("%s.content[%d]", path, index)
		var content agentMessageContentWire
		if err := rejectDuplicateNames(item); err != nil {
			return protocolcore.Message{}, "", failure(itemPath, err)
		}
		if err := json.Unmarshal(item, &content); err != nil {
			return protocolcore.Message{}, "", failure(itemPath, err)
		}
		switch content.Type {
		case "input_text", "output_text", "text":
			block, err := protocolcore.NewTextBlock(content.Text)
			if err != nil {
				return protocolcore.Message{}, "", failure(itemPath+".text", err)
			}
			blocks = append(blocks, block)
		case "summary_text":
			block, err := newResponsesExtensionBlock(
				protocolcore.ProviderExtensionReasoningSummary,
				itemPath,
				item,
			)
			if err != nil {
				return protocolcore.Message{}, "", failure(itemPath, err)
			}
			blocks = append(blocks, block)
		case "reasoning_text":
			block, err := newResponsesExtensionBlock(
				protocolcore.ProviderExtensionReasoningContent,
				itemPath,
				item,
			)
			if err != nil {
				return protocolcore.Message{}, "", failure(itemPath, err)
			}
			blocks = append(blocks, block)
		case "refusal":
			block, err := protocolcore.NewRefusalBlock(content.Refusal)
			if err != nil {
				return protocolcore.Message{}, "", failure(itemPath+".refusal", err)
			}
			blocks = append(blocks, block)
		case "encrypted_content":
			if content.EncryptedContent == "" {
				return protocolcore.Message{}, "", failure(itemPath+".encrypted_content", errors.New("encrypted agent content is empty"))
			}
			block, err := newResponsesExtensionBlock(
				protocolcore.ProviderExtensionAgentMessageEncryptedContent,
				itemPath,
				item,
			)
			if err != nil {
				return protocolcore.Message{}, "", failure(itemPath, err)
			}
			blocks = append(blocks, block)
		case "input_image":
			block, err := newResponsesExtensionBlock(
				protocolcore.ProviderExtensionAgentMessageImage,
				itemPath,
				item,
			)
			if err != nil {
				return protocolcore.Message{}, "", failure(itemPath, err)
			}
			blocks = append(blocks, block)
		case "input_file":
			block, err := newResponsesExtensionBlock(
				protocolcore.ProviderExtensionAgentMessageFile,
				itemPath,
				item,
			)
			if err != nil {
				return protocolcore.Message{}, "", failure(itemPath, err)
			}
			blocks = append(blocks, block)
		case "computer_screenshot":
			block, err := newResponsesExtensionBlock(
				protocolcore.ProviderExtensionAgentMessageScreenshot,
				itemPath,
				item,
			)
			if err != nil {
				return protocolcore.Message{}, "", failure(itemPath, err)
			}
			blocks = append(blocks, block)
		default:
			return protocolcore.Message{}, "",
				failure(itemPath+".type", errors.New("agent message content type is unsupported"))
		}
	}
	message := protocolcore.Message{Role: protocolcore.RoleAssistant, Blocks: blocks, Agent: &context}
	if err := message.Validate(); err != nil {
		return protocolcore.Message{}, "", failure(path, err)
	}
	return message, wire.ID, nil
}

func decodeMultiAgentCall(
	raw json.RawMessage,
	path string,
	compatible bool,
) (protocolcore.Message, protocolcore.TranslationReport, error) {
	if !compatible {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path+".type", errors.New("multi-agent calls are only supported on a same-dialect path"))
	}
	message, id, err := decodeMultiAgentCallItem(raw, path, false)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{}, err
	}
	return message, agentIdentityReport(id, path), nil
}

func decodeMultiAgentCallItem(
	raw json.RawMessage,
	path string,
	provider bool,
) (protocolcore.Message, string, error) {
	failure := func(failurePath string, cause error) error {
		if provider {
			return invalidProvider(failurePath, cause)
		}
		return invalidClient(failurePath, cause)
	}
	var wire multiAgentCallWire
	if err := rejectDuplicateNames(raw); err != nil {
		return protocolcore.Message{}, "", failure(path, err)
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return protocolcore.Message{}, "", failure(path, err)
	}
	if wire.Type != "multi_agent_call" || !validMultiAgentAction(wire.Action) {
		return protocolcore.Message{}, "", failure(path, errors.New("multi-agent call is invalid"))
	}
	arguments, err := protocolcore.NewJSONObject([]byte(wire.Arguments), protocolcore.MaxToolJSONBytes)
	if err != nil {
		return protocolcore.Message{}, "", failure(path+".arguments", err)
	}
	call, err := newMultiAgentToolCall(wire.ID, wire.CallID, wire.Action, arguments)
	if err != nil {
		return protocolcore.Message{}, "", failure(path, err)
	}
	block, err := protocolcore.NewToolCallBlock(call)
	if err != nil {
		return protocolcore.Message{}, "", failure(path, err)
	}
	context, err := decodeOptionalAgentContext(wire.Agent)
	if err != nil {
		return protocolcore.Message{}, "", failure(path+".agent", err)
	}
	message := protocolcore.Message{Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{block}, Agent: context}
	if err := message.Validate(); err != nil {
		return protocolcore.Message{}, "", failure(path, err)
	}
	return message, wire.ID, nil
}

func decodeMultiAgentCallOutput(
	raw json.RawMessage,
	path string,
	compatible bool,
) (protocolcore.Message, protocolcore.TranslationReport, error) {
	if !compatible {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path+".type", errors.New("multi-agent outputs are only supported on a same-dialect path"))
	}
	message, id, err := decodeMultiAgentCallOutputItem(raw, path, false)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{}, err
	}
	return message, agentIdentityReport(id, path), nil
}

func decodeMultiAgentCallOutputItem(
	raw json.RawMessage,
	path string,
	provider bool,
) (protocolcore.Message, string, error) {
	failure := func(failurePath string, cause error) error {
		if provider {
			return invalidProvider(failurePath, cause)
		}
		return invalidClient(failurePath, cause)
	}
	var wire multiAgentCallOutputWire
	if err := rejectDuplicateNames(raw); err != nil {
		return protocolcore.Message{}, "", failure(path, err)
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return protocolcore.Message{}, "", failure(path, err)
	}
	if wire.Type != "multi_agent_call_output" || !validMultiAgentAction(wire.Action) || len(wire.Output) == 0 {
		return protocolcore.Message{}, "", failure(path, errors.New("multi-agent call output is invalid"))
	}
	var output strings.Builder
	for index, item := range wire.Output {
		var text multiAgentOutputTextWire
		itemPath := fmt.Sprintf("%s.output[%d]", path, index)
		err := rejectDuplicateNames(item)
		if err == nil {
			err = json.Unmarshal(item, &text)
		}
		if err != nil || text.Type != "output_text" {
			if err == nil {
				err = errors.New("multi-agent output content type is invalid")
			}
			return protocolcore.Message{}, "", failure(itemPath, err)
		}
		if index > 0 {
			output.WriteByte('\n')
		}
		output.WriteString(text.Text)
	}
	key, err := protocolcore.NewCallKey("openai-responses-multi-agent-call", wire.CallID)
	if err != nil {
		return protocolcore.Message{}, "", failure(path+".call_id", err)
	}
	block, err := protocolcore.NewToolResultBlock(protocolcore.ToolResult{
		Key: key, Namespace: "multi_agent", Name: wire.Action,
		Content: output.String(),
	})
	if err != nil {
		return protocolcore.Message{}, "", failure(path, err)
	}
	context, err := decodeOptionalAgentContext(wire.Agent)
	if err != nil {
		return protocolcore.Message{}, "", failure(path+".agent", err)
	}
	message := protocolcore.Message{Role: protocolcore.RoleTool, Blocks: []protocolcore.ContentBlock{block}, Agent: context}
	if err := message.Validate(); err != nil {
		return protocolcore.Message{}, "", failure(path, err)
	}
	return message, wire.ID, nil
}

func decodeAgentContext(agent *agentItemWire, author, recipient string) (protocolcore.AgentMessageContext, error) {
	context := protocolcore.AgentMessageContext{Author: author, Recipient: recipient}
	if agent != nil {
		context.AgentName = agent.AgentName
	}
	if err := context.Validate(); err != nil {
		return protocolcore.AgentMessageContext{}, err
	}
	return context, nil
}

func decodeOptionalAgentContext(agent *agentItemWire) (*protocolcore.AgentMessageContext, error) {
	if agent == nil {
		return nil, nil
	}
	context, err := decodeAgentContext(agent, "", "")
	if err != nil {
		return nil, err
	}
	return &context, nil
}

func agentIdentityReport(itemID, path string) protocolcore.TranslationReport {
	if itemID == "" {
		return protocolcore.TranslationReport{}
	}
	return notice(protocolcore.NoticeAgentItemIdentityNotForwarded, path+".id")
}

func validMultiAgentAction(action string) bool {
	switch action {
	case "spawn_agent", "interrupt_agent", "list_agents", "send_message", "followup_task", "wait_agent":
		return true
	default:
		return false
	}
}

func newMultiAgentToolCall(itemID, callID, action string, arguments protocolcore.JSONDocument) (protocolcore.ToolCall, error) {
	key, err := protocolcore.NewCallKey("openai-responses-multi-agent-call", callID)
	if err != nil {
		return protocolcore.ToolCall{}, err
	}
	var itemKey protocolcore.CallKey
	if itemID != "" {
		itemKey, err = protocolcore.NewCallKey("openai-responses-item", itemID)
		if err != nil {
			return protocolcore.ToolCall{}, err
		}
	}
	call := protocolcore.ToolCall{
		Kind: protocolcore.ToolKindFunction, Key: key, ItemKey: itemKey,
		Namespace: "multi_agent", Name: action, Arguments: arguments,
	}
	if err := call.Validate(); err != nil {
		return protocolcore.ToolCall{}, err
	}
	return call, nil
}

func newResponsesExtensionBlock(kind protocolcore.ProviderExtensionKind, path string, raw json.RawMessage) (protocolcore.ContentBlock, error) {
	extension, err := protocolcore.NewProviderExtension(
		protocolcore.ProviderExtensionSourceOpenAIResponses,
		kind,
		path,
		[][]byte{append([]byte(nil), raw...)},
	)
	if err != nil {
		return protocolcore.ContentBlock{}, err
	}
	return protocolcore.NewProviderExtensionBlock(extension)
}

func decodeResponsesReasoningItem(
	raw json.RawMessage,
	path string,
	clientInput bool,
) (protocolcore.Message, error) {
	var wire responseReasoningItemWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return protocolcore.Message{}, responsesReasoningFailure(clientInput, path, err)
	}
	if err := validateBoundedString(wire.ID, 512, false); err != nil {
		return protocolcore.Message{}, responsesReasoningFailure(clientInput, path+".id", err)
	}
	if wire.Type != "reasoning" {
		return protocolcore.Message{}, responsesReasoningFailure(
			clientInput,
			path+".type",
			errors.New("Responses reasoning item type is invalid"),
		)
	}
	if wire.Status != "" && wire.Status != "in_progress" &&
		wire.Status != "completed" && wire.Status != "incomplete" {
		return protocolcore.Message{}, responsesReasoningFailure(
			clientInput,
			path+".status",
			errors.New("Responses reasoning item status is invalid"),
		)
	}

	blocks := make([]protocolcore.ContentBlock, 0, 3)
	appendExtension := func(
		kind protocolcore.ProviderExtensionKind,
		extensionPath string,
		fragments [][]byte,
	) error {
		if len(fragments) == 0 {
			return nil
		}
		extension, err := protocolcore.NewProviderExtension(
			protocolcore.ProviderExtensionSourceOpenAIResponses,
			kind,
			extensionPath,
			fragments,
		)
		if err != nil {
			return err
		}
		block, err := protocolcore.NewProviderExtensionBlock(extension)
		if err != nil {
			return err
		}
		blocks = append(blocks, block)
		return nil
	}
	decodeTextFragments := func(
		values []json.RawMessage,
		wantType string,
		fieldPath string,
	) ([][]byte, error) {
		fragments := make([][]byte, 0, len(values))
		for index, value := range values {
			var text responseReasoningTextWire
			if err := json.Unmarshal(value, &text); err != nil {
				return nil, responsesReasoningFailure(
					clientInput,
					fmt.Sprintf("%s[%d]", fieldPath, index),
					err,
				)
			}
			if text.Type != wantType || !utf8.ValidString(text.Text) {
				return nil, responsesReasoningFailure(
					clientInput,
					fmt.Sprintf("%s[%d]", fieldPath, index),
					errors.New("Responses reasoning text is invalid"),
				)
			}
			fragments = append(fragments, append([]byte(nil), value...))
		}
		return fragments, nil
	}
	summary, err := decodeTextFragments(wire.Summary, "summary_text", path+".summary")
	if err != nil {
		return protocolcore.Message{}, err
	}
	if err := appendExtension(
		protocolcore.ProviderExtensionReasoningSummary,
		path+".summary",
		summary,
	); err != nil {
		return protocolcore.Message{}, responsesReasoningFailure(clientInput, path+".summary", err)
	}
	content, err := decodeTextFragments(wire.Content, "reasoning_text", path+".content")
	if err != nil {
		return protocolcore.Message{}, err
	}
	if err := appendExtension(
		protocolcore.ProviderExtensionReasoningContent,
		path+".content",
		content,
	); err != nil {
		return protocolcore.Message{}, responsesReasoningFailure(clientInput, path+".content", err)
	}
	if rawPresent(wire.EncryptedContent) {
		var encrypted string
		if err := json.Unmarshal(wire.EncryptedContent, &encrypted); err != nil || encrypted == "" {
			if err == nil {
				err = errors.New("encrypted reasoning content is empty")
			}
			return protocolcore.Message{}, responsesReasoningFailure(
				clientInput,
				path+".encrypted_content",
				err,
			)
		}
		if err := appendExtension(
			protocolcore.ProviderExtensionReasoningEncryptedContent,
			path+".encrypted_content",
			[][]byte{append([]byte(nil), wire.EncryptedContent...)},
		); err != nil {
			return protocolcore.Message{}, responsesReasoningFailure(
				clientInput,
				path+".encrypted_content",
				err,
			)
		}
	}
	context, err := decodeOptionalAgentContext(wire.Agent)
	if err != nil {
		return protocolcore.Message{}, responsesReasoningFailure(clientInput, path+".agent", err)
	}
	return protocolcore.Message{Role: protocolcore.RoleAssistant, Blocks: blocks, Agent: context}, nil
}

func responsesReasoningFailure(clientInput bool, path string, cause error) error {
	if clientInput {
		return invalidClient(path, cause)
	}
	return invalidProvider(path, cause)
}

func decodeInputMessage(
	raw json.RawMessage,
	path string,
	compatible bool,
) (
	protocolcore.Message,
	protocolcore.TranslationReport,
	error,
) {
	var wire inputMessageWire
	if err := decodeClientWire(raw, &wire, compatible); err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	var role protocolcore.Role
	switch wire.Role {
	case "system":
		role = protocolcore.RoleSystem
	case "developer":
		role = protocolcore.RoleDeveloper
	case "user":
		role = protocolcore.RoleUser
	case "assistant":
		role = protocolcore.RoleAssistant
	default:
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(
				path+".role",
				errors.New("Responses message role is unsupported"),
			)
	}
	if wire.Phase != "" && wire.Phase != "commentary" && wire.Phase != "final_answer" {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(
				path+".phase",
				errors.New("Responses assistant phase is invalid"),
			)
	}
	report := protocolcore.TranslationReport{}
	if wire.Phase != "" {
		if !compatible {
			return protocolcore.Message{}, report,
				invalidClient(
					path+".phase",
					errors.New("Responses assistant phase is unsupported across dialects"),
				)
		}
		report = report.Merge(notice(
			protocolcore.NoticeMessagePhaseNotProjected,
			path+".phase",
		))
	}
	if wire.ID != "" {
		if err := validateBoundedString(wire.ID, 512, false); err != nil {
			return protocolcore.Message{}, report,
				invalidClient(path+".id", err)
		}
		report = report.Merge(notice(
			protocolcore.NoticeMessageItemIdentityNotForwarded,
			path+".id",
		))
	}
	metadataReport, err := decodeInternalMessageMetadata(
		wire.InternalMetadata,
		path+".internal_chat_message_metadata_passthrough",
		compatible,
	)
	if err != nil {
		return protocolcore.Message{}, report, err
	}
	report = report.Merge(metadataReport)
	blocks, err := decodeMessageContent(wire.Content, role, path+".content", compatible)
	if err != nil {
		return protocolcore.Message{}, report, err
	}
	context, err := decodeOptionalAgentContext(wire.Agent)
	if err != nil {
		return protocolcore.Message{}, report, invalidClient(path+".agent", err)
	}
	message := protocolcore.Message{Role: role, Blocks: blocks, Agent: context}
	if err := message.Validate(); err != nil {
		return protocolcore.Message{}, report,
			invalidClient(path, err)
	}
	return message, report, nil
}

func decodeInternalMessageMetadata(
	raw json.RawMessage,
	path string,
	compatible bool,
) (protocolcore.TranslationReport, error) {
	if !rawPresent(raw) {
		return protocolcore.TranslationReport{}, nil
	}
	if _, err := protocolcore.NewJSONObject(raw, MaxMetadataBytes); err != nil {
		return protocolcore.TranslationReport{}, invalidClient(path, err)
	}
	var wire internalMessageMetadataWire
	if err := decodeClientWire(raw, &wire, compatible); err != nil {
		return protocolcore.TranslationReport{}, invalidClient(path, err)
	}
	if err := validateBoundedString(wire.TurnID, 512, false); err != nil {
		return protocolcore.TranslationReport{},
			invalidClient(path+".turn_id", err)
	}
	return notice(
		protocolcore.NoticeInternalMessageMetadataNotForwarded,
		path,
	), nil
}

func decodeMessageContent(
	raw json.RawMessage,
	role protocolcore.Role,
	path string,
	compatible bool,
) ([]protocolcore.ContentBlock, error) {
	if len(raw) == 0 {
		return nil, invalidClient(path, errors.New("message content is missing"))
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		block, blockErr := protocolcore.NewTextBlock(text)
		if blockErr != nil {
			return nil, invalidClient(path, blockErr)
		}
		return []protocolcore.ContentBlock{block}, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) == 0 {
		return nil, invalidClient(
			path,
			errors.New("message content must be text or a nonempty array"),
		)
	}
	blocks := make([]protocolcore.ContentBlock, len(parts))
	for index, rawPart := range parts {
		var part inputContentWire
		if err := decodeClientWire(rawPart, &part, compatible); err != nil {
			return nil, invalidClient(
				fmt.Sprintf("%s[%d]", path, index),
				err,
			)
		}
		var block protocolcore.ContentBlock
		var err error
		switch part.Type {
		case "input_text", "output_text":
			if part.Refusal != "" {
				return nil, invalidClient(
					fmt.Sprintf("%s[%d]", path, index),
					errors.New("text content contains a refusal"),
				)
			}
			block, err = protocolcore.NewTextBlock(part.Text)
		case "refusal":
			if role != protocolcore.RoleAssistant {
				return nil, invalidClient(
					fmt.Sprintf("%s[%d].type", path, index),
					errors.New("refusal content requires an assistant role"),
				)
			}
			if part.Text != "" {
				return nil, invalidClient(
					fmt.Sprintf("%s[%d]", path, index),
					errors.New("refusal content contains text"),
				)
			}
			block, err = protocolcore.NewRefusalBlock(part.Refusal)
		default:
			return nil, invalidClient(
				fmt.Sprintf("%s[%d].type", path, index),
				errors.New("Responses content type is unsupported"),
			)
		}
		if err != nil {
			return nil, invalidClient(fmt.Sprintf("%s[%d]", path, index), err)
		}
		blocks[index] = block
	}
	return blocks, nil
}

func decodeFunctionCall(
	raw json.RawMessage,
	path string,
	compatible bool,
) (
	protocolcore.Message,
	protocolcore.TranslationReport,
	error,
) {
	var wire functionCallWire
	if err := decodeClientWire(raw, &wire, compatible); err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	metadataReport, err := decodeInternalMessageMetadata(
		wire.InternalMetadata,
		path+".internal_chat_message_metadata_passthrough",
		compatible,
	)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{}, err
	}
	if wire.Status != "" && wire.Status != "completed" {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(
				path+".status",
				errors.New("function call is not complete"),
			)
	}
	arguments, err := protocolcore.NewJSONObject(
		[]byte(wire.Arguments),
		protocolcore.MaxToolJSONBytes,
	)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path+".arguments", err)
	}
	call, err := newToolCall(
		protocolcore.ToolKindFunction,
		wire.ID,
		wire.CallID,
		wire.Namespace,
		wire.Name,
		arguments,
		"",
	)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	block, err := protocolcore.NewToolCallBlock(call)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	context, err := decodeOptionalAgentContext(wire.Agent)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{}, invalidClient(path+".agent", err)
	}
	return protocolcore.Message{
		Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{block},
		Agent: context,
	}, metadataReport, nil
}

func decodeCustomToolCall(
	raw json.RawMessage,
	path string,
	compatible bool,
) (
	protocolcore.Message,
	protocolcore.TranslationReport,
	error,
) {
	var wire customToolCallWire
	if err := decodeClientWire(raw, &wire, compatible); err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	metadataReport, err := decodeInternalMessageMetadata(
		wire.InternalMetadata,
		path+".internal_chat_message_metadata_passthrough",
		compatible,
	)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{}, err
	}
	call, err := newToolCall(
		protocolcore.ToolKindCustom,
		wire.ID,
		wire.CallID,
		wire.Namespace,
		wire.Name,
		protocolcore.JSONDocument{},
		wire.Input,
	)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	block, err := protocolcore.NewToolCallBlock(call)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	context, err := decodeOptionalAgentContext(wire.Agent)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{}, invalidClient(path+".agent", err)
	}
	return protocolcore.Message{
		Role: protocolcore.RoleAssistant, Blocks: []protocolcore.ContentBlock{block},
		Agent: context,
	}, metadataReport, nil
}

func decodeFunctionCallOutput(
	raw json.RawMessage,
	path string,
	compatible bool,
) (
	protocolcore.Message,
	protocolcore.TranslationReport,
	error,
) {
	var wire functionCallOutputWire
	if err := decodeClientWire(raw, &wire, compatible); err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	metadataReport, err := decodeInternalMessageMetadata(
		wire.InternalMetadata,
		path+".internal_chat_message_metadata_passthrough",
		compatible,
	)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{}, err
	}
	if wire.Status != "" && wire.Status != "completed" {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(
				path+".status",
				errors.New("function call output is not complete"),
			)
	}
	message, outputReport, err := decodeToolOutput(
		wire.CallID,
		wire.Output,
		path,
		compatible,
	)
	if err == nil {
		message.Agent, err = decodeOptionalAgentContext(wire.Agent)
		if err != nil {
			return protocolcore.Message{}, protocolcore.TranslationReport{}, invalidClient(path+".agent", err)
		}
	}
	return message, metadataReport.Merge(outputReport), err
}

func decodeCustomToolCallOutput(
	raw json.RawMessage,
	path string,
	compatible bool,
) (
	protocolcore.Message,
	protocolcore.TranslationReport,
	error,
) {
	var wire customToolCallOutputWire
	if err := decodeClientWire(raw, &wire, compatible); err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	metadataReport, err := decodeInternalMessageMetadata(
		wire.InternalMetadata,
		path+".internal_chat_message_metadata_passthrough",
		compatible,
	)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{}, err
	}
	message, outputReport, err := decodeToolOutput(
		wire.CallID,
		wire.Output,
		path,
		compatible,
	)
	if err == nil {
		message.Agent, err = decodeOptionalAgentContext(wire.Agent)
		if err != nil {
			return protocolcore.Message{}, protocolcore.TranslationReport{}, invalidClient(path+".agent", err)
		}
	}
	return message, metadataReport.Merge(outputReport), err
}

func decodeToolOutput(
	callID string,
	raw json.RawMessage,
	path string,
	compatible bool,
) (
	protocolcore.Message,
	protocolcore.TranslationReport,
	error,
) {
	output, report, err := decodeToolOutputText(raw, path+".output", compatible)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{}, err
	}
	key, err := protocolcore.NewCallKey("openai-responses-call", callID)
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path+".call_id", err)
	}
	block, err := protocolcore.NewToolResultBlock(protocolcore.ToolResult{
		Key:     key,
		Content: output,
	})
	if err != nil {
		return protocolcore.Message{}, protocolcore.TranslationReport{},
			invalidClient(path, err)
	}
	return protocolcore.Message{
		Role:   protocolcore.RoleTool,
		Blocks: []protocolcore.ContentBlock{block},
	}, report, nil
}

func decodeToolOutputText(
	raw json.RawMessage,
	path string,
	compatible bool,
) (string, protocolcore.TranslationReport, error) {
	var output string
	if json.Unmarshal(raw, &output) == nil {
		return output, protocolcore.TranslationReport{}, nil
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(raw, &rawItems); err != nil ||
		len(rawItems) == 0 {
		return "", protocolcore.TranslationReport{}, invalidClient(
			path,
			errors.New(
				"tool output must be text or a nonempty content array",
			),
		)
	}
	var normalized strings.Builder
	for index, rawItem := range rawItems {
		var item toolOutputContentWire
		if err := decodeClientWire(rawItem, &item, compatible); err != nil {
			return "", protocolcore.TranslationReport{},
				invalidClient(
					fmt.Sprintf("%s[%d]", path, index),
					err,
				)
		}
		if item.Type != "input_text" {
			return "", protocolcore.TranslationReport{},
				invalidClient(
					fmt.Sprintf("%s[%d].type", path, index),
					errors.New(
						"tool output content type is unsupported",
					),
				)
		}
		if index > 0 {
			normalized.WriteByte('\n')
		}
		normalized.WriteString(item.Text)
	}
	return normalized.String(), notice(
		protocolcore.NoticeToolOutputContentNormalized,
		path,
	), nil
}

func newToolCall(
	kind protocolcore.ToolKind,
	itemID string,
	callID string,
	namespace string,
	name string,
	arguments protocolcore.JSONDocument,
	input string,
) (protocolcore.ToolCall, error) {
	key, err := protocolcore.NewCallKey("openai-responses-call", callID)
	if err != nil {
		return protocolcore.ToolCall{}, err
	}
	var itemKey protocolcore.CallKey
	if itemID != "" {
		itemKey, err = protocolcore.NewCallKey(
			"openai-responses-item",
			itemID,
		)
		if err != nil {
			return protocolcore.ToolCall{}, err
		}
	}
	call := protocolcore.ToolCall{
		Kind:      kind,
		Key:       key,
		ItemKey:   itemKey,
		Namespace: namespace,
		Name:      name,
		Arguments: arguments,
		Input:     input,
	}
	if err := call.Validate(); err != nil {
		return protocolcore.ToolCall{}, err
	}
	return call, nil
}

func (codec *Codec) decodeTool(
	raw json.RawMessage,
	path string,
	allowNamespace bool,
	compatible bool,
) (
	protocolcore.ToolDefinition,
	*protocolcore.ToolNamespace,
	error,
) {
	kind, err := peekType(raw)
	if err != nil {
		return protocolcore.ToolDefinition{}, nil, invalidClient(path, err)
	}
	switch kind {
	case "function":
		tool, err := decodeFunctionTool(raw, path, compatible)
		return tool, nil, err
	case "custom":
		tool, err := decodeCustomTool(raw, path, compatible)
		return tool, nil, err
	case "namespace":
		if !allowNamespace {
			return protocolcore.ToolDefinition{}, nil, invalidClient(
				path+".type",
				errors.New("nested tool namespaces are unsupported"),
			)
		}
		var wire namespaceToolWire
		if err := decodeStrict(raw, &wire); err != nil {
			return protocolcore.ToolDefinition{}, nil, invalidClient(path, err)
		}
		namespace := protocolcore.ToolNamespace{
			Name:        wire.Name,
			Description: wire.Description,
			Tools:       make([]protocolcore.ToolDefinition, len(wire.Tools)),
		}
		if compatible && namespace.Description == "" {
			// ChatGPT's Codex namespace wire may omit a display description.
			// The original source body remains authoritative on this path; use a
			// stable audit label only to satisfy the neutral model invariant.
			namespace.Description = namespace.Name
		}
		for index, rawTool := range wire.Tools {
			tool, nested, err := codec.decodeTool(
				rawTool,
				fmt.Sprintf("%s.tools[%d]", path, index),
				false,
				compatible,
			)
			if err != nil {
				return protocolcore.ToolDefinition{}, nil, err
			}
			if nested != nil {
				return protocolcore.ToolDefinition{}, nil, invalidClient(
					path,
					errors.New("nested tool namespace is invalid"),
				)
			}
			namespace.Tools[index] = tool
		}
		if err := namespace.Validate(); err != nil {
			return protocolcore.ToolDefinition{}, nil, invalidClient(path, err)
		}
		return protocolcore.ToolDefinition{}, &namespace, nil
	default:
		return protocolcore.ToolDefinition{}, nil, invalidClient(
			path+".type",
			errors.New("Responses tool type is unsupported"),
		)
	}
}

func decodeFunctionTool(
	raw json.RawMessage,
	path string,
	compatible bool,
) (protocolcore.ToolDefinition, error) {
	var wire functionToolWire
	if err := decodeCompatibleJSON(raw, &wire, compatible); err != nil {
		return protocolcore.ToolDefinition{}, invalidClient(path, err)
	}
	if !compatible && (rawPresent(wire.OutputSchema) ||
		(wire.DeferLoading != nil && *wire.DeferLoading) ||
		len(wire.AllowedCallers) != 0) {
		return protocolcore.ToolDefinition{}, invalidClient(
			path,
			errors.New("function tool capability is unsupported"),
		)
	}
	schema, err := protocolcore.NewJSONObject(
		wire.Parameters,
		protocolcore.MaxToolJSONBytes,
	)
	if err != nil {
		return protocolcore.ToolDefinition{}, invalidClient(
			path+".parameters",
			err,
		)
	}
	tool := protocolcore.ToolDefinition{
		Kind:        protocolcore.ToolKindFunction,
		Name:        wire.Name,
		Description: wire.Description,
		InputSchema: schema,
	}
	if wire.Strict != nil {
		tool.StrictKnown = true
		tool.Strict = *wire.Strict
	}
	if err := tool.Validate(); err != nil {
		return protocolcore.ToolDefinition{}, invalidClient(path, err)
	}
	return tool, nil
}

func decodeCustomTool(
	raw json.RawMessage,
	path string,
	compatible bool,
) (protocolcore.ToolDefinition, error) {
	var wire customToolWire
	if err := decodeCompatibleJSON(raw, &wire, compatible); err != nil {
		return protocolcore.ToolDefinition{}, invalidClient(path, err)
	}
	if !compatible && ((wire.DeferLoading != nil && *wire.DeferLoading) ||
		len(wire.AllowedCallers) != 0) {
		return protocolcore.ToolDefinition{}, invalidClient(
			path,
			errors.New("custom tool capability is unsupported"),
		)
	}
	format := protocolcore.CustomToolFormat{
		Kind: protocolcore.CustomToolFormatText,
	}
	if rawPresent(wire.Format) {
		var decoded customFormatWire
		if err := decodeStrict(wire.Format, &decoded); err != nil {
			return protocolcore.ToolDefinition{}, invalidClient(
				path+".format",
				err,
			)
		}
		format = protocolcore.CustomToolFormat{
			Kind:       protocolcore.CustomToolFormatKind(decoded.Type),
			Syntax:     decoded.Syntax,
			Definition: decoded.Definition,
		}
	}
	tool := protocolcore.ToolDefinition{
		Kind:         protocolcore.ToolKindCustom,
		Name:         wire.Name,
		Description:  wire.Description,
		CustomFormat: format,
	}
	if err := tool.Validate(); err != nil {
		return protocolcore.ToolDefinition{}, invalidClient(path, err)
	}
	return tool, nil
}

func decodeCompatibleJSON(raw []byte, destination any, compatible bool) error {
	if !compatible {
		return decodeStrict(raw, destination)
	}
	if err := rejectDuplicateNames(raw); err != nil {
		return err
	}
	return json.Unmarshal(raw, destination)
}

func decodeToolChoice(
	raw json.RawMessage,
	parallel *bool,
) (protocolcore.ToolChoice, error) {
	choice := protocolcore.ToolChoice{}
	if rawPresent(raw) {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return protocolcore.ToolChoice{}, invalidClient(
				"$.tool_choice",
				errors.New("object tool choices are unsupported"),
			)
		}
		switch value {
		case "auto":
			choice.Mode = protocolcore.ToolChoiceAuto
		case "required":
			choice.Mode = protocolcore.ToolChoiceRequired
		case "none":
			choice.Mode = protocolcore.ToolChoiceNone
		default:
			return protocolcore.ToolChoice{}, invalidClient(
				"$.tool_choice",
				errors.New("tool choice is unsupported"),
			)
		}
	}
	if parallel != nil && !*parallel {
		choice.DisableParallel = true
	}
	return choice, nil
}

func decodeReasoning(
	raw json.RawMessage,
) (
	protocolcore.ReasoningIntent,
	protocolcore.TranslationReport,
	error,
) {
	if !rawPresent(raw) {
		return protocolcore.ReasoningIntent{},
			protocolcore.TranslationReport{}, nil
	}
	var wire reasoningWire
	if err := decodeStrict(raw, &wire); err != nil {
		return protocolcore.ReasoningIntent{},
			protocolcore.TranslationReport{},
			invalidClient("$.reasoning", err)
	}
	if wire.GenerateSummary != "" {
		return protocolcore.ReasoningIntent{},
			protocolcore.TranslationReport{},
			invalidClient(
				"$.reasoning.generate_summary",
				errors.New("deprecated reasoning summary is unsupported"),
			)
	}
	intent := protocolcore.ReasoningIntent{
		Context:   protocolcore.ReasoningContext(wire.Context),
		Effort:    protocolcore.ReasoningEffort(wire.Effort),
		Summary:   protocolcore.ReasoningSummary(wire.Summary),
		Execution: protocolcore.ReasoningExecutionMode(wire.Mode),
	}
	report := protocolcore.TranslationReport{}
	if intent.Context != "" {
		report = report.Merge(notice(
			protocolcore.NoticeReasoningContextNotForwarded,
			"$.reasoning.context",
		))
	}
	return intent, report, nil
}

func decodeText(
	raw json.RawMessage,
) (protocolcore.TextVerbosity, protocolcore.TranslationReport, error) {
	if !rawPresent(raw) {
		return "", protocolcore.TranslationReport{}, nil
	}
	var wire textWire
	if err := decodeStrict(raw, &wire); err != nil {
		return "", protocolcore.TranslationReport{},
			invalidClient("$.text", err)
	}
	if rawPresent(wire.Format) {
		return "", protocolcore.TranslationReport{},
			invalidClient(
				"$.text.format",
				errors.New("Responses text format is unsupported"),
			)
	}
	verbosity := protocolcore.TextVerbosity(wire.Verbosity)
	report := protocolcore.TranslationReport{}
	if verbosity != "" {
		report = notice(
			protocolcore.NoticeTextVerbosityNotForwarded,
			"$.text.verbosity",
		)
	}
	return verbosity, report, nil
}

func decodeInclude(
	values []string,
) (protocolcore.TranslationReport, error) {
	if len(values) == 0 {
		return protocolcore.TranslationReport{}, nil
	}
	seen := make(map[string]struct{}, len(values))
	report := protocolcore.TranslationReport{}
	for index, value := range values {
		if _, duplicate := seen[value]; duplicate {
			return protocolcore.TranslationReport{}, invalidClient(
				fmt.Sprintf("$.include[%d]", index),
				errors.New("Responses include value is duplicated"),
			)
		}
		seen[value] = struct{}{}
		if value != "reasoning.encrypted_content" {
			return protocolcore.TranslationReport{}, invalidClient(
				fmt.Sprintf("$.include[%d]", index),
				errors.New("Responses include value is unsupported"),
			)
		}
		report = report.Merge(notice(
			protocolcore.NoticeReasoningIncludeNotForwarded,
			fmt.Sprintf("$.include[%d]", index),
		))
	}
	return report, nil
}

func notice(
	code protocolcore.NoticeCode,
	path string,
) protocolcore.TranslationReport {
	return protocolcore.NewTranslationReport(protocolcore.TranslationNotice{
		Code: code,
		Path: path,
	})
}

func decodeClientWire(raw []byte, destination any, compatible bool) error {
	if !compatible {
		return decodeStrict(raw, destination)
	}
	if err := rejectDuplicateNames(raw); err != nil {
		return err
	}
	return json.Unmarshal(raw, destination)
}

func singleton(message protocolcore.Message) []protocolcore.Message {
	if len(message.Blocks) == 0 {
		return nil
	}
	return []protocolcore.Message{message}
}

func validateBoundedString(value string, maxBytes int, allowEmpty bool) error {
	if !allowEmpty && value == "" {
		return errors.New("value is empty")
	}
	if !utf8.ValidString(value) {
		return errors.New("value is not valid UTF-8")
	}
	if len(value) > maxBytes {
		return errors.New("value exceeds its byte limit")
	}
	if strings.TrimSpace(value) != value {
		return errors.New("value has surrounding whitespace")
	}
	return nil
}
