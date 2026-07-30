package anthropicchat

import anthropic "github.com/anthropics/anthropic-sdk-go"

func invalid() anthropic.Message {
	return anthropic.Message{}
}
