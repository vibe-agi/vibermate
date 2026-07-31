package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchange"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

const (
	maxAgentLineBytes        = 8 << 20
	maxAgentLines            = 8192
	codexStateDirectoryName  = ".vibermate-codex"
	fixedCodexRequestedModel = "gpt-5.6-sol"
	codexHTTPFallbackMessage = "Falling back from WebSockets to HTTPS transport."
	codexHTTPProviderSelect  = `model_provider="vibermate-http"`
	codexHTTPProviderDefine  = `model_providers.vibermate-http={name="VibeMate Responses HTTP",base_url="https://api.openai.com/v1",env_key="CODEX_API_KEY",wire_api="responses",requires_openai_auth=false,supports_websockets=false}`
)

type codexResponsesTransport uint8

const (
	codexResponsesHTTPOnly codexResponsesTransport = iota + 1
	codexResponsesFallbackEvidence
)

type agentRun struct {
	command    *exec.Cmd
	done       chan struct{}
	outputDone chan struct{}
	stderr     *boundedBuffer
	marker     string
	clientID   acceptanceClientID

	mu         sync.Mutex
	waitErr    error
	lines      int
	deltas     int
	toolUses   int
	markerSeen bool
	markerTail string
	lastType   string
	failure    agentFailureEvidence
	readErr    error
	changed    chan struct{}

	configurationSeen bool
	configuredTools   []string

	threadID         agentThreadID
	agentMessages    int
	httpFallbackSeen bool
	turnStarted      bool
	turnCompleted    bool
}

type agentThreadID struct {
	value string
}

func parseAgentThreadID(value string) (agentThreadID, error) {
	if len(value) != 36 {
		return agentThreadID{}, errors.New("agent thread identity is invalid")
	}
	for index := range value {
		character := value[index]
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return agentThreadID{}, errors.New(
					"agent thread identity is invalid",
				)
			}
			continue
		}
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') &&
			(character < 'A' || character > 'F') {
			return agentThreadID{}, errors.New(
				"agent thread identity is invalid",
			)
		}
	}
	return agentThreadID{value: strings.ToLower(value)}, nil
}

func (identity agentThreadID) String() string {
	return identity.value
}

type agentFailureEvidence struct {
	reasonCode     exchange.ReasonCode
	providerStatus int
	providerField  exchange.ProviderField
	protocolReason protocolcore.Reason
	responseIssue  exchange.ProviderResponseIssue
	agentStatus    int
	category       string
	resultSubtype  string
}

func startAgent(
	config config,
	workingDirectory string,
	prompt string,
	tools string,
	marker string,
) (*agentRun, error) {
	client, err := selectedAcceptanceClient(config)
	if err != nil {
		return nil, err
	}
	if client.ID == acceptanceClientCodexCLI {
		if err := privateDirectory(
			filepath.Join(workingDirectory, codexStateDirectoryName),
		); err != nil {
			return nil, fmt.Errorf("prepare Codex state: %w", err)
		}
	}
	command, err := newAgentCommand(
		config,
		workingDirectory,
		prompt,
		tools,
	)
	if err != nil {
		return nil, err
	}
	return startAgentProcess(command, client, marker)
}

func startFallbackAgent(
	config config,
	workingDirectory string,
	prompt string,
	marker string,
) (*agentRun, error) {
	client, err := selectedAcceptanceClient(config)
	if err != nil {
		return nil, err
	}
	if client.ID != acceptanceClientCodexCLI {
		return nil, errors.New("HTTP fallback evidence requires fixed Codex")
	}
	if err := privateDirectory(
		filepath.Join(workingDirectory, codexStateDirectoryName),
	); err != nil {
		return nil, fmt.Errorf("prepare Codex state: %w", err)
	}
	command, err := newFallbackAgentCommand(
		config,
		workingDirectory,
		prompt,
	)
	if err != nil {
		return nil, err
	}
	return startAgentProcess(command, client, marker)
}

func startResumeAgent(
	config config,
	workingDirectory string,
	prompt string,
	threadID agentThreadID,
	marker string,
) (*agentRun, error) {
	client, err := selectedAcceptanceClient(config)
	if err != nil {
		return nil, err
	}
	if client.ID != acceptanceClientCodexCLI {
		return nil, errors.New("resume requires the fixed Codex client")
	}
	if err := privateDirectory(
		filepath.Join(workingDirectory, codexStateDirectoryName),
	); err != nil {
		return nil, fmt.Errorf("prepare Codex state: %w", err)
	}
	command, err := newResumeAgentCommand(
		config,
		workingDirectory,
		prompt,
		threadID,
	)
	if err != nil {
		return nil, err
	}
	return startAgentProcess(command, client, marker)
}

