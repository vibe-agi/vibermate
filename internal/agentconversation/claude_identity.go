package agentconversation

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

var ErrClientIdentityNotFound = errors.New("client Agent identity was not found")

const maxClaudeTranscriptLineBytes = 64 << 20

// ClaudeIdentityResolver reads Claude Code's own local transcript authority.
// It joins an Exchange only by the exact provider message ID observed by both
// ViberMate and Claude; directory names, timestamps, and prompt text are never
// association evidence.
type ClaudeIdentityResolver struct {
	projectsRoot string
}

func DefaultClaudeProjectsRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("resolve Claude projects root: user home is unavailable")
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

func NewClaudeIdentityResolver(projectsRoot string) (*ClaudeIdentityResolver, error) {
	if projectsRoot == "" || !filepath.IsAbs(projectsRoot) ||
		filepath.Clean(projectsRoot) != projectsRoot {
		return nil, errors.New("Claude projects root is invalid")
	}
	return &ClaudeIdentityResolver{projectsRoot: projectsRoot}, nil
}

func (resolver *ClaudeIdentityResolver) Resolve(
	ctx context.Context,
	workspace string,
	providerResponseID string,
	observedAt time.Time,
) (ClientIdentity, error) {
	resolved, err := resolver.ResolveBatch(ctx, workspace, []ClientIdentityLookup{{
		ProviderResponseID: providerResponseID,
		ObservedAt:         observedAt,
	}})
	if err != nil {
		return ClientIdentity{}, err
	}
	identity, found := resolved[providerResponseID]
	if !found {
		return ClientIdentity{}, ErrClientIdentityNotFound
	}
	return identity.Clone(), nil
}

// ResolveBatch scans the workspace transcript set once and returns only exact
// provider-message joins. Missing IDs are absent from the result rather than
// being inferred from nearby timestamps or content.
func (resolver *ClaudeIdentityResolver) ResolveBatch(
	ctx context.Context,
	workspace string,
	lookups []ClientIdentityLookup,
) (map[string]ClientIdentity, error) {
	if ctx == nil || resolver == nil || resolver.projectsRoot == "" ||
		workspace == "" || !filepath.IsAbs(workspace) || len(lookups) == 0 {
		return nil, errors.New("Claude identity lookup is invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	targets := make(map[string]time.Time, len(lookups))
	for _, lookup := range lookups {
		if !validText(lookup.ProviderResponseID, false) || lookup.ObservedAt.IsZero() ||
			protocolcore.ValidateProtocolEvidence(lookup.ProtocolEvidence) != nil ||
			protocolcore.ValidateProtocolEvidence(lookup.ResponseProtocolEvidence) != nil {
			return nil, errors.New("Claude identity lookup is invalid")
		}
		observed := lookup.ObservedAt.UTC().Truncate(time.Millisecond)
		if previous, found := targets[lookup.ProviderResponseID]; found && !previous.Equal(observed) {
			return nil, errors.New("Claude identity lookup duplicated one provider message")
		}
		targets[lookup.ProviderResponseID] = observed
	}
	projectRoot := filepath.Join(resolver.projectsRoot, encodeClaudeWorkspace(workspace))
	paths, err := claudeTranscriptPaths(projectRoot)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]ClientIdentity{}, nil
	}
	if err != nil {
		return nil, err
	}
	matches := make(map[string]*claudeTranscriptMatch)
	for _, path := range paths {
		candidates, readErr := findClaudeMessages(ctx, path, targets)
		if readErr != nil {
			return nil, readErr
		}
		for providerResponseID, candidate := range candidates {
			match := matches[providerResponseID]
			if match != nil && !match.sameActor(candidate) {
				return nil, errors.New("Claude message identity is ambiguous")
			}
			if match == nil {
				owned := candidate
				matches[providerResponseID] = &owned
				continue
			}
			match.absorb(candidate)
		}
	}
	resolved := make(map[string]ClientIdentity, len(matches))
	for providerResponseID, match := range matches {
		identity, identityErr := match.identity(
			providerResponseID,
			targets[providerResponseID],
		)
		if identityErr != nil {
			return nil, identityErr
		}
		resolved[providerResponseID] = identity.Clone()
	}
	return resolved, nil
}

