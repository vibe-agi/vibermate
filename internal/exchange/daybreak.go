package exchange

import (
	"context"
	"strings"

	"github.com/vibe-agi/vibermate/internal/cyberaccess"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

type accountCyberCatalog interface {
	AccountCyberPrograms(context.Context, providertransport.Request) (cyberaccess.Catalog, error)
}

// finalizeAccountDaybreak runs after model mapping and request transforms, but
// before the final provider representation is published. The validated draft
// supplies the exact frozen credential/egress scope for an optional model read.
func (pipeline *Pipeline) finalizeAccountDaybreak(ctx context.Context, options providertransport.RequestOptions) (providertransport.Request, error) {
	draft, err := providertransport.NewRequest(options)
	if err != nil {
		return providertransport.Request{}, err
	}
	account, managed := draft.AccountRef()
	if !managed || !account.DaybreakBlueEnabled || !upstreamendpoint.IsChatGPTCodexOrigin(draft.Target().Origin()) ||
		draft.RelativePath() != upstreamendpoint.ProviderRelativePath(draft.Target().Origin(), "v1/responses") {
		return draft, nil
	}
	reader, ok := pipeline.provider.(accountCyberCatalog)
	if !ok {
		return draft, nil
	}
	programs, err := reader.AccountCyberPrograms(ctx, draft)
	if err != nil {
		if ctx.Err() != nil {
			return providertransport.Request{}, ctx.Err()
		}
		// Unknown capability does not establish an entitlement. Preserve the
		// client's explicit choice, including Blue or Red, on discovery failure.
		return draft, nil
	}
	body, err := decodeBoundedContent(options.Body, strings.Join(options.Headers.Values("Content-Encoding"), ","))
	if err != nil {
		return providertransport.Request{}, err
	}
	updated, changed, err := cyberaccess.PreferBlue(body, programs)
	if err != nil {
		return providertransport.Request{}, err
	}
	if !changed {
		return draft, nil
	}
	options.Body = updated
	options.Headers = options.Headers.Clone()
	options.Headers.Del("Content-Encoding")
	options.Headers.Del("Content-Length")
	return providertransport.NewRequest(options)
}
