package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	modulePath = "github.com/vibe-agi/vibermate"
	// corpusDirectory holds the Claude Code 2.1.329 request bodies that
	// tool/wire-skeleton made committable for the S2 goldens; the
	// anthropicresponses goldens read them in place too.
	corpusDirectory = "internal/productruntime/testdata/protocolgolden/requests"
)

// corpusFixtures are the ten bodies of design 15.3, the corpus whose real
// input_tokens the calibration fixture records (the same list as
// internal/anthropicresponses/golden_test.go).
var corpusFixtures = []string{
	"claude.007",
	"claude-tools.007",
	"claude-tools.008",
	"claude-interactive.003",
	"claude-interactive.005",
	"claude-interactive-tools.004",
	"claude-interactive-tools.005",
	"claude-interactive-tools.006",
	"claude-models.003",
	"claude-models.007",
}

// Fixture roles in the probe plan.
const (
	// fixtureMain is a plain main request: system prompt, tools, one user
	// turn and the mid-conversation system message.
	fixtureMain = "claude.007"
	// fixtureLargestTools carries the most tools and the largest schemas.
	fixtureLargestTools = "claude-interactive-tools.006"
	// fixtureToolMedia replays a tool round whose results hold an image and
	// a PDF.
	fixtureToolMedia = "claude-tools.008"
	// fixtureToolText is the same round with text-only results: replayed
	// assistant text and calls, no media.
	fixtureToolText = "claude-tools.008-text-results"
	// fixtureToolTurnOne is the first request of that round.
	fixtureToolTurnOne = "claude-tools.007"
)

// findRepository walks up from start to the module root.
func findRepository(start string) (string, error) {
	directory, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		content, err := os.ReadFile(filepath.Join(directory, "go.mod"))
		if err == nil && bytes.HasPrefix(bytes.TrimSpace(content), []byte("module "+modulePath+"\n")) {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", fmt.Errorf("no %s checkout above %s; run from the repository or pass --repo", modulePath, start)
		}
		directory = parent
	}
}

type corpus struct {
	root   string
	bodies map[string][]byte
}

func loadCorpus(repository string) (*corpus, error) {
	loaded := &corpus{root: repository, bodies: map[string][]byte{}}
	for _, name := range append(append([]string{}, corpusFixtures...), fixtureToolText) {
		body, err := os.ReadFile(filepath.Join(repository, corpusDirectory, name+".json"))
		if err != nil {
			return nil, fmt.Errorf("corpus body %s: %w", name, err)
		}
		loaded.bodies[name] = body
	}
	return loaded, nil
}

func (loaded *corpus) body(name string) []byte {
	return bytes.Clone(loaded.bodies[name])
}

// claudeBody is a Claude Code request body under edit. Top-level members
// are edited in place; nested values are replaced whole.
type claudeBody struct {
	members map[string]json.RawMessage
	edits   []string
}

func parseClaudeBody(raw []byte) (*claudeBody, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil || members == nil {
		return nil, errors.New("corpus body is not a JSON object")
	}
	return &claudeBody{members: members}, nil
}

func (body *claudeBody) bytes() ([]byte, error) {
	return marshalNoEscape(body.members)
}

func (body *claudeBody) set(name string, value any, edit string) error {
	encoded, err := marshalNoEscape(value)
	if err != nil {
		return err
	}
	body.members[name] = encoded
	body.edits = append(body.edits, edit)
	return nil
}

// capOutput sets max_tokens, which the encoder sends as max_output_tokens
// when it is below Claude Code's generic 32000 (design 6.1).
func (body *claudeBody) capOutput(tokens int) error {
	return body.set("max_tokens", tokens, fmt.Sprintf("max_tokens %d (sent as max_output_tokens)", tokens))
}

// setEffort sets output_config.effort, which maps 1:1 to reasoning.effort.
func (body *claudeBody) setEffort(effort string) error {
	config := map[string]json.RawMessage{}
	if raw, ok := body.members["output_config"]; ok {
		if err := json.Unmarshal(raw, &config); err != nil {
			return errors.New("output_config is not an object")
		}
	}
	encoded, _ := json.Marshal(effort)
	config["effort"] = encoded
	return body.set("output_config", config, "output_config.effort "+effort)
}