func startAgentProcess(
	command *exec.Cmd,
	client acceptanceClient,
	marker string,
) (*agentRun, error) {
	if command == nil {
		return nil, errors.New("captured client command is nil")
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := newBoundedBuffer(256 << 10)
	command.Stderr = stderr
	run := &agentRun{
		command:    command,
		done:       make(chan struct{}),
		outputDone: make(chan struct{}),
		stderr:     stderr,
		changed:    make(chan struct{}),
		marker:     marker,
		clientID:   client.ID,
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf(
			"start captured %s: %w",
			client.ReportLabel,
			err,
		)
	}
	go run.consume(stdout)
	go func() {
		waitErr := command.Wait()
		run.mu.Lock()
		run.waitErr = waitErr
		run.signalLocked()
		run.mu.Unlock()
		close(run.done)
	}()
	return run, nil
}

func newAgentCommand(
	config config,
	workingDirectory string,
	prompt string,
	tools string,
) (*exec.Cmd, error) {
	return newAgentCommandWithCodexTransport(
		config,
		workingDirectory,
		prompt,
		tools,
		codexResponsesHTTPOnly,
	)
}

func newFallbackAgentCommand(
	config config,
	workingDirectory string,
	prompt string,
) (*exec.Cmd, error) {
	return newAgentCommandWithCodexTransport(
		config,
		workingDirectory,
		prompt,
		"",
		codexResponsesFallbackEvidence,
	)
}

func newAgentCommandWithCodexTransport(
	config config,
	workingDirectory string,
	prompt string,
	tools string,
	transport codexResponsesTransport,
) (*exec.Cmd, error) {
	client, err := selectedAcceptanceClient(config)
	if err != nil {
		return nil, err
	}
	var arguments []string
	stateDirectory := ""
	switch client.ID {
	case acceptanceClientClaudeCode:
		if transport != codexResponsesHTTPOnly {
			return nil, errors.New(
				"HTTP fallback evidence is unsupported for Claude",
			)
		}
		arguments = fixedClaudeArguments(client.ExecutablePath, tools)
	case acceptanceClientCodexCLI:
		arguments, err = fixedCodexArguments(
			client.ExecutablePath,
			transport,
		)
		if err != nil {
			return nil, err
		}
		stateDirectory = filepath.Join(
			workingDirectory,
			codexStateDirectoryName,
		)
	default:
		return nil, errors.New("acceptance client invocation is unsupported")
	}
	command := exec.Command(config.launcherPath, arguments...)
	command.Dir = workingDirectory
	command.Env, err = clientAcceptanceEnvironment(
		client.ID,
		os.Environ(),
		stateDirectory,
	)
	if err != nil {
		return nil, err
	}
	command.Stdin = strings.NewReader(prompt)
	return command, nil
}

func fixedClaudeArguments(
	claudePath string,
	tools string,
) []string {
	arguments := []string{
		"run",
		"--",
		claudePath,
		"--print",
		"--output-format",
		"stream-json",
		"--include-partial-messages",
		"--verbose",
		"--debug",
		"api",
		"--safe-mode",
		"--no-session-persistence",
		"--model",
		"sonnet",
		"--tools=" + tools,
	}
	if tools != "" {
		arguments = append(
			arguments,
			"--permission-mode",
			"bypassPermissions",
		)
	}
	return arguments
}

func fixedCodexArguments(
	codexPath string,
	transport codexResponsesTransport,
) ([]string, error) {
	arguments := []string{
		"run",
		"--",
		codexPath,
	}
	switch transport {
	case codexResponsesHTTPOnly:
		arguments = append(
			arguments,
			"-c",
			codexHTTPProviderSelect,
			"-c",
			codexHTTPProviderDefine,
		)
	case codexResponsesFallbackEvidence:
	default:
		return nil, errors.New("Codex Responses transport is unsupported")
	}
	arguments = append(
		arguments,
		"-a",
		"never",
		"-s",
		"workspace-write",
		"exec",
		"--json",
		"--skip-git-repo-check",
		"--ignore-user-config",
		"--ignore-rules",
		"--color",
		"never",
		"--model",
		fixedCodexRequestedModel,
		"-",
	)
	return arguments, nil
}

func newResumeAgentCommand(
	config config,
	workingDirectory string,
	prompt string,
	threadID agentThreadID,
) (*exec.Cmd, error) {
	client, err := selectedAcceptanceClient(config)
	if err != nil {
		return nil, err
	}
	if client.ID != acceptanceClientCodexCLI || threadID.String() == "" {
		return nil, errors.New("Codex resume invocation is invalid")
	}
	stateDirectory := filepath.Join(
		workingDirectory,
		codexStateDirectoryName,
	)
	environment, err := clientAcceptanceEnvironment(
		client.ID,
		os.Environ(),
		stateDirectory,
	)
	if err != nil {
		return nil, err
	}
	arguments := []string{
		"run",
		"--",
		client.ExecutablePath,
		"-c",
		codexHTTPProviderSelect,
		"-c",
		codexHTTPProviderDefine,
		"-a",
		"never",
		"-s",
		"workspace-write",
		"exec",
		"resume",
		"--json",
		"--skip-git-repo-check",
		"--ignore-user-config",
		"--ignore-rules",
		"--model",
		fixedCodexRequestedModel,
		threadID.String(),
		"-",
	}
	command := exec.Command(config.launcherPath, arguments...)
	command.Dir = workingDirectory
	command.Env = environment
	command.Stdin = strings.NewReader(prompt)
	return command, nil
}

func clientAcceptanceEnvironment(
	clientID acceptanceClientID,
	base []string,
	stateDirectory string,
) ([]string, error) {
	result := acceptanceEnvironment(base)
	switch clientID {
	case acceptanceClientClaudeCode:
		if stateDirectory != "" {
			return nil, errors.New(
				"Claude acceptance state directory is unexpected",
			)
		}
		result = append(
			result,
			"ANTHROPIC_API_KEY=vibermate-assembly-placeholder",
		)
	case acceptanceClientCodexCLI:
		if stateDirectory == "" ||
			!filepath.IsAbs(stateDirectory) ||
			filepath.Clean(stateDirectory) != stateDirectory {
			return nil, errors.New(
				"Codex acceptance state directory is invalid",
			)
		}
		result = append(result, "CODEX_HOME="+stateDirectory)
	default:
		return nil, errors.New("acceptance client environment is unsupported")
	}
	slices.Sort(result)
	return result, nil
}

func acceptanceEnvironment(base []string) []string {
	managed := map[string]struct{}{
		"ANTHROPIC_API_KEY":          {},
		"ANTHROPIC_AUTH_TOKEN":       {},
		"ANTHROPIC_BASE_URL":         {},
		"ANTHROPIC_BEDROCK_BASE_URL": {},
		"ANTHROPIC_CUSTOM_HEADERS":   {},
		"ANTHROPIC_FOUNDRY_BASE_URL": {},
		"ANTHROPIC_VERTEX_BASE_URL":  {},
		"CLAUDE_CODE_OAUTH_TOKEN":    {},
		"CLAUDE_CODE_USE_BEDROCK":    {},
		"CLAUDE_CODE_USE_FOUNDRY":    {},
		"CLAUDE_CODE_USE_VERTEX":     {},
		"CODEX_API_KEY":              {},
		"CODEX_BASE_URL":             {},
		"CODEX_HOME":                 {},
		"CURL_CA_BUNDLE":             {},
		"NODE_EXTRA_CA_CERTS":        {},
		"NODE_USE_ENV_PROXY":         {},
		"OPENAI_ACCESS_TOKEN":        {},
		"OPENAI_API_KEY":             {},
		"OPENAI_BASE_URL":            {},
		"OPENAI_ORGANIZATION":        {},
		"OPENAI_ORG_ID":              {},
		"OPENAI_PROJECT":             {},
		"OPENAI_PROJECT_ID":          {},
		"REQUESTS_CA_BUNDLE":         {},
		"SSL_CERT_FILE":              {},
	}
	result := make([]string, 0, len(base))
	for _, entry := range base {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if _, replace := managed[name]; replace {
			continue
		}
		result = append(result, entry)
	}
	slices.Sort(result)
	return result
}

func (run *agentRun) consume(reader io.Reader) {
	defer close(run.outputDone)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxAgentLineBytes)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		run.mu.Lock()
		run.lines++
		if run.lines > maxAgentLines {
			run.readErr = fmt.Errorf(
				"%s output exceeded the line bound",
				run.clientLabel(),
			)
			run.signalLocked()
			run.mu.Unlock()
			_ = run.command.Process.Signal(syscall.SIGINT)
			return
		}
		run.observeLine(line)
		run.signalLocked()
		run.mu.Unlock()
	}
	run.mu.Lock()
	if err := scanner.Err(); err != nil && run.readErr == nil {
		run.readErr = err
	}
	run.signalLocked()
	run.mu.Unlock()
}

