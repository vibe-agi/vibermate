// Package messagetransform owns the bounded JavaScript capability that may
// edit an AI Endpoint HTTP message. It has no transport, credential, routing,
// persistence, filesystem, or network authority.
package messagetransform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/textproto"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dop251/goja"
	"github.com/vibe-agi/vibermate/internal/clientannotation"
)

var (
	ErrInvalidPolicy   = errors.New("message transform policy is invalid")
	ErrExecutionFailed = errors.New("message transform execution failed")
	ErrInvalidOutput   = errors.New("message transform output is invalid")
)

// Policy is deliberately source-only. Compiled programs are rebuilt with an
// immutable Environment revision and are never part of its persisted form.
type Policy struct {
	RequestJavaScript  string `json:"requestJavaScript"`
	ResponseJavaScript string `json:"responseJavaScript"`
}

func (policy Policy) Enabled() bool {
	return policy.RequestJavaScript != "" || policy.ResponseJavaScript != ""
}

func (policy Policy) Validate() error {
	_, err := Compile(policy, DefaultLimits())
	return err
}

type Limits struct {
	MaximumScriptBytes       int
	MaximumBodyBytes         int
	MaximumHeaderFields      int
	MaximumHeaderBytes       int
	MaximumHeaderValueBytes  int
	MaximumContextBytes      int
	MaximumContextDepth      int
	MaximumContextValues     int
	MaximumCallStackDepth    int
	MaximumExecutionDuration time.Duration
}

func DefaultLimits() Limits {
	return Limits{
		MaximumScriptBytes:       64 << 10,
		MaximumBodyBytes:         16 << 20,
		MaximumHeaderFields:      128,
		MaximumHeaderBytes:       64 << 10,
		MaximumHeaderValueBytes:  16 << 10,
		MaximumContextBytes:      64 << 10,
		MaximumContextDepth:      16,
		MaximumContextValues:     1024,
		MaximumCallStackDepth:    256,
		MaximumExecutionDuration: 2 * time.Second,
	}
}

func (limits Limits) validate() error {
	if limits.MaximumScriptBytes <= 0 ||
		limits.MaximumBodyBytes <= 0 ||
		limits.MaximumHeaderFields <= 0 ||
		limits.MaximumHeaderBytes <= 0 ||
		limits.MaximumHeaderValueBytes <= 0 ||
		limits.MaximumContextBytes <= 0 ||
		limits.MaximumContextDepth <= 0 ||
		limits.MaximumContextValues <= 0 ||
		limits.MaximumCallStackDepth <= 0 ||
		limits.MaximumExecutionDuration <= 0 {
		return fmt.Errorf("%w: limits must be positive", ErrInvalidPolicy)
	}
	return nil
}

type Program struct {
	request  *goja.Program
	response *goja.Program
	limits   Limits
}

func (program Program) HasRequest() bool  { return program.request != nil }
func (program Program) HasResponse() bool { return program.response != nil }

func Compile(policy Policy, limits Limits) (Program, error) {
	if err := limits.validate(); err != nil {
		return Program{}, err
	}
	request, err := compileStage("request", "request", policy.RequestJavaScript, limits)
	if err != nil {
		return Program{}, err
	}
	response, err := compileStage("response", "response", policy.ResponseJavaScript, limits)
	if err != nil {
		return Program{}, err
	}
	return Program{request: request, response: response, limits: limits}, nil
}

// Pipeline composes independent transforms. Requests run in declaration order;
// responses unwind in reverse order so each transform sees the response to the
// request representation it produced.
type Pipeline struct {
	programs []Program
}

func CompilePipeline(policies []Policy, limits Limits) (Pipeline, error) {
	programs := make([]Program, len(policies))
	for index, policy := range policies {
		program, err := Compile(policy, limits)
		if err != nil {
			return Pipeline{}, fmt.Errorf("transform %d: %w", index+1, err)
		}
		programs[index] = program
	}
	return Pipeline{programs: programs}, nil
}

type PipelineTurn struct {
	turns       []*Turn
	annotations *clientannotation.Signer
}

