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

const maxCodexRolloutLineBytes = 64 << 20

// CodexIdentityResolver reads Codex's append-only local rollout authority.
// Association is exact: a Responses client turn_id is preferred; when Codex
// omits it on an initial request, provider output item IDs shared by the
// network response and local rollout form the join. Any supplied root session,
// Agent thread, and workspace identities must agree with the rollout. It never
// falls back to timestamps, titles, or prompt text.
type CodexIdentityResolver struct {
	sessionsRoot string
}

func DefaultCodexSessionsRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("resolve Codex sessions root: user home is unavailable")
	}
	return filepath.Join(home, ".codex", "sessions"), nil
}

func NewCodexIdentityResolver(sessionsRoot string) (*CodexIdentityResolver, error) {
	if sessionsRoot == "" || !filepath.IsAbs(sessionsRoot) ||
		filepath.Clean(sessionsRoot) != sessionsRoot {
		return nil, errors.New("Codex sessions root is invalid")
	}
	return &CodexIdentityResolver{sessionsRoot: sessionsRoot}, nil
}

func (resolver *CodexIdentityResolver) ResolveBatch(
	ctx context.Context,
	workspace string,
	lookups []ClientIdentityLookup,
) (map[string]ClientIdentity, error) {
	if ctx == nil || resolver == nil || resolver.sessionsRoot == "" ||
		workspace == "" || !filepath.IsAbs(workspace) || len(lookups) == 0 {
		return nil, errors.New("Codex identity lookup is invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	workspace = canonicalCodexWorkspace(workspace)
	targets, err := codexLookupTargets(lookups)
	if err != nil {
		return nil, err
	}
	paths, err := codexRolloutPaths(resolver.sessionsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]ClientIdentity{}, nil
	}
	if err != nil {
		return nil, err
	}

	rollouts := make([]codexRollout, 0, len(paths))
	for _, path := range paths {
		rollout, found, readErr := readCodexRollout(ctx, path, workspace, targets)
		if readErr != nil {
			// Codex owns this append-only directory and its schema evolves across
			// client releases. One legacy, partially written, or future rollout is
			// not authority for unrelated sessions and must not poison every exact
			// identity lookup. Context cancellation remains fatal.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if found {
			rollouts = append(rollouts, rollout)
		}
	}

	resolved := make(map[string]ClientIdentity, len(lookups))
	for _, lookup := range lookups {
		target, targetFound := targets.byResponseID[lookup.ProviderResponseID]
		if !targetFound {
			continue
		}
		var matched *codexRollout
		matchedTurnID := ""
		for index := range rollouts {
			rollout := &rollouts[index]
			turnID, matches, matchErr := rollout.matchedTurn(target)
			if matchErr != nil {
				return nil, matchErr
			}
			if !matches {
				continue
			}
			if matched != nil && (!matched.sameThread(*rollout) || matchedTurnID != turnID) {
				return nil, errors.New("Codex turn identity is ambiguous")
			}
			matched = rollout
			matchedTurnID = turnID
		}
		if matched == nil {
			continue
		}
		identity, identityErr := matched.identity(target, matchedTurnID, lookup)
		if identityErr != nil {
			return nil, identityErr
		}
		resolved[lookup.ProviderResponseID] = identity
	}
	return resolved, nil
}

type codexLookupTarget struct {
	SessionID          string
	ThreadID           string
	TurnID             string
	PreviousResponseID string
	ResponseItemIDs    map[string]struct{}
	CallIDs            map[string]struct{}
}

type codexTargets struct {
	byResponseID map[string]codexLookupTarget
	turnIDs      map[string]struct{}
	itemIDs      map[string]struct{}
	callIDs      map[string]struct{}
}

func (targets codexTargets) mayMatch(meta codexSessionMeta) bool {
	for _, target := range targets.byResponseID {
		if target.SessionID != "" && target.SessionID != meta.SessionID {
			continue
		}
		if target.ThreadID != "" && target.ThreadID != meta.ThreadID {
			continue
		}
		return true
	}
	return false
}

func codexLookupTargets(lookups []ClientIdentityLookup) (codexTargets, error) {
	targets := codexTargets{
		byResponseID: make(map[string]codexLookupTarget, len(lookups)),
		turnIDs:      make(map[string]struct{}, len(lookups)),
		itemIDs:      make(map[string]struct{}, len(lookups)*2),
		callIDs:      make(map[string]struct{}, len(lookups)),
	}
	for _, lookup := range lookups {
		if !validText(lookup.ProviderResponseID, false) || lookup.ObservedAt.IsZero() ||
			protocolcore.ValidateProtocolEvidence(lookup.ProtocolEvidence) != nil ||
			protocolcore.ValidateProtocolEvidence(lookup.ResponseProtocolEvidence) != nil {
			return codexTargets{}, errors.New("Codex identity lookup is invalid")
		}
		target := codexLookupTarget{
			ResponseItemIDs: make(map[string]struct{}),
			CallIDs:         make(map[string]struct{}),
		}
		for _, value := range lookup.ProtocolEvidence {
			switch value.Name {
			case "openai_responses.session_id":
				target.SessionID = value.Value
			case "openai_responses.thread_id":
				target.ThreadID = value.Value
			case "openai_responses.turn_id":
				target.TurnID = value.Value
			case "openai_responses.previous_response_id":
				target.PreviousResponseID = value.Value
			}
		}
		for _, value := range lookup.ResponseProtocolEvidence {
			switch {
			case strings.HasPrefix(value.Name, "openai_responses.output.") &&
				(strings.HasSuffix(value.Name, ".metadata.turn_id") ||
					strings.HasSuffix(value.Name, ".internal_chat_message_metadata_passthrough.turn_id")):
				if target.TurnID != "" && target.TurnID != value.Value {
					return codexTargets{}, errors.New("Codex response contains conflicting turn identities")
				}
				target.TurnID = value.Value
			case strings.HasPrefix(value.Name, "openai_responses.output.") &&
				strings.HasSuffix(value.Name, ".id"):
				target.ResponseItemIDs[value.Value] = struct{}{}
			case strings.HasPrefix(value.Name, "openai_responses.output.") &&
				strings.HasSuffix(value.Name, ".call_id"):
				target.CallIDs[value.Value] = struct{}{}
			}
		}
		// A root session or Agent thread alone would merge multiple requests and
		// is therefore insufficient. At least one exact per-request identifier
		// must be present on either side of the exchange.
		if target.TurnID == "" && len(target.ResponseItemIDs) == 0 && len(target.CallIDs) == 0 {
			continue
		}
		if previous, duplicate := targets.byResponseID[lookup.ProviderResponseID]; duplicate {
			if !sameCodexLookupTarget(previous, target) {
				return codexTargets{}, errors.New("Codex identity lookup duplicated one provider response")
			}
			continue
		}
		targets.byResponseID[lookup.ProviderResponseID] = target
		if target.TurnID != "" {
			targets.turnIDs[target.TurnID] = struct{}{}
		}
		for value := range target.ResponseItemIDs {
			targets.itemIDs[value] = struct{}{}
		}
		for value := range target.CallIDs {
			targets.callIDs[value] = struct{}{}
		}
	}
	return targets, nil
}

func sameCodexLookupTarget(left, right codexLookupTarget) bool {
	return left.SessionID == right.SessionID &&
		left.ThreadID == right.ThreadID &&
		left.TurnID == right.TurnID &&
		left.PreviousResponseID == right.PreviousResponseID &&
		mapSetsEqual(left.ResponseItemIDs, right.ResponseItemIDs) &&
		mapSetsEqual(left.CallIDs, right.CallIDs)
}

func mapSetsEqual(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for value := range left {
		if _, found := right[value]; !found {
			return false
		}
	}
	return true
}

func codexRolloutPaths(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" ||
			!strings.HasPrefix(entry.Name(), "rollout-") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list Codex rollouts: %w", err)
	}
	sort.Strings(paths)
	return paths, nil
}

