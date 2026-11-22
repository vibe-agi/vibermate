package openairesponses

import (
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// ValidateTransformedRequest re-admits the provider body after request
// scripts ran, with the compatible decoder R->R uses (design 6.9), bounded at
// the provider side's own request limit.
func (side *ProviderSide) ValidateTransformedRequest(body []byte) error {
	_, _, err := side.admission.DecodeCompatibleClientRequest(body)
	return err
}

// AdmitRequest refuses, right after client decode, an attachment the provider
// cannot carry: an image or document that names a provider file (the
// Anthropic Files API) has no Responses form. The class renders as the
// capability token that makes Claude Code strip the attachment and retry
// (design 6.3, 8.3). nil admits.
func (side *ProviderSide) AdmitRequest(request protocolcore.Request) *protocolcore.ProviderError {
	refuse := func(media *protocolcore.Media) *protocolcore.ProviderError {
		if media == nil || media.FileID == "" {
			return nil
		}
		class := protocolcore.ProviderErrorImage
		if media.Kind == protocolcore.MediaDocument {
			class = protocolcore.ProviderErrorDocument
		}
		return &protocolcore.ProviderError{Class: class}
	}
	for _, message := range request.Messages {
		for _, block := range message.Blocks {
			switch block.Kind {
			case protocolcore.BlockMedia:
				if refused := refuse(block.Media); refused != nil {
					return refused
				}
			case protocolcore.BlockToolResult:
				if block.ToolResult.Parts == nil {
					continue
				}
				for _, part := range block.ToolResult.Parts.Items {
					if refused := refuse(part.Media); refused != nil {
						return refused
					}
				}
			}
		}
	}
	return nil
}