func (run *agentRun) observeLine(line []byte) {
	if run.clientID == acceptanceClientCodexCLI {
		run.observeCodexLine(line)
		return
	}
	run.observeClaudeLine(line)
}

func (run *agentRun) observeClaudeLine(line []byte) {
	var envelope any
	if json.Unmarshal(line, &envelope) != nil {
		run.failure.merge(extractAgentFailureEvidence(line))
		return
	}
	run.deltas += countType(envelope, "content_block_delta")
	run.toolUses += trustedToolUseCount(envelope)
	run.failure.merge(failureEvidenceFromEnvelope(envelope))
	if tools, initialized := trustedConfiguredTools(envelope); initialized {
		run.configurationSeen = true
		run.configuredTools = tools
	}
	if typed, ok := envelope.(map[string]any); ok {
		kind, _ := typed["type"].(string)
		if validEnvelopeType(kind) {
			run.lastType = kind
		}
	}
	text := trustedAssistantText(envelope)
	run.observeTrustedText(text)
}

func (run *agentRun) observeCodexLine(line []byte) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(line, &envelope) != nil {
		run.failure.merge(extractAgentFailureEvidence(line))
		return
	}
	var kind string
	if json.Unmarshal(envelope["type"], &kind) != nil {
		return
	}
	switch kind {
	case "thread.started":
		var rawThreadID string
		if json.Unmarshal(envelope["thread_id"], &rawThreadID) != nil {
			run.setReadError(errors.New(
				"Codex thread event omitted its identity",
			))
			return
		}
		threadID, err := parseAgentThreadID(rawThreadID)
		if err != nil {
			run.setReadError(err)
			return
		}
		if run.threadID.String() != "" && run.threadID != threadID {
			run.setReadError(errors.New(
				"Codex thread identity changed within one invocation",
			))
			return
		}
		run.threadID = threadID
		run.lastType = kind
	case "turn.started":
		if run.threadID.String() == "" || run.turnStarted ||
			run.turnCompleted {
			run.setReadError(errors.New(
				"Codex turn start sequence is invalid",
			))
			return
		}
		run.turnStarted = true
		run.lastType = kind
	case "item.completed":
		if !run.turnStarted || run.turnCompleted {
			run.setReadError(errors.New(
				"Codex completed item sequence is invalid",
			))
			return
		}
		run.observeCodexCompletedItem(envelope["item"])
		run.lastType = kind
	case "turn.completed":
		if !run.turnStarted || run.turnCompleted {
			run.setReadError(errors.New(
				"Codex turn completion sequence is invalid",
			))
			return
		}
		run.turnCompleted = true
		run.lastType = kind
	case "turn.failed":
		run.failure.merge(extractAgentFailureEvidence(line))
		run.lastType = kind
	case "item.started", "item.updated":
		if run.turnStarted && !run.turnCompleted {
			run.lastType = kind
		}
	default:
		run.failure.merge(extractAgentFailureEvidence(line))
	}
}