type codexSessionMeta struct {
	SessionID      string          `json:"session_id"`
	ThreadID       string          `json:"id"`
	ForkedFromID   string          `json:"forked_from_id"`
	ParentThreadID string          `json:"parent_thread_id"`
	CWD            string          `json:"cwd"`
	Originator     string          `json:"originator"`
	CLIVersion     string          `json:"cli_version"`
	Source         json.RawMessage `json:"source"`
	ThreadSource   string          `json:"thread_source"`
	AgentNickname  string          `json:"agent_nickname"`
	AgentPath      string          `json:"agent_path"`
	AgentRole      string          `json:"agent_role"`
	ModelProvider  string          `json:"model_provider"`
	ContextWindow  struct {
		WindowID string `json:"window_id"`
	} `json:"context_window"`
}

type codexThreadSpawn struct {
	ParentThreadID string  `json:"parent_thread_id"`
	Depth          *int    `json:"depth"`
	AgentPath      string  `json:"agent_path"`
	AgentNickname  string  `json:"agent_nickname"`
	AgentRole      *string `json:"agent_role"`
}

type codexSource struct {
	Subagent *struct {
		ThreadSpawn codexThreadSpawn `json:"thread_spawn"`
	} `json:"subagent"`
}

type codexTurnEvidence struct {
	ResponseItemIDs  map[string]struct{}
	ReasoningItemIDs map[string]struct{}
	CallIDs          map[string]struct{}
	CompactionHashes map[string]struct{}
}

