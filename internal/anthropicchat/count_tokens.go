package anthropicchat

import "github.com/vibe-agi/vibermate/internal/protocolcore"

// DecodeCountTokensRequest decodes a /v1/messages/count_tokens body for a
// local estimate (design 10). It always uses the translated client input
// profile, so the estimate sees what a translated provider side would send
// (media, tool-result parts, reasoning seals, the dropped billing line), and
// it does not need max_tokens, which a count_tokens body never carries.
// Nothing it decodes is sent upstream.
func (codec *Codec) DecodeCountTokensRequest(
	body []byte,
) (protocolcore.Request, protocolcore.TranslationReport, error) {
	return codec.decodeClientRequest(body, requestDecodeTranslated, true)
}