func (pipeline Pipeline) NewTurn() *PipelineTurn {
	return pipeline.newTurn(runtimeMetadataJSON{}, nil)
}

func (pipeline Pipeline) NewTurnWithMetadata(metadata RuntimeMetadata) (*PipelineTurn, error) {
	return pipeline.NewTurnWithOptions(TurnOptions{Metadata: metadata})
}

type TurnOptions struct {
	Metadata    RuntimeMetadata
	Annotations *clientannotation.Signer
}

func (pipeline Pipeline) NewTurnWithOptions(options TurnOptions) (*PipelineTurn, error) {
	encoded, err := options.Metadata.encode()
	if err != nil {
		return nil, err
	}
	return pipeline.newTurn(encoded, options.Annotations), nil
}

func (pipeline Pipeline) newTurn(
	metadata runtimeMetadataJSON,
	annotations *clientannotation.Signer,
) *PipelineTurn {
	turns := make([]*Turn, len(pipeline.programs))
	for index, program := range pipeline.programs {
		turns[index] = program.newTurn(metadata, annotations)
	}
	return &PipelineTurn{turns: turns, annotations: annotations}
}

func (turn *PipelineTurn) HasRequest() bool {
	if turn == nil {
		return false
	}
	for _, item := range turn.turns {
		if item.HasRequest() {
			return true
		}
	}
	return false
}

func (turn *PipelineTurn) HasResponse() bool {
	if turn == nil {
		return false
	}
	for _, item := range turn.turns {
		if item.HasResponse() {
			return true
		}
	}
	return false
}

func (turn *PipelineTurn) ApplyRequest(
	ctx context.Context,
	input RequestMessage,
) (RequestMessage, error) {
	if turn == nil {
		return RequestMessage{}, fmt.Errorf("%w: Pipeline Turn is nil", ErrExecutionFailed)
	}
	output := cloneRequest(input)
	if turn.annotations != nil {
		cleaned, _, err := turn.StripRequestAnnotations(output.Body)
		if err != nil {
			return RequestMessage{}, err
		}
		output.Body = cleaned
	}
	var err error
	for _, item := range turn.turns {
		output, err = item.ApplyRequest(ctx, output)
		if err != nil {
			return RequestMessage{}, err
		}
	}
	return output, nil
}

func (turn *PipelineTurn) StripRequestAnnotations(body []byte) ([]byte, bool, error) {
	if turn == nil || turn.annotations == nil {
		return bytes.Clone(body), false, nil
	}
	cleaned, changed, err := turn.annotations.StripJSON(body)
	if err != nil {
		return nil, false, fmt.Errorf("%w: clean client annotations", ErrExecutionFailed)
	}
	return cleaned, changed, nil
}

func (turn *PipelineTurn) ApplyResponse(
	ctx context.Context,
	input ResponseMessage,
) (ResponseMessage, error) {
	if turn == nil {
		return ResponseMessage{}, fmt.Errorf("%w: Pipeline Turn is nil", ErrExecutionFailed)
	}
	output := cloneResponse(input)
	var err error
	for index := len(turn.turns) - 1; index >= 0; index-- {
		output, err = turn.turns[index].ApplyResponse(ctx, output)
		if err != nil {
			return ResponseMessage{}, err
		}
	}
	return output, nil
}

func compileStage(stage, parameter, source string, limits Limits) (*goja.Program, error) {
	if source == "" {
		return nil, nil
	}
	if len(source) > limits.MaximumScriptBytes || !utf8.ValidString(source) || hasForbiddenSourceControl(source) {
		return nil, fmt.Errorf("%w: %s JavaScript source is not bounded UTF-8", ErrInvalidPolicy, stage)
	}
	wrapper := "(function(" + parameter + ", context, runtime) {\n\"use strict\";\n" + source + "\n})"
	program, err := goja.Compile("vibermate-"+stage+"-transform.js", wrapper, true)
	if err != nil {
		return nil, fmt.Errorf("%w: compile %s JavaScript: %v", ErrInvalidPolicy, stage, err)
	}
	return program, nil
}