type codexWindowEvidence struct {
	ObservedAt       time.Time
	WindowID         string
	PreviousWindowID string
	FirstWindowID    string
}

type codexRollout struct {
	path       string
	createdAt  time.Time
	meta       codexSessionMeta
	spawn      *codexThreadSpawn
	sourceKind string
	turns      map[string]*codexTurnEvidence
	windows    []codexWindowEvidence
}

type codexRolloutEnvelope struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

func readCodexRollout(
	ctx context.Context,
	path string,
	workspace string,
	targets codexTargets,
) (codexRollout, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return codexRollout{}, false, fmt.Errorf("open Codex rollout: %w", err)
	}
	defer file.Close()
	rollout := codexRollout{path: path, turns: make(map[string]*codexTurnEvidence)}
	metaFound := false
	currentTurnID := ""
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 256<<10), maxCodexRolloutLineBytes)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return codexRollout{}, false, err
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var envelope codexRolloutEnvelope
		if err := json.Unmarshal(line, &envelope); err != nil {
			// Codex appends concurrently; an incomplete tail is not evidence.
			continue
		}
		switch envelope.Type {
		case "session_meta":
			if metaFound {
				// Forked rollouts may contain copied parent history, including its
				// session_meta. The first record is the authority for this file.
				continue
			}
			if err := json.Unmarshal(envelope.Payload, &rollout.meta); err != nil {
				continue
			}
			// Codex 0.118 and earlier used payload.id as the root session ID.
			// Current releases additionally expose session_id and use id as the
			// Agent thread ID. For a legacy root rollout those identities are the
			// same; preserving the alias keeps old evidence resumable without
			// inventing a child Agent relationship.
			if rollout.meta.SessionID == "" && rollout.meta.ThreadID != "" {
				rollout.meta.SessionID = rollout.meta.ThreadID
			}
			rollout.createdAt = envelope.Timestamp
			if rollout.meta.SessionID == "" || rollout.meta.ThreadID == "" ||
				rollout.meta.CWD == "" || !filepath.IsAbs(rollout.meta.CWD) {
				return codexRollout{}, false, errors.New("Codex rollout session metadata is invalid")
			}
			rollout.decodeSource()
			metaFound = true
			// session_meta is the first authority in a rollout. Reject unrelated
			// workspaces and exact session/thread mismatches before scanning what
			// can be hundreds of MiB of transcript payloads.
			if canonicalCodexWorkspace(rollout.meta.CWD) != workspace ||
				!targets.mayMatch(rollout.meta) {
				return codexRollout{}, false, nil
			}
		case "event_msg", "turn_context":
			if rollout.copiedBeforeCreation(envelope.Timestamp) {
				continue
			}
			var event struct {
				Type     string `json:"type"`
				TurnID   string `json:"turn_id"`
				ThreadID string `json:"thread_id"`
				CompHash string `json:"comp_hash"`
				Item     struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					TurnID   string `json:"turn_id"`
					ThreadID string `json:"thread_id"`
				} `json:"item"`
			}
			if err := json.Unmarshal(envelope.Payload, &event); err != nil {
				continue
			}
			if event.TurnID != "" && (envelope.Type == "turn_context" || event.Type == "task_started") {
				currentTurnID = event.TurnID
				evidence := rollout.ensureTurn(targets.turnIDs, event.TurnID)
				if evidence != nil && event.CompHash != "" {
					evidence.CompactionHashes[event.CompHash] = struct{}{}
				}
			}
			itemTurnID := event.Item.TurnID
			if itemTurnID == "" {
				itemTurnID = event.TurnID
			}
			if event.Item.ID != "" {
				rollout.addTurnItem(targets, itemTurnID, event.Item.Type, event.Item.ID, "")
			}
		case "response_item":
			if rollout.copiedBeforeCreation(envelope.Timestamp) {
				continue
			}
			var item struct {
				Type             string `json:"type"`
				ID               string `json:"id"`
				CallID           string `json:"call_id"`
				InternalMetadata struct {
					TurnID string `json:"turn_id"`
				} `json:"internal_chat_message_metadata_passthrough"`
				Metadata struct {
					TurnID string `json:"turn_id"`
				} `json:"metadata"`
			}
			if err := json.Unmarshal(envelope.Payload, &item); err != nil {
				continue
			}
			turnID := item.InternalMetadata.TurnID
			if turnID == "" {
				turnID = item.Metadata.TurnID
			}
			if turnID == "" {
				turnID = currentTurnID
			}
			rollout.addTurnItem(targets, turnID, item.Type, item.ID, item.CallID)
		case "compacted":
			if rollout.copiedBeforeCreation(envelope.Timestamp) {
				continue
			}
			var window struct {
				WindowID         string `json:"window_id"`
				PreviousWindowID string `json:"previous_window_id"`
				FirstWindowID    string `json:"first_window_id"`
			}
			if json.Unmarshal(envelope.Payload, &window) != nil {
				continue
			}
			if window.WindowID != "" || window.PreviousWindowID != "" || window.FirstWindowID != "" {
				rollout.windows = append(rollout.windows, codexWindowEvidence{
					ObservedAt: envelope.Timestamp, WindowID: window.WindowID,
					PreviousWindowID: window.PreviousWindowID, FirstWindowID: window.FirstWindowID,
				})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return codexRollout{}, false, fmt.Errorf("read Codex rollout: %w", err)
	}
	if !metaFound {
		return codexRollout{}, false, nil
	}
	return rollout, true, nil
}