func (run *agentRun) observeCodexCompletedItem(raw json.RawMessage) {
	var item struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &item) != nil {
		run.setReadError(errors.New("Codex completed item is invalid"))
		return
	}
	switch item.Type {
	case "agent_message":
		run.agentMessages++
		run.observeTrustedText(item.Text)
	case "error":
		if strings.TrimSpace(item.Message) == codexHTTPFallbackMessage {
			run.httpFallbackSeen = true
		}
		run.failure.merge(extractAgentFailureEvidence(raw))
	case "command_execution",
		"file_change",
		"mcp_tool_call",
		"collaboration_tool_call",
		"web_search":
		run.toolUses++
	}
}

func (run *agentRun) setReadError(err error) {
	if run.readErr == nil {
		run.readErr = err
	}
}

func (run *agentRun) observeTrustedText(text string) {
	candidate := run.markerTail + text
	if run.marker != "" && strings.Contains(candidate, run.marker) {
		run.markerSeen = true
	}
	const tailLimit = 64
	if len(candidate) > tailLimit {
		candidate = candidate[len(candidate)-tailLimit:]
	}
	run.markerTail = candidate
}

func trustedConfiguredTools(value any) ([]string, bool) {
	envelope, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	kind, _ := envelope["type"].(string)
	subtype, _ := envelope["subtype"].(string)
	if kind != "system" || subtype != "init" {
		return nil, false
	}
	rawTools, ok := envelope["tools"].([]any)
	if !ok || len(rawTools) > 128 {
		return nil, true
	}
	tools := make([]string, 0, len(rawTools))
	for _, rawTool := range rawTools {
		tool, valid := rawTool.(string)
		if !valid || tool == "" || len(tool) > 128 {
			return nil, true
		}
		tools = append(tools, tool)
	}
	slices.Sort(tools)
	return slices.Compact(tools), true
}

func failureEvidenceFromEnvelope(value any) agentFailureEvidence {
	evidence := agentFailureEvidence{}
	envelope, ok := value.(map[string]any)
	if ok {
		if kind, _ := envelope["type"].(string); kind == "result" {
			subtype, _ := envelope["subtype"].(string)
			if validResultSubtype(subtype) {
				evidence.resultSubtype = subtype
			}
		}
	}
	collectFailureEvidence(value, &evidence)
	return evidence
}

func collectFailureEvidence(value any, evidence *agentFailureEvidence) {
	switch typed := value.(type) {
	case map[string]any:
		if rawReason, ok := typed["reasonCode"].(string); ok {
			if reason := knownExchangeReason(rawReason); reason != "" {
				evidence.reasonCode = reason
				if rawStatus, exists := typed["providerStatus"]; exists {
					evidence.providerStatus = boundedJSONStatus(rawStatus)
				}
				if rawField, exists := typed["providerField"].(string); exists {
					evidence.providerField = knownProviderField(rawField)
				}
			}
		}
		if rawReason, ok := typed["protocolReason"].(string); ok {
			evidence.protocolReason = knownProtocolReason(rawReason)
		}
		if rawIssue, ok := typed["providerResponseIssue"].(string); ok {
			evidence.responseIssue = knownProviderResponseIssue(rawIssue)
		}
		for _, nested := range typed {
			collectFailureEvidence(nested, evidence)
		}
	case []any:
		for _, nested := range typed {
			collectFailureEvidence(nested, evidence)
		}
	case string:
		evidence.merge(extractAgentFailureEvidence([]byte(typed)))
	}
}

func extractAgentFailureEvidence(payload []byte) agentFailureEvidence {
	evidence := agentFailureEvidence{}
	for _, reason := range knownExchangeReasons() {
		if bytes.Contains(payload, []byte(reason)) {
			evidence.reasonCode = reason
			break
		}
	}
	for _, reason := range knownProtocolReasons() {
		for _, prefix := range [][]byte{
			[]byte(`"protocolReason":"`),
			[]byte("protocolReason="),
		} {
			marker := append(append([]byte(nil), prefix...), []byte(reason)...)
			if bytes.Contains(payload, marker) {
				evidence.protocolReason = reason
				break
			}
		}
		if evidence.protocolReason != "" {
			break
		}
	}
	for _, issue := range knownProviderResponseIssues() {
		for _, prefix := range [][]byte{
			[]byte(`"providerResponseIssue":"`),
			[]byte("providerResponseIssue="),
		} {
			marker := append(append([]byte(nil), prefix...), []byte(issue)...)
			if bytes.Contains(payload, marker) {
				evidence.responseIssue = issue
				break
			}
		}
		if evidence.responseIssue != "" {
			break
		}
	}
	if evidence.reasonCode != "" {
		evidence.providerStatus = extractProviderStatus(payload)
		evidence.providerField = extractProviderField(payload)
	}
	lower := bytes.ToLower(payload)
	evidence.agentStatus = extractAgentStatus(lower)
	evidence.category = classifyAgentFailure(lower)
	var decoded any
	if json.Unmarshal(payload, &decoded) == nil {
		switch decoded.(type) {
		case map[string]any, []any:
			evidence.merge(failureEvidenceFromEnvelope(decoded))
		}
	}
	return evidence
}

