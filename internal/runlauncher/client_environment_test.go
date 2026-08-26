package runlauncher

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/clienttarget"
)

func TestCaptureCreateTargetFactsAreUsefulAndSecretFree(t *testing.T) {
	t.Parallel()
	const secret = "must-not-appear-in-capture-control"
	input := clientEnvironmentInput(clienttarget.FromEnvironment([]string{
		"ANTHROPIC_BASE_URL=http://127.0.0.1:23333",
		"ANTHROPIC_AUTH_TOKEN=" + secret,
		"OPENAI_API_KEY=" + secret,
	}))
	payload, err := json.Marshal(capturecontrol.CreateRequest{
		EnvironmentID:     "system_transparent",
		CWD:               "/workspace",
		Command:           []string{"claude"},
		ExecutablePath:    "/usr/local/bin/claude",
		ClientEnvironment: input,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte(secret)) {
		t.Fatalf("Capture create payload contains credential bytes: %s", payload)
	}
	if input.AnthropicBaseURL != "http://127.0.0.1:23333" ||
		!input.OpenAIAPIKeyPresent {
		t.Fatalf("client target input = %+v", input)
	}
}