func canonicalCodexWorkspace(path string) string {
	clean := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(clean)
	if err == nil && resolved != "" {
		return filepath.Clean(resolved)
	}
	return clean
}

// A forked Codex rollout starts with the child session_meta and then copies
// parent history verbatim. Those copied envelopes keep their original earlier
// timestamps. This exact append-only boundary prevents a parent turn_id from
// appearing to belong to both the parent and child thread.
func (rollout codexRollout) copiedBeforeCreation(observedAt time.Time) bool {
	return rollout.spawn != nil && !rollout.createdAt.IsZero() &&
		!observedAt.IsZero() && !observedAt.After(rollout.createdAt)
}

func (rollout *codexRollout) ensureTurn(
	targetTurnIDs map[string]struct{},
	turnID string,
) *codexTurnEvidence {
	if _, wanted := targetTurnIDs[turnID]; !wanted {
		return nil
	}
	return rollout.ensureTurnEvidence(turnID)
}

func (rollout *codexRollout) ensureTurnEvidence(
	turnID string,
) *codexTurnEvidence {
	if turnID == "" {
		return nil
	}
	evidence := rollout.turns[turnID]
	if evidence == nil {
		evidence = &codexTurnEvidence{
			ResponseItemIDs:  make(map[string]struct{}),
			ReasoningItemIDs: make(map[string]struct{}),
			CallIDs:          make(map[string]struct{}),
			CompactionHashes: make(map[string]struct{}),
		}
		rollout.turns[turnID] = evidence
	}
	return evidence
}

