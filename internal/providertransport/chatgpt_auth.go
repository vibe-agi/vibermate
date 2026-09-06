package providertransport

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
)

const chatGPTAccountHeader = "Chatgpt-Account-Id"

// chatGPTAccountID extracts an outbound routing hint from the selected stored
// access token. It is NOT JWT authentication: the upstream verifies the token,
// and these claims never grant local permissions or select a different account.
// Opaque/non-JWT credentials remain usable with an explicit Set Header policy.
func chatGPTAccountID(token []byte) string {
	parts := bytes.Split(token, []byte("."))
	if len(parts) != 3 || len(parts[0]) == 0 || len(parts[1]) == 0 || len(parts[2]) == 0 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(string(parts[1]))
	if err != nil {
		return ""
	}
	defer clear(payload)
	var claims struct {
		Auth struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	accountID := claims.Auth.AccountID
	if len(accountID) == 0 || len(accountID) > 512 {
		return ""
	}
	for _, character := range accountID {
		if character <= ' ' || character > '~' {
			return ""
		}
	}
	return accountID
}