func hasForbiddenSourceControl(value string) bool {
	for _, character := range value {
		if (unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t') ||
			character == unicode.ReplacementChar {
			return true
		}
	}
	return false
}

type RequestMessage struct {
	Method  string
	Path    string
	Headers http.Header
	Body    []byte
}

type ResponseMessage struct {
	StatusCode int
	Streaming  bool
	EventName  string
	Headers    http.Header
	Body       []byte
}

// RuntimeMetadata is launcher-observed context exposed to JavaScript as a
// fresh snapshot for each stage. It carries no filesystem, process, network,
// clock, credential, or other ambient authority.
type RuntimeMetadata struct {
	LocalUserName          string
	HomeDirectory          string
	OperatingSystem        string
	OperatingSystemVersion string
	Architecture           string
	TimeZone               string
	WorkspaceRoot          string
	WorkspaceLabel         string
	TurnStartedAt          time.Time
}

type runtimeMetadataJSON struct {
	User struct {
		Name          string `json:"name"`
		HomeDirectory string `json:"homeDirectory"`
	} `json:"user"`
	Device struct {
		OperatingSystem        string `json:"operatingSystem"`
		OperatingSystemVersion string `json:"operatingSystemVersion"`
		Architecture           string `json:"architecture"`
		TimeZone               string `json:"timeZone"`
	} `json:"device"`
	Workspace struct {
		Root  string `json:"root"`
		Label string `json:"label"`
	} `json:"workspace"`
	Turn struct {
		StartedAt string `json:"startedAt"`
	} `json:"turn"`
}

func (metadata RuntimeMetadata) encode() (runtimeMetadataJSON, error) {
	values := []struct {
		name  string
		value string
		limit int
	}{
		{"local user name", metadata.LocalUserName, 128},
		{"home directory", metadata.HomeDirectory, 4096},
		{"operating system", metadata.OperatingSystem, 64},
		{"operating system version", metadata.OperatingSystemVersion, 256},
		{"architecture", metadata.Architecture, 64},
		{"time zone", metadata.TimeZone, 128},
		{"workspace root", metadata.WorkspaceRoot, 4096},
		{"workspace label", metadata.WorkspaceLabel, 256},
	}
	for _, item := range values {
		if !validMetadataText(item.value, item.limit) {
			return runtimeMetadataJSON{}, fmt.Errorf("%w: %s is invalid", ErrExecutionFailed, item.name)
		}
	}
	var encoded runtimeMetadataJSON
	encoded.User.Name = metadata.LocalUserName
	encoded.User.HomeDirectory = metadata.HomeDirectory
	encoded.Device.OperatingSystem = metadata.OperatingSystem
	encoded.Device.OperatingSystemVersion = metadata.OperatingSystemVersion
	encoded.Device.Architecture = metadata.Architecture
	encoded.Device.TimeZone = metadata.TimeZone
	encoded.Workspace.Root = metadata.WorkspaceRoot
	encoded.Workspace.Label = metadata.WorkspaceLabel
	if !metadata.TurnStartedAt.IsZero() {
		if metadata.TurnStartedAt.Year() < 1 || metadata.TurnStartedAt.Year() > 9999 {
			return runtimeMetadataJSON{}, fmt.Errorf("%w: Turn start time is invalid", ErrExecutionFailed)
		}
		encoded.Turn.StartedAt = metadata.TurnStartedAt.UTC().Format(time.RFC3339Nano)
	}
	return encoded, nil
}

func validMetadataText(value string, maximumBytes int) bool {
	if len(value) > maximumBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

type ContextSnapshot struct {
	encoded []byte
}

func (snapshot ContextSnapshot) clone() []byte {
	if len(snapshot.encoded) == 0 {
		return []byte("{}")
	}
	return bytes.Clone(snapshot.encoded)
}

type Turn struct {
	program     Program
	metadata    []byte
	annotations *clientannotation.Signer

	mu      sync.Mutex
	context []byte

	requestApplied bool
	requestInput   scriptMessage
	requestOutput  scriptMessage
}

func (turn *Turn) HasRequest() bool {
	return turn != nil && turn.program.HasRequest()
}

func (turn *Turn) HasResponse() bool {
	return turn != nil && turn.program.HasResponse()
}

func (program Program) NewTurn() *Turn {
	return program.newTurn(runtimeMetadataJSON{}, nil)
}

func (program Program) newTurn(
	metadata runtimeMetadataJSON,
	annotations *clientannotation.Signer,
) *Turn {
	encoded, _ := json.Marshal(metadata)
	return &Turn{
		program: program, context: []byte("{}"), metadata: encoded,
		annotations: annotations,
	}
}

func (program Program) NewTurnWithContext(snapshot ContextSnapshot) *Turn {
	turn := program.newTurn(runtimeMetadataJSON{}, nil)
	turn.context = snapshot.clone()
	return turn
}

func (turn *Turn) ContextSnapshot() ContextSnapshot {
	if turn == nil {
		return ContextSnapshot{encoded: []byte("{}")}
	}
	turn.mu.Lock()
	defer turn.mu.Unlock()
	return ContextSnapshot{encoded: bytes.Clone(turn.context)}
}

func (turn *Turn) ApplyRequest(ctx context.Context, input RequestMessage) (RequestMessage, error) {
	cloned := cloneRequest(input)
	if turn == nil {
		return RequestMessage{}, fmt.Errorf("%w: Turn is nil", ErrExecutionFailed)
	}
	if turn.program.request == nil {
		return cloned, nil
	}
	message := scriptMessage{
		Method:  input.Method,
		Path:    input.Path,
		Headers: input.Headers,
		Body:    input.Body,
	}
	output, err := turn.apply(ctx, "request", turn.program.request, message)
	if err != nil {
		return RequestMessage{}, err
	}
	return RequestMessage{
		Method:  input.Method,
		Path:    input.Path,
		Headers: output.Headers,
		Body:    output.Body,
	}, nil
}

func (turn *Turn) ApplyResponse(ctx context.Context, input ResponseMessage) (ResponseMessage, error) {
	cloned := cloneResponse(input)
	if turn == nil {
		return ResponseMessage{}, fmt.Errorf("%w: Turn is nil", ErrExecutionFailed)
	}
	if turn.program.response == nil {
		return cloned, nil
	}
	message := scriptMessage{
		StatusCode: input.StatusCode,
		Streaming:  input.Streaming,
		EventName:  input.EventName,
		Headers:    input.Headers,
		Body:       input.Body,
	}
	output, err := turn.apply(ctx, "response", turn.program.response, message)
	if err != nil {
		return ResponseMessage{}, err
	}
	return ResponseMessage{
		StatusCode: input.StatusCode,
		Streaming:  input.Streaming,
		EventName:  input.EventName,
		Headers:    output.Headers,
		Body:       output.Body,
	}, nil
}

type scriptMessage struct {
	Method     string
	Path       string
	StatusCode int
	Streaming  bool
	EventName  string
	Headers    http.Header
	Body       []byte
}

type scriptMessageJSON struct {
	Method     string              `json:"method,omitempty"`
	Path       string              `json:"path,omitempty"`
	StatusCode int                 `json:"statusCode,omitempty"`
	Streaming  bool                `json:"streaming"`
	EventName  string              `json:"eventName,omitempty"`
	Headers    map[string][]string `json:"headers"`
	Body       string              `json:"body"`
}

func (turn *Turn) apply(
	ctx context.Context,
	stage string,
	program *goja.Program,
	input scriptMessage,
) (scriptMessage, error) {
	if ctx == nil {
		return scriptMessage{}, fmt.Errorf("%w: %s context is nil", ErrExecutionFailed, stage)
	}
	if err := ctx.Err(); err != nil {
		return scriptMessage{}, fmt.Errorf("%w: %s context: %v", ErrExecutionFailed, stage, err)
	}
	turn.mu.Lock()
	defer turn.mu.Unlock()

	if len(input.Body) > turn.program.limits.MaximumBodyBytes || !utf8.Valid(input.Body) {
		return scriptMessage{}, fmt.Errorf("%w: %s Body is not bounded UTF-8", ErrInvalidOutput, stage)
	}
	headers, err := normalizeInputHeaders(input.Headers, turn.program.limits)
	if err != nil {
		return scriptMessage{}, err
	}
	canonicalInput := cloneScriptMessage(input)
	canonicalInput.Headers = make(http.Header, len(headers))
	for name, values := range headers {
		canonicalInput.Headers[name] = append([]string(nil), values...)
	}
	if stage == "request" && turn.requestApplied {
		if !equalScriptMessage(canonicalInput, turn.requestInput) {
			return scriptMessage{}, fmt.Errorf("%w: request stage already applied to a different message", ErrExecutionFailed)
		}
		return cloneScriptMessage(turn.requestOutput), nil
	}
	encodedMessage, err := json.Marshal(scriptMessageJSON{
		Method: input.Method, Path: input.Path, StatusCode: input.StatusCode,
		Streaming: input.Streaming, EventName: input.EventName,
		Headers: headers, Body: string(input.Body),
	})
	if err != nil {
		return scriptMessage{}, fmt.Errorf("%w: encode %s input", ErrExecutionFailed, stage)
	}

	runtime := goja.New()
	runtime.SetMaxCallStackSize(turn.program.limits.MaximumCallStackDepth)
	removeAmbientCapabilities(runtime)
	messageValue, err := parseJSON(runtime, encodedMessage)
	if err != nil {
		return scriptMessage{}, fmt.Errorf("%w: build %s message", ErrExecutionFailed, stage)
	}
	contextValue, err := parseJSON(runtime, turn.context)
	if err != nil {
		return scriptMessage{}, fmt.Errorf("%w: restore Turn Context", ErrExecutionFailed)
	}
	runtimeValue, err := parseJSON(runtime, turn.metadata)
	if err != nil {
		return scriptMessage{}, fmt.Errorf("%w: restore Runtime Metadata", ErrExecutionFailed)
	}
	if err := installRuntimeCapabilities(runtime, runtimeValue, turn.annotations); err != nil {
		return scriptMessage{}, fmt.Errorf("%w: install Runtime capabilities", ErrExecutionFailed)
	}

	functionValue, err := runtime.RunProgram(program)
	if err != nil {
		return scriptMessage{}, classifyRuntimeError(stage, err)
	}
	function, ok := goja.AssertFunction(functionValue)
	if !ok {
		return scriptMessage{}, fmt.Errorf("%w: %s program is not callable", ErrExecutionFailed, stage)
	}
	executionContext, cancelExecution := context.WithTimeout(
		ctx,
		turn.program.limits.MaximumExecutionDuration,
	)
	interruptDone := make(chan struct{})
	stopContext := context.AfterFunc(executionContext, func() {
		runtime.Interrupt(context.Cause(executionContext))
		close(interruptDone)
	})
	output, contextOutput, stageErr := executeAndExportStage(
		runtime,
		function,
		messageValue,
		contextValue,
		runtimeValue,
		input,
		turn.program.limits,
		stage,
	)
	if !stopContext() {
		<-interruptDone
	}
	cancelExecution()
	runtime.ClearInterrupt()
	if stageErr != nil {
		return scriptMessage{}, stageErr
	}
	turn.context = contextOutput
	if stage == "request" {
		turn.requestApplied = true
		turn.requestInput = canonicalInput
		turn.requestOutput = cloneScriptMessage(output)
	}
	return output, nil
}

func executeAndExportStage(
	runtime *goja.Runtime,
	function goja.Callable,
	messageValue goja.Value,
	contextValue goja.Value,
	runtimeValue goja.Value,
	input scriptMessage,
	limits Limits,
	stage string,
) (output scriptMessage, contextOutput []byte, err error) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		runtimeErr, ok := recovered.(error)
		if !ok {
			panic(recovered)
		}
		switch runtimeErr.(type) {
		case *goja.Exception, *goja.InterruptedError, *goja.StackOverflowError:
			output = scriptMessage{}
			contextOutput = nil
			err = classifyRuntimeError(stage, runtimeErr)
		default:
			panic(recovered)
		}
	}()
	if _, callErr := function(
		goja.Undefined(),
		messageValue,
		contextValue,
		runtimeValue,
	); callErr != nil {
		return scriptMessage{}, nil, classifyRuntimeError(stage, callErr)
	}
	output, err = exportMessage(runtime, messageValue, input, limits)
	if err != nil {
		return scriptMessage{}, nil, fmt.Errorf("%w: %s: %v", ErrInvalidOutput, stage, err)
	}
	contextOutput, err = exportContext(runtime, contextValue, limits)
	if err != nil {
		return scriptMessage{}, nil, fmt.Errorf("%w: %s Context: %v", ErrInvalidOutput, stage, err)
	}
	return output, contextOutput, nil
}