func (body *claudeBody) messages() ([]map[string]json.RawMessage, error) {
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(body.members["messages"], &messages); err != nil {
		return nil, errors.New("messages is not an array of objects")
	}
	return messages, nil
}

func messageRole(message map[string]json.RawMessage) string {
	var role string
	_ = json.Unmarshal(message["role"], &role)
	return role
}

// lastUserBlocks returns the index of the last user message and its content
// as blocks (a string content becomes one text block).
func lastUserBlocks(messages []map[string]json.RawMessage) (int, []map[string]any, error) {
	for index := len(messages) - 1; index >= 0; index-- {
		if messageRole(messages[index]) != "user" {
			continue
		}
		var text string
		if json.Unmarshal(messages[index]["content"], &text) == nil {
			return index, []map[string]any{{"type": "text", "text": text}}, nil
		}
		var blocks []map[string]any
		if err := json.Unmarshal(messages[index]["content"], &blocks); err != nil {
			return 0, nil, errors.New("user content is neither text nor blocks")
		}
		return index, blocks, nil
	}
	return 0, nil, errors.New("no user message")
}

func (body *claudeBody) setUserBlocks(messages []map[string]json.RawMessage, index int, blocks []map[string]any, edit string) error {
	encoded, err := marshalNoEscape(blocks)
	if err != nil {
		return err
	}
	messages[index]["content"] = encoded
	return body.set("messages", messages, edit)
}

// replaceLastUserText replaces the last text block of the last user message.
func (body *claudeBody) replaceLastUserText(text, edit string) error {
	messages, err := body.messages()
	if err != nil {
		return err
	}
	index, blocks, err := lastUserBlocks(messages)
	if err != nil {
		return err
	}
	for at := len(blocks) - 1; at >= 0; at-- {
		if blocks[at]["type"] == "text" {
			blocks[at] = map[string]any{"type": "text", "text": text}
			return body.setUserBlocks(messages, index, blocks, edit)
		}
	}
	return errors.New("the last user message has no text block")
}

// appendUserText adds a text block to the last user message.
func (body *claudeBody) appendUserText(text, edit string) error {
	messages, err := body.messages()
	if err != nil {
		return err
	}
	index, blocks, err := lastUserBlocks(messages)
	if err != nil {
		return err
	}
	blocks = append(blocks, map[string]any{"type": "text", "text": text})
	return body.setUserBlocks(messages, index, blocks, edit)
}

// appendTurn adds an assistant message and the user message that answers
// it, as Claude Code replays a turn.
func (body *claudeBody) appendTurn(assistant, user []map[string]any, edit string) error {
	messages, err := body.messages()
	if err != nil {
		return err
	}
	for _, message := range []struct {
		role   string
		blocks []map[string]any
	}{{"assistant", assistant}, {"user", user}} {
		role, _ := json.Marshal(message.role)
		content, err := marshalNoEscape(message.blocks)
		if err != nil {
			return err
		}
		messages = append(messages, map[string]json.RawMessage{"role": role, "content": content})
	}
	return body.set("messages", messages, edit)
}

func marshalNoEscape(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

// overflowText is synthetic filler of at least tokens real tokens: groups
// of three digits after a space, which no tokenizer packs below one token
// per four bytes, from a fixed sequence so every run sends the same bytes.
func overflowText(tokens int) string {
	var builder strings.Builder
	builder.Grow(tokens * 4)
	state := uint32(2463534242)
	for index := 0; index < tokens; index++ {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		fmt.Fprintf(&builder, " %03d", state%1000)
	}
	return builder.String()
}

// reasoningTask is the synthetic prompt of probe Q8: work that keeps a model
// reasoning for a long time, with no tool to shortcut it.
const reasoningTask = "Synthetic probe task, answer without tools. Let a(n) be the number of " +
	"permutations p of {1..n} with p(i) != i and p(i) != i+1 (mod n) for every i. " +
	"Compute a(n) for every n from 3 to 14 by two independent methods (an " +
	"inclusion-exclusion formula and an explicit recurrence), check that the two " +
	"agree for every n, then give the prime factorisation of a(13) + a(14). " +
	"Reply with the twelve values and the factorisation only."
