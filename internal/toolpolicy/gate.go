// Package toolpolicy applies the frozen Environment tool policy at the last
// boundary before a provider tool call is released to the client.
package toolpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

const ReasonStrictPolicy = "tool_policy_strict"

// Gate preserves the user's explicit mode while keeping the durable human
// approval authority separate. Observe never waits. Review delegates only
// unproven groups. Strict rejects them without creating a question that no
// human answer is allowed to override.
type Gate struct {
	review exchange.ToolDecisionGate
}

func New(review exchange.ToolDecisionGate) (Gate, error) {
	if review == nil {
		return Gate{}, errors.New("tool review authority is unavailable")
	}
	return Gate{review: review}, nil
}

func (gate Gate) Decide(
	ctx context.Context,
	request exchange.ToolDecisionRequest,
) (exchange.ToolDecision, error) {
	if ctx == nil {
		return exchange.ToolDecision{}, errors.New("tool policy context is nil")
	}
	policyContext := request.Context()
	policy := policyContext.PolicySet()
	if err := policy.Validate(); err != nil {
		return exchange.ToolDecision{}, err
	}
	if policy.ToolMode == environment.ToolPolicyObserve {
		// Observe is deliberately free of filesystem and schema classification.
		// Evidence collection must never turn into a hidden admission dependency.
		return exchange.ToolDecision{Outcome: exchange.ToolDecisionApproved}, nil
	}
	safe := safeWorkspaceGroup(policyContext, request.ToolIntents())
	switch policy.ToolMode {
	case environment.ToolPolicyReview:
		if safe {
			return exchange.ToolDecision{Outcome: exchange.ToolDecisionApproved}, nil
		}
		return gate.review.Decide(ctx, request)
	case environment.ToolPolicyStrict:
		if safe {
			return exchange.ToolDecision{Outcome: exchange.ToolDecisionApproved}, nil
		}
		return exchange.ToolDecision{
			Outcome: exchange.ToolDecisionRejected, ReasonCode: ReasonStrictPolicy,
		}, nil
	default:
		return exchange.ToolDecision{}, errors.New("tool policy mode is unsupported")
	}
}

func safeWorkspaceGroup(
	decision exchange.ToolDecisionContext,
	intents []protocolcore.ToolIntent,
) bool {
	root, available := decision.WorkspaceRoot()
	if !decision.StructuredWorkspaceTools() || !available || len(intents) == 0 {
		return false
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	rootInfo, err := os.Stat(realRoot)
	if err != nil || !rootInfo.IsDir() {
		return false
	}
	definitions := make(map[string]protocolcore.ToolDefinition)
	for _, definition := range decision.Tools() {
		if _, duplicate := definitions[definition.Name]; duplicate {
			return false
		}
		definitions[definition.Name] = definition
	}
	// Namespaced/MCP tools have provider-specific semantics and never inherit
	// the built-in workspace capability.
	if len(decision.ToolNamespaces()) != 0 {
		for _, intent := range intents {
			if intent.Call.Namespace != "" {
				return false
			}
		}
	}
	for _, intent := range intents {
		if intent.Call.Namespace != "" ||
			intent.Call.EffectiveKind() != protocolcore.ToolKindFunction {
			return false
		}
		definition, exists := definitions[intent.Call.Name]
		if !exists || definition.EffectiveKind() != protocolcore.ToolKindFunction {
			return false
		}
		if !safeWorkspaceIntent(realRoot, definition, intent) {
			return false
		}
	}
	return true
}

type toolArguments map[string]json.RawMessage

func safeWorkspaceIntent(
	realRoot string,
	definition protocolcore.ToolDefinition,
	intent protocolcore.ToolIntent,
) bool {
	var arguments toolArguments
	if json.Unmarshal(intent.Call.Arguments.Bytes(), &arguments) != nil {
		return false
	}
	pathField := ""
	allowCreate := false
	optionalPath := false
	switch intent.Call.Name {
	case "Read", "Edit":
		pathField = "file_path"
	case "Write":
		pathField = "file_path"
		allowCreate = true
	case "NotebookEdit":
		pathField = "notebook_path"
	case "Glob", "Grep":
		pathField = "path"
		optionalPath = true
	default:
		return false
	}
	if !schemaDeclaresStringPath(definition, pathField, optionalPath) {
		return false
	}
	rawPath, present := arguments[pathField]
	if !present {
		if !optionalPath {
			return false
		}
	} else {
		var path string
		if json.Unmarshal(rawPath, &path) != nil || path == "" {
			return false
		}
		if !insideWorkspace(realRoot, path, allowCreate) {
			return false
		}
	}
	if intent.Call.Name == "Glob" && !safeRelativePattern(arguments["pattern"]) {
		return false
	}
	if intent.Call.Name == "Grep" {
		if rawGlob, exists := arguments["glob"]; exists &&
			!safeRelativePattern(rawGlob) {
			return false
		}
	}
	return true
}

func schemaDeclaresStringPath(
	definition protocolcore.ToolDefinition,
	field string,
	optional bool,
) bool {
	var schema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if json.Unmarshal(definition.InputSchema.Bytes(), &schema) != nil ||
		schema.Type != "object" || schema.Properties[field].Type != "string" {
		return false
	}
	if optional {
		return true
	}
	for _, required := range schema.Required {
		if required == field {
			return true
		}
	}
	return false
}

func insideWorkspace(realRoot, input string, allowCreate bool) bool {
	candidate := input
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(realRoot, candidate)
	}
	candidate = filepath.Clean(candidate)
	if !lexicallyInside(realRoot, candidate) {
		return false
	}
	realCandidate, err := filepath.EvalSymlinks(candidate)
	if err == nil {
		return lexicallyInside(realRoot, realCandidate)
	}
	if !allowCreate || !errors.Is(err, os.ErrNotExist) {
		return false
	}
	// Find the nearest existing path with Lstat rather than beginning at the
	// parent. A dangling leaf symlink does not exist to Stat/EvalSymlinks, but
	// an eventual Write would follow it and could create a file outside the
	// workspace.
	ancestor := candidate
	for {
		_, lstatErr := os.Lstat(ancestor)
		if lstatErr == nil {
			realAncestor, ancestorErr := filepath.EvalSymlinks(ancestor)
			return ancestorErr == nil && lexicallyInside(realRoot, realAncestor)
		}
		if !errors.Is(lstatErr, os.ErrNotExist) || ancestor == filepath.Dir(ancestor) {
			return false
		}
		ancestor = filepath.Dir(ancestor)
	}
}

func lexicallyInside(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func safeRelativePattern(raw json.RawMessage) bool {
	var pattern string
	if len(raw) == 0 || json.Unmarshal(raw, &pattern) != nil || pattern == "" ||
		filepath.IsAbs(pattern) || strings.Contains(pattern, "..") {
		return false
	}
	for _, segment := range strings.FieldsFunc(pattern, func(character rune) bool {
		return character == '/' || character == '\\'
	}) {
		if segment == ".." {
			return false
		}
	}
	return true
}
