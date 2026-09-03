// Package accountselector owns the bounded JavaScript decision that chooses
// one Account from a Route's frozen Account set. It has no credential,
// transport, persistence, filesystem, or network authority.
package accountselector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dop251/goja"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

var (
	ErrInvalidPolicy    = errors.New("Account Selector policy is invalid")
	ErrExecutionFailed  = errors.New("Account Selector execution failed")
	ErrInvalidSelection = errors.New("Account Selector result is invalid")
)

type Policy struct {
	JavaScript string `json:"javaScript"`
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
	MaximumAccounts          int
	MaximumCallStackDepth    int
	MaximumExecutionDuration time.Duration
}

func DefaultLimits() Limits {
	return Limits{
		MaximumScriptBytes:       64 << 10,
		MaximumBodyBytes:         16 << 20,
		MaximumHeaderFields:      128,
		MaximumHeaderBytes:       64 << 10,
		MaximumAccounts:          1024,
		MaximumCallStackDepth:    256,
		MaximumExecutionDuration: 2 * time.Second,
	}
}

func (limits Limits) validate() error {
	if limits.MaximumScriptBytes <= 0 || limits.MaximumBodyBytes <= 0 ||
		limits.MaximumHeaderFields <= 0 || limits.MaximumHeaderBytes <= 0 ||
		limits.MaximumAccounts <= 0 || limits.MaximumCallStackDepth <= 0 ||
		limits.MaximumExecutionDuration <= 0 {
		return fmt.Errorf("%w: limits must be positive", ErrInvalidPolicy)
	}
	return nil
}

type Program struct {
	program *goja.Program
	limits  Limits
}

func Compile(policy Policy, limits Limits) (Program, error) {
	if err := limits.validate(); err != nil {
		return Program{}, err
	}
	source := policy.JavaScript
	if source == "" || len(source) > limits.MaximumScriptBytes ||
		!utf8.ValidString(source) || hasForbiddenSourceControl(source) {
		return Program{}, fmt.Errorf("%w: JavaScript source is not bounded UTF-8", ErrInvalidPolicy)
	}
	wrapper := "(function(request, runtime, accounts, selection) {\n\"use strict\";\n" +
		source + "\n})"
	compiled, err := goja.Compile("vibermate-account-selector.js", wrapper, true)
	if err != nil {
		return Program{}, fmt.Errorf("%w: compile JavaScript: %v", ErrInvalidPolicy, err)
	}
	return Program{program: compiled, limits: limits}, nil
}

type Account struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type RuntimeMetadata struct {
	LocalUserName          string
	LoginUsername          string
	HomeDirectory          string
	OperatingSystem        string
	OperatingSystemVersion string
	Architecture           string
	TimeZone               string
	WorkspaceRoot          string
	WorkspaceLabel         string
	TurnStartedAt          time.Time
}

type Request struct {
	Method         string
	Path           string
	Headers        http.Header
	Body           []byte
	ClientProtocol string
	RequestedModel string
}

type Selection struct {
	AccountID string
}

type TurnOptions struct {
	Runtime  RuntimeMetadata
	Accounts []Account
}

type Turn struct {
	program  Program
	runtime  []byte
	accounts []byte
	allowed  map[string]struct{}

	mu        sync.Mutex
	applied   bool
	request   []byte
	selection Selection
}

func (program Program) NewTurn(options TurnOptions) (*Turn, error) {
	if program.program == nil || program.limits.validate() != nil {
		return nil, ErrInvalidPolicy
	}
	runtime, err := encodeRuntime(options.Runtime)
	if err != nil {
		return nil, err
	}
	if len(options.Accounts) == 0 || len(options.Accounts) > program.limits.MaximumAccounts {
		return nil, fmt.Errorf("%w: frozen Account set is empty or too large", ErrInvalidSelection)
	}
	accounts := make([]Account, len(options.Accounts))
	allowed := make(map[string]struct{}, len(options.Accounts))
	for index, account := range options.Accounts {
		if !validIdentifier(account.ID) || !validDisplayName(account.DisplayName) {
			return nil, fmt.Errorf("%w: frozen Account metadata is invalid", ErrInvalidSelection)
		}
		if _, duplicate := allowed[account.ID]; duplicate {
			return nil, fmt.Errorf("%w: frozen Account is duplicated", ErrInvalidSelection)
		}
		allowed[account.ID] = struct{}{}
		accounts[index] = account
	}
	encodedAccounts, err := json.Marshal(accounts)
	if err != nil {
		return nil, fmt.Errorf("%w: encode frozen Accounts", ErrExecutionFailed)
	}
	return &Turn{
		program: program, runtime: runtime, accounts: encodedAccounts, allowed: allowed,
	}, nil
}