func installRuntimeCapabilities(
	runtime *goja.Runtime,
	runtimeValue goja.Value,
	annotations *clientannotation.Signer,
) error {
	object := runtimeValue.ToObject(runtime)
	annotationObject := runtime.NewObject()
	if annotations != nil {
		if err := annotationObject.Set(
			"create",
			func(kind, text string) (string, error) {
				annotation, err := annotations.Issue(kind, text)
				if err != nil {
					return "", errors.New("client annotation is invalid")
				}
				return annotation, nil
			},
		); err != nil {
			return err
		}
	}
	return object.Set("annotations", annotationObject)
}

func removeAmbientCapabilities(runtime *goja.Runtime) {
	for _, name := range []string{
		"fetch", "require", "process", "Deno", "Bun", "XMLHttpRequest",
		"WebSocket", "Date", "eval", "Function",
	} {
		_ = runtime.Set(name, goja.Undefined())
	}
	if mathObject := runtime.Get("Math"); mathObject != nil && !goja.IsUndefined(mathObject) {
		_ = mathObject.ToObject(runtime).Set("random", goja.Undefined())
	}
}

func parseJSON(runtime *goja.Runtime, encoded []byte) (goja.Value, error) {
	jsonObject := runtime.Get("JSON").ToObject(runtime)
	parse, ok := goja.AssertFunction(jsonObject.Get("parse"))
	if !ok {
		return nil, errors.New("JSON.parse is unavailable")
	}
	return parse(jsonObject, runtime.ToValue(string(encoded)))
}