func extractProviderField(payload []byte) exchange.ProviderField {
	for _, field := range []exchange.ProviderField{
		exchange.ProviderFieldModel,
		exchange.ProviderFieldMessages,
		exchange.ProviderFieldTools,
		exchange.ProviderFieldToolChoice,
		exchange.ProviderFieldParallelToolCalls,
		exchange.ProviderFieldMaxCompletionTokens,
		exchange.ProviderFieldMaxTokens,
		exchange.ProviderFieldReasoningEffort,
		exchange.ProviderFieldTemperature,
		exchange.ProviderFieldTopP,
		exchange.ProviderFieldStop,
		exchange.ProviderFieldStream,
		exchange.ProviderFieldStreamOptions,
		exchange.ProviderFieldN,
	} {
		for _, prefix := range [][]byte{
			[]byte(`"providerField":"`),
			[]byte("providerField="),
		} {
			marker := append(append([]byte(nil), prefix...), []byte(field)...)
			if bytes.Contains(payload, marker) {
				return field
			}
		}
	}
	return ""
}

func extractAgentStatus(payload []byte) int {
	for _, marker := range [][]byte{
		[]byte("api error"),
		[]byte("http error"),
		[]byte("status code"),
		[]byte("error:"),
	} {
		index := bytes.Index(payload, marker)
		if index < 0 {
			continue
		}
		if status := firstHTTPStatus(
			payload[index+len(marker):],
			48,
		); status != 0 {
			return status
		}
	}
	return 0
}

func firstHTTPStatus(payload []byte, limit int) int {
	if len(payload) > limit {
		payload = payload[:limit]
	}
	for index := 0; index+3 <= len(payload); index++ {
		if payload[index] < '1' || payload[index] > '5' ||
			payload[index+1] < '0' || payload[index+1] > '9' ||
			payload[index+2] < '0' || payload[index+2] > '9' {
			continue
		}
		if index > 0 &&
			payload[index-1] >= '0' &&
			payload[index-1] <= '9' {
			continue
		}
		if index+3 < len(payload) &&
			payload[index+3] >= '0' &&
			payload[index+3] <= '9' {
			continue
		}
		status := int(payload[index]-'0')*100 +
			int(payload[index+1]-'0')*10 +
			int(payload[index+2]-'0')
		return status
	}
	return 0
}

func classifyAgentFailure(payload []byte) string {
	switch {
	case containsAny(payload, "invalid api key", "unauthorized", "authentication"):
		return "authentication"
	case bytes.Contains(payload, []byte("model")) &&
		containsAny(payload, "not found", "does not exist", "unsupported"):
		return "model_unavailable"
	case containsAny(payload, "rate limit", "too many requests"):
		return "rate_limited"
	case containsAny(payload, "insufficient quota", "credit balance", "billing"):
		return "quota"
	case containsAny(payload, "certificate", "tls handshake"):
		return "tls"
	case containsAny(payload, "connection refused", "connection reset", "econn"):
		return "connection"
	case containsAny(payload, "unexpected eof", "stream closed", "socket closed"):
		return "stream"
	case bytes.Contains(payload, []byte("not found")):
		return "not_found"
	default:
		return ""
	}
}

func containsAny(payload []byte, values ...string) bool {
	for _, value := range values {
		if bytes.Contains(payload, []byte(value)) {
			return true
		}
	}
	return false
}

func extractProviderStatus(payload []byte) int {
	for _, field := range [][]byte{
		[]byte(`"providerStatus"`),
		[]byte(`providerStatus`),
	} {
		index := bytes.Index(payload, field)
		if index < 0 {
			continue
		}
		remainder := payload[index+len(field):]
		for len(remainder) > 0 &&
			(remainder[0] == ':' ||
				remainder[0] == '=' ||
				remainder[0] == ' ' ||
				remainder[0] == '\t') {
			remainder = remainder[1:]
		}
		status := 0
		digits := 0
		for len(remainder) > 0 &&
			remainder[0] >= '0' &&
			remainder[0] <= '9' &&
			digits < 3 {
			status = status*10 + int(remainder[0]-'0')
			remainder = remainder[1:]
			digits++
		}
		if digits == 3 && status >= 100 && status <= 599 {
			return status
		}
	}
	return 0
}

func boundedJSONStatus(value any) int {
	number, ok := value.(float64)
	if !ok || number < 100 || number > 599 || number != float64(int(number)) {
		return 0
	}
	return int(number)
}

func knownExchangeReason(value string) exchange.ReasonCode {
	for _, reason := range knownExchangeReasons() {
		if string(reason) == value {
			return reason
		}
	}
	return ""
}

func knownProviderField(value string) exchange.ProviderField {
	switch exchange.ProviderField(value) {
	case exchange.ProviderFieldModel,
		exchange.ProviderFieldMessages,
		exchange.ProviderFieldTools,
		exchange.ProviderFieldToolChoice,
		exchange.ProviderFieldParallelToolCalls,
		exchange.ProviderFieldMaxCompletionTokens,
		exchange.ProviderFieldMaxTokens,
		exchange.ProviderFieldReasoningEffort,
		exchange.ProviderFieldTemperature,
		exchange.ProviderFieldTopP,
		exchange.ProviderFieldStop,
		exchange.ProviderFieldStream,
		exchange.ProviderFieldStreamOptions,
		exchange.ProviderFieldN:
		return exchange.ProviderField(value)
	default:
		return ""
	}
}

