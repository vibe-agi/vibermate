package openairesponses

import (
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/tokenestimate"
)

// CountInputTokens estimates the input tokens of the body this side would
// send for request (design 10). It runs the item plan the encoder writes, so
// the count reflects what would be sent, not what the client sent: the
// billing line is already gone, deferred tools nothing loaded are left out,
// and reasoning that would not be replayed is not counted.
func (side *ProviderSide) CountInputTokens(
	providerContext protocolpath.ProviderContext,
	request protocolcore.Request,
) (int, error) {
	plan, err := side.planRequest(providerContext, request)
	if err != nil {
		return 0, err
	}
	return plan.estimateTokens(), nil
}

// estimateTokens counts the planned values with the tokenestimate rules.
// Replayed reasoning counts its visible text only; its encrypted state is
// opaque to the estimate.
func (plan *requestPlan) estimateTokens() int {
	total := 0
	for _, instruction := range plan.instructions {
		total += tokenestimate.Text(instruction)
	}
	for _, tool := range plan.tools {
		total += tokenestimate.PerTool + tokenestimate.Text(tool.name) +
			tokenestimate.Text(tool.description) + tokenestimate.Schema(len(tool.parameters))
	}
	if plan.format != nil {
		total += tokenestimate.Schema(len(plan.format.schema))
	}
	for _, item := range plan.items {
		total += tokenestimate.PerItem
		switch item.kind {
		case planItemMessage, planItemFunctionCallOutput:
			for _, part := range item.parts {
				total += plan.estimatePart(part)
			}
		case planItemFunctionCall:
			total += tokenestimate.Text(item.name) + tokenestimate.Text(string(item.arguments))
		case planItemReasoning:
			total += tokenestimate.Text(item.summary) + tokenestimate.Text(item.content)
		}
	}
	return total
}

func (plan *requestPlan) estimatePart(part planPart) int {
	switch part.kind {
	case planPartInputImage:
		switch {
		case plan.imageDetail == "low":
			return tokenestimate.LowDetailImageTokens
		case part.media.Data != "":
			return tokenestimate.InlineImage(part.media.MediaType, part.media.Data)
		default:
			return tokenestimate.DefaultImageTokens
		}
	case planPartInputFile:
		switch {
		case part.media.Data == "":
			// A URL document's pages cannot be counted without fetching it.
			return tokenestimate.PDFPageTokens
		case part.media.MediaType == "" || part.media.MediaType == "application/pdf":
			return tokenestimate.InlinePDF(part.media.Data)
		default:
			// An inline plain-text payload: its decoded bytes as text.
			return tokenestimate.Schema(len(part.media.Data) * 3 / 4)
		}
	default:
		return tokenestimate.Text(part.prefix) + tokenestimate.Text(part.text) + tokenestimate.Text(part.suffix)
	}
}