func classifyRuntimeError(stage string, err error) error {
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		if cause, ok := interrupted.Value().(error); ok {
			return fmt.Errorf("%w: %s JavaScript interrupted: %v", ErrExecutionFailed, stage, cause)
		}
	}
	// A script can build an exception from message content. Never propagate that
	// text across this capability boundary.
	return fmt.Errorf("%w: %s JavaScript raised an exception", ErrExecutionFailed, stage)
}

func exportMessage(
	runtime *goja.Runtime,
	value goja.Value,
	input scriptMessage,
	limits Limits,
) (scriptMessage, error) {
	object := value.ToObject(runtime)
	bodyValue := object.Get("body")
	if bodyValue == nil || bodyValue.ExportType() != reflect.TypeFor[string]() {
		return scriptMessage{}, errors.New("Body must remain a string")
	}
	body := []byte(bodyValue.String())
	if len(body) > limits.MaximumBodyBytes || !utf8.Valid(body) || strings.ContainsRune(string(body), unicode.ReplacementChar) {
		return scriptMessage{}, errors.New("Body is not bounded UTF-8")
	}
	headersValue := object.Get("headers")
	if headersValue == nil || goja.IsNull(headersValue) || goja.IsUndefined(headersValue) {
		return scriptMessage{}, errors.New("Headers must remain an object")
	}
	headers, err := exportHeaders(runtime, headersValue, limits)
	if err != nil {
		return scriptMessage{}, err
	}
	stripFramingHeaders(headers)
	if !bytes.Equal(body, input.Body) {
		stripRepresentationValidators(headers)
	}
	return scriptMessage{
		Method: input.Method, Path: input.Path, StatusCode: input.StatusCode,
		Streaming: input.Streaming, EventName: input.EventName,
		Headers: headers, Body: body,
	}, nil
}