func knownExchangeReasons() []exchange.ReasonCode {
	return []exchange.ReasonCode{
		exchange.ReasonInvalidExchangeRequest,
		exchange.ReasonUnsupportedClientInput,
		exchange.ReasonAccessPlanUnavailable,
		exchange.ReasonUnsupportedAccessPlan,
		exchange.ReasonProviderRequestInvalid,
		exchange.ReasonProviderCredentialUnavailable,
		exchange.ReasonProviderTransportFailed,
		exchange.ReasonProviderResponseIdle,
		exchange.ReasonProviderStatusRejected,
		exchange.ReasonProviderResponseInvalid,
		exchange.ReasonTransportRetryExhausted,
		exchange.ReasonToolDecisionRejected,
		exchange.ReasonToolDecisionUnavailable,
		exchange.ReasonDownstreamCommitFailed,
		exchange.ReasonDownstreamDisconnected,
		exchange.ReasonExchangeCanceled,
		exchange.ReasonExchangeRuntimeStopping,
		exchange.ReasonDownstreamFailureAborted,
	}
}

func knownProtocolReasons() []protocolcore.Reason {
	return []protocolcore.Reason{
		protocolcore.ReasonInvalidClientRequest,
		protocolcore.ReasonUnsupportedClientInput,
		protocolcore.ReasonInvalidProviderResponse,
		protocolcore.ReasonUnsupportedProviderData,
		protocolcore.ReasonMalformedEventStream,
		protocolcore.ReasonTruncatedEventStream,
		protocolcore.ReasonStreamStateViolation,
		protocolcore.ReasonStreamLimitExceeded,
		protocolcore.ReasonToolCallIncomplete,
		protocolcore.ReasonOperationCanceled,
	}
}

func knownProtocolReason(value string) protocolcore.Reason {
	for _, reason := range knownProtocolReasons() {
		if string(reason) == value {
			return reason
		}
	}
	return ""
}

func knownProviderResponseIssues() []exchange.ProviderResponseIssue {
	return []exchange.ProviderResponseIssue{
		exchange.ProviderResponseIssueContentType,
	}
}

func knownProviderResponseIssue(value string) exchange.ProviderResponseIssue {
	for _, issue := range knownProviderResponseIssues() {
		if string(issue) == value {
			return issue
		}
	}
	return ""
}

func validResultSubtype(value string) bool {
	switch value {
	case "success",
		"error_during_execution",
		"error_max_turns",
		"error_max_budget_usd",
		"error_max_structured_output_retries":
		return true
	default:
		return false
	}
}

func validEnvelopeType(value string) bool {
	switch value {
	case "system", "assistant", "user", "stream_event", "result":
		return true
	default:
		return false
	}
}

func (evidence *agentFailureEvidence) merge(candidate agentFailureEvidence) {
	if candidate.reasonCode != "" {
		evidence.reasonCode = candidate.reasonCode
	}
	if candidate.providerStatus != 0 {
		evidence.providerStatus = candidate.providerStatus
	}
	if candidate.providerField != "" {
		evidence.providerField = candidate.providerField
	}
	if candidate.protocolReason != "" {
		evidence.protocolReason = candidate.protocolReason
	}
	if candidate.responseIssue != "" {
		evidence.responseIssue = candidate.responseIssue
	}
	if candidate.agentStatus != 0 {
		evidence.agentStatus = candidate.agentStatus
	}
	if candidate.category != "" {
		evidence.category = candidate.category
	}
	if candidate.resultSubtype != "" {
		evidence.resultSubtype = candidate.resultSubtype
	}
}

func trustedToolUseCount(value any) int {
	envelope, ok := value.(map[string]any)
	if !ok {
		return 0
	}
	kind, _ := envelope["type"].(string)
	if kind != "assistant" && kind != "stream_event" {
		return 0
	}
	return countType(envelope, "tool_use")
}

func trustedAssistantText(value any) string {
	envelope, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	kind, _ := envelope["type"].(string)
	switch kind {
	case "assistant":
		return collectText(envelope["message"])
	case "stream_event":
		return collectText(envelope["event"])
	case "result":
		result, _ := envelope["result"].(string)
		return result
	default:
		return ""
	}
}

func collectText(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		var result strings.Builder
		for key, nested := range typed {
			if key == "text" {
				text, _ := nested.(string)
				result.WriteString(text)
				continue
			}
			result.WriteString(collectText(nested))
		}
		return result.String()
	case []any:
		var result strings.Builder
		for _, nested := range typed {
			result.WriteString(collectText(nested))
		}
		return result.String()
	default:
		return ""
	}
}

func countType(value any, expected string) int {
	switch typed := value.(type) {
	case map[string]any:
		count := 0
		if kind, _ := typed["type"].(string); kind == expected {
			count++
		}
		for _, nested := range typed {
			count += countType(nested, expected)
		}
		return count
	case []any:
		count := 0
		for _, nested := range typed {
			count += countType(nested, expected)
		}
		return count
	default:
		return 0
	}
}

