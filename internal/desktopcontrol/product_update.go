package desktopcontrol

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/productupdate"
	"net/http"
)

type ProductUpdateReader interface {
	Check(context.Context) (productupdate.Result, error)
}

func (h *Handler) checkProductUpdate(w http.ResponseWriter, r *http.Request) {
	a, ok := r.Context().Value(nativeManagementKey{}).(nativeManagementAuthority)
	if !ok || a.scope != ScopeWrite || !a.owner.Valid() {
		writeProblem(w, 403, ReasonCode("product_update_forbidden"))
		return
	}
	body, err := readJSONBody(r)
	if err != nil || defaultObject(body) == nil || r.URL.RawQuery != "" {
		writeProblem(w, 422, ReasonInvalidRequest)
		return
	}
	if h.productUpdates == nil {
		writeProblem(w, 503, ReasonCode("product_update_unavailable"))
		return
	}
	result, err := h.productUpdates.Check(r.Context())
	if err != nil {
		writeProblem(w, 503, ReasonCode("product_update_unavailable"))
		return
	}
	writeJSON(w, 200, result)
}