func normalizeInputHeaders(input http.Header, limits Limits) (map[string][]string, error) {
	result := make(map[string][]string, len(input))
	for name, values := range input {
		lower := strings.ToLower(name)
		if !validHeaderName(lower) {
			return nil, fmt.Errorf("%w: input Header name is invalid", ErrInvalidOutput)
		}
		if _, duplicate := result[lower]; duplicate {
			return nil, fmt.Errorf("%w: input Header names collide case-insensitively", ErrInvalidOutput)
		}
		result[lower] = append([]string(nil), values...)
	}
	if err := validateHeaderValues(result, limits); err != nil {
		return nil, fmt.Errorf("%w: input Headers: %v", ErrInvalidOutput, err)
	}
	return result, nil
}

func exportHeaders(runtime *goja.Runtime, value goja.Value, limits Limits) (http.Header, error) {
	object := value.ToObject(runtime)
	if object.ClassName() != "Object" {
		return nil, errors.New("Headers must be a plain object")
	}
	valuesByName := make(map[string][]string)
	for _, name := range object.Keys() {
		lower := strings.ToLower(name)
		if !validHeaderName(lower) {
			return nil, fmt.Errorf("Header name %q is invalid", name)
		}
		if _, duplicate := valuesByName[lower]; duplicate {
			return nil, fmt.Errorf("Header name %q collides case-insensitively", name)
		}
		value := object.Get(name)
		var values []string
		switch {
		case value.ExportType() == reflect.TypeFor[string]():
			values = []string{value.String()}
		case value.ToObject(runtime).ClassName() == "Array":
			array := value.ToObject(runtime)
			length := int(array.Get("length").ToInteger())
			if length < 0 || length > limits.MaximumHeaderFields {
				return nil, fmt.Errorf("Header %q has too many values", name)
			}
			values = make([]string, length)
			for index := 0; index < length; index++ {
				entry := array.Get(fmt.Sprintf("%d", index))
				if entry == nil || entry.ExportType() != reflect.TypeFor[string]() {
					return nil, fmt.Errorf("Header %q values must be strings", name)
				}
				values[index] = entry.String()
			}
		default:
			return nil, fmt.Errorf("Header %q must be a string or string array", name)
		}
		valuesByName[lower] = values
	}
	if err := validateHeaderValues(valuesByName, limits); err != nil {
		return nil, err
	}
	result := make(http.Header, len(valuesByName))
	for name, values := range valuesByName {
		if len(values) == 0 {
			continue
		}
		result[textproto.CanonicalMIMEHeaderKey(name)] = append([]string(nil), values...)
	}
	return result, nil
}

