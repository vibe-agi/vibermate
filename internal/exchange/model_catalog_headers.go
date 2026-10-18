package exchange

import (
	"net/http"

	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamservice"
)

func scopeManagedModelETag(headers http.Header, request ClientRequest, selection frozenSelection, account providerauth.AccountRef) {
	if !isNativeCodexSelection(request, selection) || account.Validate() != nil {
		return
	}
	raw := upstreamservice.ModelValidator(headers, "X-Models-Etag")
	headers.Del("X-Models-Etag")
	tag := upstreamservice.ModelCatalogETag(raw, upstreamservice.ModelCatalogScope{
		EnvironmentID: selection.environmentID.String(), EnvironmentRevision: uint64(selection.environmentRevision),
		RouteID: selection.routeID.String(), RouteRevision: uint64(selection.routeRevision),
		EndpointID: selection.targetRef, EndpointRevision: uint64(selection.targetRevision),
		Origin: selection.target.Origin().String(), Account: account,
	})
	if tag != "" {
		headers.Set("X-Models-Etag", tag)
	}
}