func (rollout *codexRollout) decodeSource() {
	if len(rollout.meta.Source) == 0 {
		return
	}
	var scalar string
	if json.Unmarshal(rollout.meta.Source, &scalar) == nil {
		rollout.sourceKind = scalar
		return
	}
	var source codexSource
	if json.Unmarshal(rollout.meta.Source, &source) != nil || source.Subagent == nil {
		return
	}
	spawn := source.Subagent.ThreadSpawn
	rollout.spawn = &spawn
	rollout.sourceKind = "subagent"
	if rollout.meta.ParentThreadID == "" {
		rollout.meta.ParentThreadID = spawn.ParentThreadID
	}
	if rollout.meta.AgentPath == "" {
		rollout.meta.AgentPath = spawn.AgentPath
	}
	if rollout.meta.AgentNickname == "" {
		rollout.meta.AgentNickname = spawn.AgentNickname
	}
	if rollout.meta.AgentRole == "" && spawn.AgentRole != nil {
		rollout.meta.AgentRole = *spawn.AgentRole
	}
}

func (rollout *codexRollout) addTurnItem(
	targets codexTargets,
	turnID string,
	kind string,
	itemID string,
	callID string,
) {
	_, wantedTurn := targets.turnIDs[turnID]
	_, wantedItem := targets.itemIDs[itemID]
	_, wantedCall := targets.callIDs[callID]
	if !wantedTurn && !wantedItem && !wantedCall {
		return
	}
	evidence := rollout.ensureTurnEvidence(turnID)
	if evidence == nil {
		return
	}
	if itemID != "" {
		evidence.ResponseItemIDs[itemID] = struct{}{}
		if kind == "reasoning" || strings.HasPrefix(itemID, "rs_") {
			evidence.ReasoningItemIDs[itemID] = struct{}{}
		}
	}
	if callID != "" {
		evidence.CallIDs[callID] = struct{}{}
	}
}

func (rollout codexRollout) matchedTurn(
	target codexLookupTarget,
) (string, bool, error) {
	if target.SessionID != "" && target.SessionID != rollout.meta.SessionID {
		return "", false, nil
	}
	if target.ThreadID != "" && target.ThreadID != rollout.meta.ThreadID {
		return "", false, nil
	}
	if target.TurnID != "" {
		return target.TurnID, rollout.turns[target.TurnID] != nil, nil
	}
	matchedTurnID := ""
	for turnID, evidence := range rollout.turns {
		if !mapsIntersect(evidence.ResponseItemIDs, target.ResponseItemIDs) &&
			!mapsIntersect(evidence.CallIDs, target.CallIDs) {
			continue
		}
		if matchedTurnID != "" && matchedTurnID != turnID {
			return "", false, errors.New("Codex response item identity is ambiguous")
		}
		matchedTurnID = turnID
	}
	return matchedTurnID, matchedTurnID != "", nil
}

func mapsIntersect(left, right map[string]struct{}) bool {
	for value := range left {
		if _, found := right[value]; found {
			return true
		}
	}
	return false
}

func (rollout codexRollout) sameThread(other codexRollout) bool {
	return rollout.meta.SessionID == other.meta.SessionID &&
		rollout.meta.ThreadID == other.meta.ThreadID
}