func (run *agentRun) wait(
	ctx context.Context,
) (int, error) {
	if run == nil {
		return -1, errors.New("client run is nil")
	}
	select {
	case <-run.done:
		select {
		case <-run.outputDone:
		case <-ctx.Done():
			return -1, ctx.Err()
		}
		run.mu.Lock()
		readErr := run.readErr
		waitErr := run.waitErr
		run.mu.Unlock()
		if readErr != nil {
			return -1, readErr
		}
		if waitErr == nil {
			return 0, nil
		}
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			return exitError.ExitCode(), nil
		}
		return -1, waitErr
	case <-ctx.Done():
		_ = run.command.Process.Signal(syscall.SIGINT)
		select {
		case <-run.done:
		case <-time.After(5 * time.Second):
			_ = run.command.Process.Kill()
			<-run.done
		}
		select {
		case <-run.outputDone:
		case <-time.After(5 * time.Second):
		}
		return -1, ctx.Err()
	}
}

func (run *agentRun) signalInterrupt() error {
	if run == nil || run.command == nil || run.command.Process == nil {
		return errors.New("client run process is unavailable")
	}
	return run.command.Process.Signal(os.Interrupt)
}

func (run *agentRun) waitForDelta(ctx context.Context) error {
	if run == nil || run.clientID != acceptanceClientClaudeCode {
		return errors.New("Claude stream run is unavailable")
	}
	for {
		run.mu.Lock()
		if run.deltas > 0 {
			run.mu.Unlock()
			return nil
		}
		if run.readErr != nil {
			err := run.readErr
			run.mu.Unlock()
			return err
		}
		changed := run.changed
		run.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		case <-run.done:
			select {
			case <-run.outputDone:
			case <-ctx.Done():
				return ctx.Err()
			}
			run.mu.Lock()
			if run.deltas > 0 {
				run.mu.Unlock()
				return nil
			}
			waitErr := run.waitErr
			run.mu.Unlock()
			if waitErr == nil {
				return errors.New(
					"Claude exited before its first streamed delta",
				)
			}
			return fmt.Errorf(
				"Claude exited before its first streamed delta: %w",
				normalizeWaitError(waitErr),
			)
		}
	}
}

func (run *agentRun) waitForConfiguredTool(
	ctx context.Context,
	expected string,
) error {
	if run == nil {
		return errors.New("client run is nil")
	}
	if expected == "" {
		return errors.New("expected Claude tool is empty")
	}
	for {
		run.mu.Lock()
		if run.configurationSeen {
			tools := append([]string(nil), run.configuredTools...)
			run.mu.Unlock()
			if slices.Contains(tools, expected) {
				return nil
			}
			return fmt.Errorf(
				"Claude init did not expose required tool %q; available tools: %s",
				expected,
				strings.Join(tools, ","),
			)
		}
		if run.readErr != nil {
			err := run.readErr
			run.mu.Unlock()
			return err
		}
		changed := run.changed
		run.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		case <-run.done:
			select {
			case <-run.outputDone:
			case <-ctx.Done():
				return ctx.Err()
			}
			run.mu.Lock()
			seen := run.configurationSeen
			tools := append([]string(nil), run.configuredTools...)
			readErr := run.readErr
			run.mu.Unlock()
			if readErr != nil {
				return readErr
			}
			if seen && slices.Contains(tools, expected) {
				return nil
			}
			return fmt.Errorf(
				"Claude exited before exposing required tool %q",
				expected,
			)
		}
	}
}

func (run *agentRun) waitForHTTPFallback(ctx context.Context) error {
	if run == nil || run.clientID != acceptanceClientCodexCLI {
		return errors.New("Codex fallback run is unavailable")
	}
	for {
		run.mu.Lock()
		if run.httpFallbackSeen {
			run.mu.Unlock()
			return nil
		}
		if run.readErr != nil {
			err := run.readErr
			run.mu.Unlock()
			return err
		}
		changed := run.changed
		run.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		case <-run.done:
			select {
			case <-run.outputDone:
			case <-ctx.Done():
				return ctx.Err()
			}
			run.mu.Lock()
			seen := run.httpFallbackSeen
			readErr := run.readErr
			run.mu.Unlock()
			if readErr != nil {
				return readErr
			}
			if seen {
				return nil
			}
			return errors.New(
				"Codex exited without HTTP fallback evidence",
			)
		}
	}
}

func (run *agentRun) clientLabel() string {
	if run == nil {
		return "Agent"
	}
	switch run.clientID {
	case acceptanceClientClaudeCode:
		return "Claude"
	case acceptanceClientCodexCLI:
		return "Codex"
	default:
		return "Agent"
	}
}

func (run *agentRun) evidence() (
	lines int,
	deltas int,
	toolUses int,
	marker bool,
) {
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.lines, run.deltas, run.toolUses, run.markerSeen
}

type agentProtocolEvidence struct {
	ThreadID         agentThreadID
	AgentMessages    int
	HTTPFallbackSeen bool
	TurnStarted      bool
	TurnCompleted    bool
}

