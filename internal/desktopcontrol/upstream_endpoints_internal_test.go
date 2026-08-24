package desktopcontrol

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/vibe-agi/vibermate/internal/modelcatalog"
)

func TestModelCatalogTimeoutHasAStableControlReason(t *testing.T) {
	t.Parallel()

	spec := classifyModelCatalogError(errors.Join(
		modelcatalog.ErrCatalogUnavailable,
		context.DeadlineExceeded,
	))
	if spec.status != http.StatusGatewayTimeout ||
		spec.reason != ReasonCode("model_catalog_timeout") {
		t.Fatalf("timeout problem = %+v", spec)
	}
}

func TestModelCatalogAuthenticationRejectionHasAStableControlReason(t *testing.T) {
	t.Parallel()

	spec := classifyModelCatalogError(errors.Join(
		modelcatalog.ErrCatalogUnavailable,
		&modelcatalog.EndpointHTTPError{StatusCode: http.StatusUnauthorized},
	))
	if spec.status != http.StatusBadGateway ||
		spec.reason != ReasonCode("model_catalog_authentication_rejected") {
		t.Fatalf("authentication problem = %+v", spec)
	}
}