func (turn *Turn) Select(ctx context.Context, request Request) (Selection, error) {
	if turn == nil || ctx == nil {
		return Selection{}, fmt.Errorf("%w: Turn or context is nil", ErrExecutionFailed)
	}
	if err := ctx.Err(); err != nil {
		return Selection{}, fmt.Errorf("%w: context: %v", ErrExecutionFailed, err)
	}
	encodedRequest, err := encodeRequest(request, turn.program.limits)
	if err != nil {
		return Selection{}, err
	}
	turn.mu.Lock()
	defer turn.mu.Unlock()
	if turn.applied {
		if !bytes.Equal(encodedRequest, turn.request) {
			return Selection{}, fmt.Errorf(
				"%w: selector already applied to a different request",
				ErrExecutionFailed,
			)
		}
		return turn.selection, nil
	}
	selection, err := turn.execute(ctx, encodedRequest)
	if err != nil {
		return Selection{}, err
	}
	turn.applied = true
	turn.request = bytes.Clone(encodedRequest)
	turn.selection = selection
	return selection, nil
}

func (turn *Turn) execute(ctx context.Context, request []byte) (Selection, error) {
	runtime := goja.New()
	runtime.SetMaxCallStackSize(turn.program.limits.MaximumCallStackDepth)
	requestValue, err := parseJSON(runtime, request)
	if err != nil {
		return Selection{}, fmt.Errorf("%w: build request", ErrExecutionFailed)
	}
	runtimeValue, err := parseJSON(runtime, turn.runtime)
	if err != nil {
		return Selection{}, fmt.Errorf("%w: restore Runtime Metadata", ErrExecutionFailed)
	}
	accountsValue, err := parseJSON(runtime, turn.accounts)
	if err != nil {
		return Selection{}, fmt.Errorf("%w: restore frozen Accounts", ErrExecutionFailed)
	}
	selectionValue := runtime.NewObject()
	if err := selectionValue.DefineDataProperty(
		"accountId",
		runtime.ToValue(""),
		goja.FLAG_TRUE,
		goja.FLAG_FALSE,
		goja.FLAG_TRUE,
	); err != nil {
		return Selection{}, fmt.Errorf("%w: build selection", ErrExecutionFailed)
	}
	if err := deepFreeze(runtime, requestValue, runtimeValue, accountsValue); err != nil {
		return Selection{}, fmt.Errorf("%w: freeze selector input", ErrExecutionFailed)
	}
	removeAmbientCapabilities(runtime)
	functionValue, err := runtime.RunProgram(turn.program.program)
	if err != nil {
		return Selection{}, classifyRuntimeError(err)
	}
	function, ok := goja.AssertFunction(functionValue)
	if !ok {
		return Selection{}, fmt.Errorf("%w: JavaScript is not callable", ErrExecutionFailed)
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
	_, callErr := function(
		goja.Undefined(), requestValue, runtimeValue, accountsValue, selectionValue,
	)
	if !stopContext() {
		<-interruptDone
	}
	cancelExecution()
	runtime.ClearInterrupt()
	if callErr != nil {
		return Selection{}, classifyRuntimeError(callErr)
	}
	accountID := selectionValue.Get("accountId")
	if accountID == nil || accountID.ExportType() != reflect.TypeFor[string]() {
		return Selection{}, fmt.Errorf("%w: accountId must be a string", ErrInvalidSelection)
	}
	selected := accountID.String()
	if _, allowed := turn.allowed[selected]; !allowed {
		return Selection{}, fmt.Errorf("%w: accountId is outside the frozen Account set", ErrInvalidSelection)
	}
	return Selection{AccountID: selected}, nil
}

type requestJSON struct {
	Method         string              `json:"method"`
	Path           string              `json:"path"`
	Headers        map[string][]string `json:"headers"`
	Body           string              `json:"body"`
	ClientProtocol string              `json:"protocol,omitempty"`
	RequestedModel string              `json:"requestedModel,omitempty"`
}

func encodeRequest(request Request, limits Limits) ([]byte, error) {
	if !validBoundedText(request.Method, 32, false) ||
		!validBoundedText(request.Path, 8192, false) || !strings.HasPrefix(request.Path, "/") ||
		!validBoundedText(request.ClientProtocol, 128, true) ||
		!validBoundedText(request.RequestedModel, 256, true) ||
		len(request.Body) > limits.MaximumBodyBytes || !utf8.Valid(request.Body) {
		return nil, fmt.Errorf("%w: request is invalid", ErrExecutionFailed)
	}
	headers := make(map[string][]string, len(request.Headers))
	headerBytes := 0
	if len(request.Headers) > limits.MaximumHeaderFields {
		return nil, fmt.Errorf("%w: request has too many Headers", ErrExecutionFailed)
	}
	for name, values := range request.Headers {
		lower := strings.ToLower(name)
		if !validBoundedText(lower, 256, false) {
			return nil, fmt.Errorf("%w: request Header name is invalid", ErrExecutionFailed)
		}
		if lower != "anthropic-beta" {
			return nil, fmt.Errorf("%w: request Header is unavailable", ErrExecutionFailed)
		}
		if _, duplicate := headers[lower]; duplicate {
			return nil, fmt.Errorf("%w: request Header names collide", ErrExecutionFailed)
		}
		cloned := make([]string, len(values))
		for index, value := range values {
			if !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
				return nil, fmt.Errorf("%w: request Header value is invalid", ErrExecutionFailed)
			}
			headerBytes += len(lower) + len(value)
			cloned[index] = value
		}
		headers[lower] = cloned
	}
	if headerBytes > limits.MaximumHeaderBytes {
		return nil, fmt.Errorf("%w: request Headers are too large", ErrExecutionFailed)
	}
	encoded, err := json.Marshal(requestJSON{
		Method: request.Method, Path: request.Path, Headers: headers, Body: string(request.Body),
		ClientProtocol: request.ClientProtocol, RequestedModel: request.RequestedModel,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: encode request", ErrExecutionFailed)
	}
	return encoded, nil
}

type runtimeJSON struct {
	User struct {
		Name          string `json:"name"`
		HomeDirectory string `json:"homeDirectory"`
	} `json:"user"`
	Login struct {
		Username string `json:"username"`
	} `json:"login"`
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

func encodeRuntime(metadata RuntimeMetadata) ([]byte, error) {
	values := []struct {
		value string
		limit int
	}{
		{metadata.LocalUserName, 128}, {metadata.HomeDirectory, 4096},
		{metadata.LoginUsername, 64},
		{metadata.OperatingSystem, 64}, {metadata.OperatingSystemVersion, 256},
		{metadata.Architecture, 64}, {metadata.TimeZone, 128},
		{metadata.WorkspaceRoot, 4096}, {metadata.WorkspaceLabel, 256},
	}
	for _, item := range values {
		if !validBoundedText(item.value, item.limit, true) {
			return nil, fmt.Errorf("%w: Runtime Metadata is invalid", ErrExecutionFailed)
		}
	}
	if metadata.LoginUsername != "" && !runtimeuser.ValidUsername(metadata.LoginUsername) {
		return nil, fmt.Errorf("%w: Runtime Metadata is invalid", ErrExecutionFailed)
	}
	var output runtimeJSON
	output.User.Name = metadata.LocalUserName
	output.User.HomeDirectory = metadata.HomeDirectory
	output.Login.Username = metadata.LoginUsername
	output.Device.OperatingSystem = metadata.OperatingSystem
	output.Device.OperatingSystemVersion = metadata.OperatingSystemVersion
	output.Device.Architecture = metadata.Architecture
	output.Device.TimeZone = metadata.TimeZone
	output.Workspace.Root = metadata.WorkspaceRoot
	output.Workspace.Label = metadata.WorkspaceLabel
	if !metadata.TurnStartedAt.IsZero() {
		if metadata.TurnStartedAt.Year() < 1 || metadata.TurnStartedAt.Year() > 9999 {
			return nil, fmt.Errorf("%w: Turn start time is invalid", ErrExecutionFailed)
		}
		output.Turn.StartedAt = metadata.TurnStartedAt.UTC().Format(time.RFC3339Nano)
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return nil, fmt.Errorf("%w: encode Runtime Metadata", ErrExecutionFailed)
	}
	return encoded, nil
}

func deepFreeze(runtime *goja.Runtime, values ...goja.Value) error {
	value, err := runtime.RunString(`(function freeze(value) {
  if (value !== null && typeof value === "object" && !Object.isFrozen(value)) {
    Object.keys(value).forEach(function (key) { freeze(value[key]); });
    Object.freeze(value);
  }
  return value;
})`)
	if err != nil {
		return err
	}
	freeze, ok := goja.AssertFunction(value)
	if !ok {
		return errors.New("freeze helper is not callable")
	}
	for _, item := range values {
		if _, err := freeze(goja.Undefined(), item); err != nil {
			return err
		}
	}
	return nil
}

func parseJSON(runtime *goja.Runtime, encoded []byte) (goja.Value, error) {
	jsonObject := runtime.Get("JSON").ToObject(runtime)
	parse, ok := goja.AssertFunction(jsonObject.Get("parse"))
	if !ok {
		return nil, errors.New("JSON.parse is unavailable")
	}
	return parse(jsonObject, runtime.ToValue(string(encoded)))
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

func classifyRuntimeError(err error) error {
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		if cause, ok := interrupted.Value().(error); ok {
			return fmt.Errorf("%w: JavaScript interrupted: %v", ErrExecutionFailed, cause)
		}
	}
	return fmt.Errorf("%w: JavaScript raised an exception", ErrExecutionFailed)
}

func hasForbiddenSourceControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t' ||
			character == unicode.ReplacementChar {
			return true
		}
	}
	return false
}

func validIdentifier(value string) bool {
	if !validBoundedText(value, 128, false) {
		return false
	}
	for _, character := range value {
		if character > unicode.MaxASCII ||
			!(character >= 'a' && character <= 'z') &&
				!(character >= 'A' && character <= 'Z') &&
				!(character >= '0' && character <= '9') &&
				character != '-' && character != '_' && character != '.' && character != ':' {
			return false
		}
	}
	return true
}

func validDisplayName(value string) bool {
	return validBoundedText(value, 256, false) && strings.TrimSpace(value) == value
}

func validBoundedText(value string, limit int, allowEmpty bool) bool {
	if (!allowEmpty && value == "") || len(value) > limit || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || character == unicode.ReplacementChar {
			return false
		}
	}
	return true
}