func encodeClaudeWorkspace(workspace string) string {
	cleaned := filepath.Clean(workspace)
	return strings.NewReplacer("/", "-", `\`, "-").Replace(cleaned)
}

func claudeTranscriptPaths(projectRoot string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(projectRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Ext(entry.Name()) != ".jsonl" {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list Claude transcripts: %w", err)
	}
	sort.Strings(paths)
	return paths, nil
}

type claudeTranscriptEnvelope struct {
	SessionID        string `json:"sessionId"`
	LegacySessionID  string `json:"session_id"`
	AgentID          string `json:"agentId"`
	RequestID        string `json:"requestId"`
	PromptID         string `json:"promptId"`
	UUID             string `json:"uuid"`
	ParentUUID       string `json:"parentUuid"`
	SourceToolUUID   string `json:"sourceToolAssistantUUID"`
	IsSidechain      bool   `json:"isSidechain"`
	AttributionAgent string `json:"attributionAgent"`
	AttributionSkill string `json:"attributionSkill"`
	Attachment       struct {
		ToolUseID string `json:"toolUseID"`
	} `json:"attachment"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
	Message       json.RawMessage `json:"message"`
}

type claudeMessageEnvelope struct {
	ID      string          `json:"id"`
	Content json.RawMessage `json:"content"`
}

type claudeMessageContent struct {
	ID        string `json:"id"`
	ToolUseID string `json:"tool_use_id"`
}

type claudeToolUseResult struct {
	AgentID string `json:"agentId"`
}

type claudeTranscriptMatch struct {
	path             string
	initialized      bool
	sessionID        string
	legacySessionID  string
	agentID          string
	requestIDs       map[string]struct{}
	promptIDs        map[string]struct{}
	eventUUIDs       map[string]struct{}
	parentUUIDs      map[string]struct{}
	sourceToolUUIDs  map[string]struct{}
	sourceMessageIDs map[string]struct{}
	contentBlockIDs  map[string]struct{}
	toolUseIDs       map[string]struct{}
	spawnedAgentIDs  map[string]struct{}
	isSidechain      bool
	attributionAgent string
	attributionSkill string
	metadata         map[string]string
}

type claudeTranscriptEvent struct {
	sessionID           string
	agentID             string
	requestID           string
	promptID            string
	sourceToolUUID      string
	attachmentToolID    string
	spawnedAgentID      string
	messageID           string
	contentBlockIDs     []string
	contentBlockToolIDs []string
}

func findClaudeMessages(
	ctx context.Context,
	path string,
	targets map[string]time.Time,
) (map[string]claudeTranscriptMatch, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Claude transcript: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 256<<10), maxClaudeTranscriptLineBytes)
	matches := make(map[string]*claudeTranscriptMatch)
	events := make(map[string]claudeTranscriptEvent)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) != 0 {
			var envelope claudeTranscriptEnvelope
			if err := json.Unmarshal(line, &envelope); err != nil {
				// Claude owns and appends this file concurrently. A partial tail or
				// unrelated damaged record cannot be association evidence, but it
				// must not make the runtime's existing Conversation index unreadable.
				continue
			}
			var message claudeMessageEnvelope
			if len(envelope.Message) != 0 {
				_ = json.Unmarshal(envelope.Message, &message)
			}
			if envelope.UUID != "" {
				events[envelope.UUID] = newClaudeTranscriptEvent(envelope, message)
			}
			if message.ID != "" {
				if _, wanted := targets[message.ID]; wanted {
					match := matches[message.ID]
					if match == nil {
						match = &claudeTranscriptMatch{
							path: path, requestIDs: map[string]struct{}{}, promptIDs: map[string]struct{}{},
							eventUUIDs: map[string]struct{}{}, parentUUIDs: map[string]struct{}{},
							sourceToolUUIDs: map[string]struct{}{}, sourceMessageIDs: map[string]struct{}{},
							contentBlockIDs: map[string]struct{}{},
							toolUseIDs:      map[string]struct{}{}, spawnedAgentIDs: map[string]struct{}{},
							metadata: map[string]string{},
						}
						matches[message.ID] = match
					}
					if err := match.merge(envelope); err != nil {
						return nil, err
					}
					match.mergeMessageContent(message.Content)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Claude transcript: %w", err)
	}
	for _, match := range matches {
		for parentUUID := range match.parentUUIDs {
			parent, found := events[parentUUID]
			if !found {
				continue
			}
			if err := match.mergeLinked(parent, false); err != nil {
				return nil, err
			}
			if parent.sourceToolUUID == "" {
				continue
			}
			source, found := events[parent.sourceToolUUID]
			if !found {
				continue
			}
			if err := match.mergeLinked(source, true); err != nil {
				return nil, err
			}
		}
	}
	result := make(map[string]claudeTranscriptMatch, len(matches))
	for providerResponseID, match := range matches {
		if err := match.mergeMetadata(); err != nil {
			return nil, err
		}
		result[providerResponseID] = *match
	}
	return result, nil
}

func (match *claudeTranscriptMatch) mergeLinked(
	event claudeTranscriptEvent,
	isSourceTool bool,
) error {
	if event.sessionID != "" && event.sessionID != match.sessionID {
		return errors.New("Claude linked event crossed session authority")
	}
	if event.agentID != "" && match.agentID != "" && event.agentID != match.agentID {
		return errors.New("Claude linked event crossed Agent authority")
	}
	addNonEmpty(match.requestIDs, event.requestID)
	addNonEmpty(match.promptIDs, event.promptID)
	addNonEmpty(match.sourceToolUUIDs, event.sourceToolUUID)
	addNonEmpty(match.toolUseIDs, event.attachmentToolID)
	addNonEmpty(match.spawnedAgentIDs, event.spawnedAgentID)
	if isSourceTool {
		addNonEmpty(match.sourceMessageIDs, event.messageID)
	}
	for _, value := range event.contentBlockIDs {
		addNonEmpty(match.contentBlockIDs, value)
	}
	for _, value := range event.contentBlockToolIDs {
		addNonEmpty(match.toolUseIDs, value)
	}
	return nil
}

func (match *claudeTranscriptMatch) mergeMessageContent(encoded json.RawMessage) {
	contentBlockIDs, toolUseIDs := claudeContentIDs(encoded)
	for _, value := range contentBlockIDs {
		addNonEmpty(match.contentBlockIDs, value)
	}
	for _, value := range toolUseIDs {
		addNonEmpty(match.toolUseIDs, value)
	}
}

func newClaudeTranscriptEvent(
	envelope claudeTranscriptEnvelope,
	message claudeMessageEnvelope,
) claudeTranscriptEvent {
	sessionID := envelope.SessionID
	if sessionID == "" {
		sessionID = envelope.LegacySessionID
	}
	contentBlockIDs, contentBlockToolIDs := claudeContentIDs(message.Content)
	return claudeTranscriptEvent{
		sessionID:           sessionID,
		agentID:             envelope.AgentID,
		requestID:           envelope.RequestID,
		promptID:            envelope.PromptID,
		sourceToolUUID:      envelope.SourceToolUUID,
		attachmentToolID:    envelope.Attachment.ToolUseID,
		spawnedAgentID:      claudeSpawnedAgentID(envelope.ToolUseResult),
		messageID:           message.ID,
		contentBlockIDs:     contentBlockIDs,
		contentBlockToolIDs: contentBlockToolIDs,
	}
}

func claudeContentIDs(encoded json.RawMessage) ([]string, []string) {
	var contents []claudeMessageContent
	if json.Unmarshal(encoded, &contents) != nil {
		return nil, nil
	}
	blockIDs := make([]string, 0, len(contents))
	toolUseIDs := make([]string, 0, len(contents))
	for _, content := range contents {
		if content.ID != "" {
			blockIDs = append(blockIDs, content.ID)
		}
		if content.ToolUseID != "" {
			toolUseIDs = append(toolUseIDs, content.ToolUseID)
		}
	}
	return blockIDs, toolUseIDs
}

func (match *claudeTranscriptMatch) merge(envelope claudeTranscriptEnvelope) error {
	sessionID := envelope.SessionID
	if sessionID == "" {
		sessionID = envelope.LegacySessionID
	}
	if match.initialized && (match.sessionID != sessionID ||
		match.agentID != envelope.AgentID ||
		match.isSidechain != envelope.IsSidechain ||
		match.attributionAgent != envelope.AttributionAgent ||
		match.attributionSkill != envelope.AttributionSkill) {
		return errors.New("Claude message identity changed inside one transcript")
	}
	match.initialized = true
	match.sessionID = sessionID
	match.legacySessionID = envelope.LegacySessionID
	match.agentID = envelope.AgentID
	match.isSidechain = envelope.IsSidechain
	match.attributionAgent = envelope.AttributionAgent
	match.attributionSkill = envelope.AttributionSkill
	addNonEmpty(match.requestIDs, envelope.RequestID)
	addNonEmpty(match.promptIDs, envelope.PromptID)
	addNonEmpty(match.eventUUIDs, envelope.UUID)
	addNonEmpty(match.parentUUIDs, envelope.ParentUUID)
	addNonEmpty(match.sourceToolUUIDs, envelope.SourceToolUUID)
	addNonEmpty(match.toolUseIDs, envelope.Attachment.ToolUseID)
	addNonEmpty(match.spawnedAgentIDs, claudeSpawnedAgentID(envelope.ToolUseResult))
	return nil
}

func (match *claudeTranscriptMatch) absorb(other claudeTranscriptMatch) {
	for value := range other.requestIDs {
		match.requestIDs[value] = struct{}{}
	}
	for value := range other.promptIDs {
		match.promptIDs[value] = struct{}{}
	}
	for value := range other.eventUUIDs {
		match.eventUUIDs[value] = struct{}{}
	}
	for value := range other.parentUUIDs {
		match.parentUUIDs[value] = struct{}{}
	}
	for value := range other.sourceToolUUIDs {
		match.sourceToolUUIDs[value] = struct{}{}
	}
	for value := range other.sourceMessageIDs {
		match.sourceMessageIDs[value] = struct{}{}
	}
	for value := range other.contentBlockIDs {
		match.contentBlockIDs[value] = struct{}{}
	}
	for value := range other.toolUseIDs {
		match.toolUseIDs[value] = struct{}{}
	}
	for value := range other.spawnedAgentIDs {
		match.spawnedAgentIDs[value] = struct{}{}
	}
	for name, value := range other.metadata {
		match.metadata[name] = value
	}
	if match.legacySessionID == "" {
		match.legacySessionID = other.legacySessionID
	}
}

func claudeSpawnedAgentID(encoded json.RawMessage) string {
	var result claudeToolUseResult
	if json.Unmarshal(encoded, &result) != nil {
		return ""
	}
	return result.AgentID
}

func (match *claudeTranscriptMatch) mergeMetadata() error {
	if match.agentID == "" {
		return nil
	}
	base := strings.TrimSuffix(match.path, ".jsonl")
	if encoded, err := os.ReadFile(base + ".meta.json"); err == nil {
		var metadata struct {
			AgentType     string `json:"agentType"`
			Description   string `json:"description"`
			ParentAgentID string `json:"parentAgentId"`
			SpawnDepth    *int   `json:"spawnDepth"`
		}
		if err := json.Unmarshal(encoded, &metadata); err != nil {
			return fmt.Errorf("decode Claude agent metadata: %w", err)
		}
		if match.attributionAgent == "" {
			match.attributionAgent = metadata.AgentType
		}
		if err := match.addMetadata("claude.description", metadata.Description); err != nil {
			return err
		}
		if err := match.addMetadata("claude.parent_agent_id", metadata.ParentAgentID); err != nil {
			return err
		}
		if metadata.SpawnDepth != nil {
			if err := match.addMetadata("claude.spawn_depth", strconv.Itoa(*metadata.SpawnDepth)); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read Claude agent metadata: %w", err)
	}
	if encoded, err := os.ReadFile(base + ".forked-skill.json"); err == nil {
		var metadata struct {
			AgentType     string `json:"agentType"`
			Description   string `json:"description"`
			ToolUseID     string `json:"toolUseId"`
			ParentAgentID string `json:"parentAgentId"`
			SpawnDepth    *int   `json:"spawnDepth"`
		}
		if err := json.Unmarshal(encoded, &metadata); err != nil {
			return fmt.Errorf("decode Claude forked-skill metadata: %w", err)
		}
		if match.attributionAgent == "" {
			match.attributionAgent = metadata.AgentType
		}
		for name, value := range map[string]string{
			"claude.description":     metadata.Description,
			"claude.tool_use_id":     metadata.ToolUseID,
			"claude.parent_agent_id": metadata.ParentAgentID,
		} {
			if err := match.addMetadata(name, value); err != nil {
				return err
			}
		}
		if metadata.SpawnDepth != nil {
			if err := match.addMetadata("claude.spawn_depth", strconv.Itoa(*metadata.SpawnDepth)); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read Claude forked-skill metadata: %w", err)
	}
	return nil
}

func (match *claudeTranscriptMatch) addMetadata(name, value string) error {
	if value == "" {
		return nil
	}
	if existing, ok := match.metadata[name]; ok && existing != value {
		return fmt.Errorf("Claude metadata %q changed", name)
	}
	match.metadata[name] = value
	return nil
}

func (match claudeTranscriptMatch) sameActor(other claudeTranscriptMatch) bool {
	return match.sessionID == other.sessionID && match.agentID == other.agentID &&
		match.isSidechain == other.isSidechain
}

func (match claudeTranscriptMatch) identity(
	providerResponseID string,
	observedAt time.Time,
) (ClientIdentity, error) {
	label := match.metadata["claude.description"]
	if label == "" {
		label = match.attributionSkill
	}
	if label == "" {
		label = match.attributionAgent
	}
	actorType := match.attributionAgent
	actorIsSubagent := match.isSidechain
	if match.agentID == "" {
		// Attribution labels without an actor ID are useful client attributes,
		// but cannot establish a stable Actor projection on their own.
		label = ""
		actorType = ""
		actorIsSubagent = false
	}
	identity := ClientIdentity{
		Client: "claude", SessionID: match.sessionID, SessionResumable: true,
		ActorID: match.agentID, ActorLabel: label, ActorType: actorType,
		ActorIsSubagent: actorIsSubagent, ProviderResponseID: providerResponseID,
		ProviderMessageID: providerResponseID, Source: "client_local_state",
		Confidence: "exact", ObservedAt: observedAt.UTC().Truncate(time.Millisecond),
	}
	// Keep the generic association fields and their Claude-native names. The
	// former lets shared Conversation code group actors; the latter keeps an
	// investigator's export lossless and makes the evidence self-describing.
	if match.sessionID != "" {
		identity.ProtocolIDs = append(identity.ProtocolIDs, ClientEvidenceValue{
			Name: "claude.session_id", Value: match.sessionID,
		})
	}
	if match.agentID != "" {
		identity.ProtocolIDs = append(identity.ProtocolIDs, ClientEvidenceValue{
			Name: "claude.agent_id", Value: match.agentID,
		})
	}
	appendSet := func(name string, values map[string]struct{}) {
		for value := range values {
			identity.ProtocolIDs = append(identity.ProtocolIDs, ClientEvidenceValue{Name: name, Value: value})
		}
	}
	appendSet("claude.event_uuid", match.eventUUIDs)
	appendSet("claude.parent_event_uuid", match.parentUUIDs)
	appendSet("claude.request_id", match.requestIDs)
	appendSet("claude.prompt_id", match.promptIDs)
	appendSet("claude.source_tool_assistant_uuid", match.sourceToolUUIDs)
	appendSet("claude.source_provider_message_id", match.sourceMessageIDs)
	appendSet("claude.content_block_id", match.contentBlockIDs)
	appendSet("claude.tool_use_id", match.toolUseIDs)
	appendSet("claude.spawned_agent_id", match.spawnedAgentIDs)
	if match.legacySessionID != "" && match.legacySessionID != match.sessionID {
		identity.ProtocolIDs = append(identity.ProtocolIDs, ClientEvidenceValue{Name: "claude.legacy_session_id", Value: match.legacySessionID})
	}
	for name, value := range match.metadata {
		if strings.HasSuffix(name, "_id") {
			identity.ProtocolIDs = append(identity.ProtocolIDs, ClientEvidenceValue{Name: name, Value: value})
		} else {
			identity.Attributes = append(identity.Attributes, ClientEvidenceValue{Name: name, Value: value})
		}
	}
	if match.attributionAgent != "" {
		identity.Attributes = append(identity.Attributes, ClientEvidenceValue{Name: "claude.agent_type", Value: match.attributionAgent})
	}
	if match.attributionSkill != "" {
		identity.Attributes = append(identity.Attributes, ClientEvidenceValue{Name: "claude.skill", Value: match.attributionSkill})
	}
	sort.Slice(identity.ProtocolIDs, func(i, j int) bool {
		if identity.ProtocolIDs[i].Name == identity.ProtocolIDs[j].Name {
			return identity.ProtocolIDs[i].Value < identity.ProtocolIDs[j].Value
		}
		return identity.ProtocolIDs[i].Name < identity.ProtocolIDs[j].Name
	})
	sort.Slice(identity.Attributes, func(i, j int) bool { return identity.Attributes[i].Name < identity.Attributes[j].Name })
	if err := identity.Validate(); err != nil {
		return ClientIdentity{}, err
	}
	return identity, nil
}

func addNonEmpty(target map[string]struct{}, value string) {
	if value != "" {
		target[value] = struct{}{}
	}
}
