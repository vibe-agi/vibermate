package exchange

import "github.com/vibe-agi/vibermate/internal/environment"

type ClientRequest struct {
	plan   environment.RequestPlan
	policy environment.PolicySet
}
