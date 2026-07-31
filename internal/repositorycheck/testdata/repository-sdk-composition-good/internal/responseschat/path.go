package responseschat

import "encoding/json"

func valid(value []byte) bool {
	return json.Valid(value)
}
