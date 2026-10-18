package exchange

import (
	"net/http"
	"strings"
)

// rewriteMappedModelHeaders restores a configured client alias only when the
// provider reported that exact mapping target. A different server-selected
// model remains visible, including native safety reroutes. Callers pass the
// downstream header clone; upstream evidence keeps its original identity.
func rewriteMappedModelHeaders(headers http.Header, requestedModel, effectiveModel string) {
	if requestedModel == "" || effectiveModel == "" || requestedModel == effectiveModel {
		return
	}
	for name, values := range headers {
		if !strings.EqualFold(name, "openai-model") && !strings.EqualFold(name, "x-openai-model") {
			continue
		}
		for index, value := range values {
			if value == effectiveModel {
				values[index] = requestedModel
			}
		}
	}
}
