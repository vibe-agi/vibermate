package responseschat

import (
	"encoding/json"

	"github.com/openai/openai-go/v3/responses"
)

func oracle(value []byte) (responses.Response, error) {
	var response responses.Response
	err := json.Unmarshal(value, &response)
	return response, err
}