func (rollout codexRollout) identity(
	target codexLookupTarget,
	turnID string,
	lookup ClientIdentityLookup,
) (ClientIdentity, error) {
	isSubagent := rollout.spawn != nil || rollout.meta.ThreadSource == "subagent" ||
		rollout.sourceKind == "subagent"
	actorID := ""
	actorLabel := ""
	actorType := ""
	if isSubagent {
		actorID = rollout.meta.ThreadID
		actorLabel = rollout.meta.AgentNickname
		if actorLabel == "" {
			actorLabel = actorLeaf(rollout.meta.AgentPath)
		}
		if actorLabel == "" {
			actorLabel = "subagent"
		}
		actorType = rollout.meta.AgentRole
		if actorType == "" {
			actorType = "subagent"
		}
	}
	identity := ClientIdentity{
		Client: "codex", SessionID: rollout.meta.SessionID, SessionResumable: true,
		ActorID: actorID, ActorLabel: actorLabel, ActorType: actorType,
		ActorIsSubagent: isSubagent, ProviderResponseID: lookup.ProviderResponseID,
		Source: "client_local_state", Confidence: "exact",
		ObservedAt: lookup.ObservedAt.UTC().Truncate(time.Millisecond),
	}
	protocolIDs := map[string]map[string]struct{}{}
	addProtocolID := func(name, value string) {
		if value == "" {
			return
		}
		values := protocolIDs[name]
		if values == nil {
			values = map[string]struct{}{}
			protocolIDs[name] = values
		}
		values[value] = struct{}{}
	}
	addProtocolID("codex.session_id", rollout.meta.SessionID)
	addProtocolID("codex.thread_id", rollout.meta.ThreadID)
	addProtocolID("codex.turn_id", turnID)
	addProtocolID("codex.parent_thread_id", rollout.meta.ParentThreadID)
	addProtocolID("codex.forked_from_thread_id", rollout.meta.ForkedFromID)
	addProtocolID("codex.context_window_id", rollout.meta.ContextWindow.WindowID)
	addProtocolID("openai_responses.previous_response_id", target.PreviousResponseID)
	for _, window := range rollout.windows {
		if !window.ObservedAt.IsZero() && window.ObservedAt.After(lookup.ObservedAt) {
			continue
		}
		addProtocolID("codex.compaction_window_id", window.WindowID)
		addProtocolID("codex.previous_window_id", window.PreviousWindowID)
		addProtocolID("codex.first_window_id", window.FirstWindowID)
	}
	turn := rollout.turns[turnID]
	for value := range turn.ResponseItemIDs {
		addProtocolID("codex.response_item_id", value)
	}
	for value := range turn.ReasoningItemIDs {
		addProtocolID("codex.reasoning_item_id", value)
	}
	for value := range turn.CallIDs {
		addProtocolID("codex.call_id", value)
	}
	for value := range turn.CompactionHashes {
		addProtocolID("codex.compaction_hash", value)
	}
	for name, values := range protocolIDs {
		for value := range values {
			identity.ProtocolIDs = append(identity.ProtocolIDs, ClientEvidenceValue{Name: name, Value: value})
		}
	}
	attributes := map[string]string{
		"codex.agent_nickname": rollout.meta.AgentNickname,
		"codex.agent_path":     rollout.meta.AgentPath,
		"codex.agent_role":     rollout.meta.AgentRole,
		"codex.cli_version":    rollout.meta.CLIVersion,
		"codex.model_provider": rollout.meta.ModelProvider,
		"codex.originator":     rollout.meta.Originator,
		"codex.source":         rollout.sourceKind,
		"codex.thread_source":  rollout.meta.ThreadSource,
	}
	if rollout.spawn != nil && rollout.spawn.Depth != nil {
		attributes["codex.spawn_depth"] = strconv.Itoa(*rollout.spawn.Depth)
	}
	for name, value := range attributes {
		if value != "" {
			identity.Attributes = append(identity.Attributes, ClientEvidenceValue{Name: name, Value: value})
		}
	}
	sort.Slice(identity.ProtocolIDs, func(i, j int) bool {
		if identity.ProtocolIDs[i].Name == identity.ProtocolIDs[j].Name {
			return identity.ProtocolIDs[i].Value < identity.ProtocolIDs[j].Value
		}
		return identity.ProtocolIDs[i].Name < identity.ProtocolIDs[j].Name
	})
	sort.Slice(identity.Attributes, func(i, j int) bool {
		return identity.Attributes[i].Name < identity.Attributes[j].Name
	})
	if err := identity.Validate(); err != nil {
		return ClientIdentity{}, err
	}
	return identity, nil
}