func (run *agentRun) protocolEvidence() agentProtocolEvidence {
	if run == nil {
		return agentProtocolEvidence{}
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	return agentProtocolEvidence{
		ThreadID:         run.threadID,
		AgentMessages:    run.agentMessages,
		HTTPFallbackSeen: run.httpFallbackSeen,
		TurnStarted:      run.turnStarted,
		TurnCompleted:    run.turnCompleted,
	}
}

func (run *agentRun) safeFailureEvidence() string {
	if run == nil {
		return ""
	}
	var stderr []byte
	var truncated bool
	if run.stderr != nil {
		stderr, truncated = run.stderr.snapshot()
	}
	stderrEvidence := extractAgentFailureEvidence(stderr)
	run.mu.Lock()
	evidence := run.failure
	lines := run.lines
	deltas := run.deltas
	lastType := run.lastType
	run.mu.Unlock()
	evidence.merge(stderrEvidence)

	fields := []string{
		fmt.Sprintf("stdoutLines=%d", lines),
		fmt.Sprintf("deltas=%d", deltas),
		fmt.Sprintf("stderrBytes=%d", len(stderr)),
	}
	if lastType != "" {
		fields = append(fields, "lastEnvelope="+lastType)
	}
	if evidence.reasonCode != "" {
		fields = append(fields, "reasonCode="+string(evidence.reasonCode))
	}
	if evidence.providerStatus != 0 {
		fields = append(
			fields,
			fmt.Sprintf("providerStatus=%d", evidence.providerStatus),
		)
	}
	if evidence.providerField != "" {
		fields = append(
			fields,
			"providerField="+string(evidence.providerField),
		)
	}
	if evidence.protocolReason != "" {
		fields = append(
			fields,
			"protocolReason="+string(evidence.protocolReason),
		)
	}
	if evidence.responseIssue != "" {
		fields = append(
			fields,
			"providerResponseIssue="+string(evidence.responseIssue),
		)
	}
	if evidence.agentStatus != 0 {
		fields = append(
			fields,
			fmt.Sprintf("agentStatus=%d", evidence.agentStatus),
		)
	}
	if evidence.category != "" {
		fields = append(fields, "category="+evidence.category)
	}
	if keywords := safeFailureKeywords(stderr); len(keywords) != 0 {
		fields = append(fields, "keywords="+strings.Join(keywords, ","))
	}
	if shape := safeFailureShape(stderr); shape != "" {
		fields = append(fields, "shape="+shape)
	}
	if evidence.resultSubtype != "" {
		fields = append(fields, "resultSubtype="+evidence.resultSubtype)
	}
	if truncated {
		fields = append(fields, "stderrTruncated=true")
	}
	return strings.Join(fields, " ")
}

func safeFailureShape(payload []byte) string {
	const maximumTokens = 24
	tokens := make([]string, 0, maximumTokens)
	for start := 0; start < len(payload) && len(tokens) < maximumTokens; {
		for start < len(payload) && !isDiagnosticToken(payload[start]) {
			start++
		}
		end := start
		numeric := true
		for end < len(payload) && isDiagnosticToken(payload[end]) {
			if payload[end] < '0' || payload[end] > '9' {
				numeric = false
			}
			end++
		}
		if end == start {
			break
		}
		length := end - start
		if numeric && length == 3 &&
			payload[start] >= '1' && payload[start] <= '5' {
			tokens = append(tokens, "s"+string(payload[start:end]))
		} else if numeric {
			tokens = append(tokens, fmt.Sprintf("n%d", length))
		} else {
			tokens = append(tokens, fmt.Sprintf("a%d", length))
		}
		start = end
	}
	return strings.Join(tokens, ".")
}

func isDiagnosticToken(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' ||
		value == '_' ||
		value == '-'
}

func safeFailureKeywords(payload []byte) []string {
	lower := bytes.ToLower(payload)
	dictionary := []string{
		"access",
		"account",
		"api",
		"authentication",
		"authorization",
		"available",
		"balance",
		"billing",
		"body",
		"captured",
		"certificate",
		"claude",
		"closed",
		"code",
		"command",
		"connect",
		"connection",
		"content",
		"credit",
		"denied",
		"does",
		"endpoint",
		"eof",
		"error",
		"failed",
		"fetch",
		"forbidden",
		"format",
		"found",
		"internal",
		"invalid",
		"json",
		"key",
		"limit",
		"maximum",
		"message",
		"missing",
		"model",
		"network",
		"not",
		"offline",
		"organization",
		"overloaded",
		"parse",
		"path",
		"permission",
		"proxy",
		"quota",
		"rate",
		"region",
		"request",
		"required",
		"response",
		"retry",
		"route",
		"schema",
		"server",
		"service",
		"socket",
		"started",
		"status",
		"stream",
		"streaming",
		"timeout",
		"tls",
		"tool",
		"type",
		"unauthorized",
		"unknown",
		"unavailable",
		"unexpected",
		"unsupported",
	}
	result := make([]string, 0, len(dictionary))
	for _, word := range dictionary {
		if containsWord(lower, word) {
			result = append(result, word)
		}
	}
	return result
}

func containsWord(payload []byte, word string) bool {
	needle := []byte(word)
	for start := 0; start < len(payload); {
		index := bytes.Index(payload[start:], needle)
		if index < 0 {
			return false
		}
		index += start
		beforeOK := index == 0 || !isASCIIWord(payload[index-1])
		afterIndex := index + len(needle)
		afterOK := afterIndex == len(payload) ||
			!isASCIIWord(payload[afterIndex])
		if beforeOK && afterOK {
			return true
		}
		start = index + 1
	}
	return false
}

func isASCIIWord(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= '0' && value <= '9' ||
		value == '_'
}

func (run *agentRun) signalLocked() {
	close(run.changed)
	run.changed = make(chan struct{})
}