func validateHeaderValues(headers map[string][]string, limits Limits) error {
	if len(headers) > limits.MaximumHeaderFields {
		return errors.New("too many Header fields")
	}
	total := 0
	for name, values := range headers {
		total += len(name)
		for _, value := range values {
			if len(value) > limits.MaximumHeaderValueBytes || !validHeaderValue(value) {
				return fmt.Errorf("Header %q value is invalid", name)
			}
			total += len(value)
		}
	}
	if total > limits.MaximumHeaderBytes {
		return errors.New("Headers exceed their byte limit")
	}
	return nil
}

func validHeaderName(value string) bool {
	if value == "" || !utf8.ValidString(value) {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character > 0x7f || !strings.ContainsRune("!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", rune(character)) {
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character == '\r' || character == '\n' || character == 0 || character == 0x7f ||
			(character < 0x20 && character != '\t') {
			return false
		}
	}
	return true
}

func stripFramingHeaders(headers http.Header) {
	for _, name := range []string{
		"Connection", "Content-Length", "Keep-Alive", "Proxy-Connection",
		"TE", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		headers.Del(name)
	}
}

func stripRepresentationValidators(headers http.Header) {
	for _, name := range []string{"Content-Encoding", "Content-MD5", "Digest", "ETag"} {
		headers.Del(name)
	}
}

func exportContext(runtime *goja.Runtime, value goja.Value, limits Limits) ([]byte, error) {
	if value == nil || goja.IsNull(value) || goja.IsUndefined(value) || value.ToObject(runtime).ClassName() != "Object" {
		return nil, errors.New("Context must remain a plain object")
	}
	count := 0
	exported, err := exportJSONValue(runtime, value, 0, &count, make(map[*goja.Object]bool), limits)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(exported)
	if err != nil || len(encoded) > limits.MaximumContextBytes {
		return nil, errors.New("Context exceeds its encoded byte limit")
	}
	return encoded, nil
}

func exportJSONValue(
	runtime *goja.Runtime,
	value goja.Value,
	depth int,
	count *int,
	ancestors map[*goja.Object]bool,
	limits Limits,
) (any, error) {
	*count++
	if *count > limits.MaximumContextValues || depth > limits.MaximumContextDepth {
		return nil, errors.New("Context structure exceeds its limit")
	}
	if value == nil || goja.IsUndefined(value) {
		return nil, errors.New("Context contains undefined")
	}
	if goja.IsNull(value) {
		return nil, nil
	}
	if _, callable := goja.AssertFunction(value); callable {
		return nil, errors.New("Context contains a function")
	}
	exported := value.Export()
	switch candidate := exported.(type) {
	case string:
		if !utf8.ValidString(candidate) || strings.ContainsRune(candidate, unicode.ReplacementChar) {
			return nil, errors.New("Context contains invalid UTF-8")
		}
		return candidate, nil
	case bool:
		return candidate, nil
	case int64:
		return candidate, nil
	case float64:
		if math.IsNaN(candidate) || math.IsInf(candidate, 0) {
			return nil, errors.New("Context contains a non-finite number")
		}
		return candidate, nil
	case nil:
		return nil, nil
	}
	object := value.ToObject(runtime)
	if ancestors[object] {
		return nil, errors.New("Context contains a cycle")
	}
	ancestors[object] = true
	defer delete(ancestors, object)
	switch object.ClassName() {
	case "Array":
		length := int(object.Get("length").ToInteger())
		if length < 0 || length > limits.MaximumContextValues {
			return nil, errors.New("Context array exceeds its limit")
		}
		result := make([]any, length)
		for index := 0; index < length; index++ {
			entry, err := exportJSONValue(runtime, object.Get(fmt.Sprintf("%d", index)), depth+1, count, ancestors, limits)
			if err != nil {
				return nil, err
			}
			result[index] = entry
		}
		return result, nil
	case "Object":
		result := make(map[string]any)
		for _, key := range object.Keys() {
			if !utf8.ValidString(key) || hasForbiddenContextKeyControl(key) {
				return nil, errors.New("Context contains an invalid key")
			}
			entry, err := exportJSONValue(runtime, object.Get(key), depth+1, count, ancestors, limits)
			if err != nil {
				return nil, err
			}
			result[key] = entry
		}
		return result, nil
	default:
		return nil, fmt.Errorf("Context contains unsupported %s value", object.ClassName())
	}
}

func hasForbiddenContextKeyControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) || character == unicode.ReplacementChar {
			return true
		}
	}
	return false
}

func cloneRequest(input RequestMessage) RequestMessage {
	return RequestMessage{
		Method: input.Method, Path: input.Path,
		Headers: input.Headers.Clone(), Body: bytes.Clone(input.Body),
	}
}

func cloneResponse(input ResponseMessage) ResponseMessage {
	return ResponseMessage{
		StatusCode: input.StatusCode,
		Streaming:  input.Streaming,
		EventName:  input.EventName,
		Headers:    input.Headers.Clone(), Body: bytes.Clone(input.Body),
	}
}

func cloneScriptMessage(input scriptMessage) scriptMessage {
	return scriptMessage{
		Method:     input.Method,
		Path:       input.Path,
		StatusCode: input.StatusCode,
		Streaming:  input.Streaming,
		EventName:  input.EventName,
		Headers:    input.Headers.Clone(),
		Body:       bytes.Clone(input.Body),
	}
}

func equalScriptMessage(left, right scriptMessage) bool {
	return left.Method == right.Method &&
		left.Path == right.Path &&
		left.StatusCode == right.StatusCode &&
		left.Streaming == right.Streaming &&
		left.EventName == right.EventName &&
		bytes.Equal(left.Body, right.Body) &&
		reflect.DeepEqual(left.Headers, right.Headers)
}
