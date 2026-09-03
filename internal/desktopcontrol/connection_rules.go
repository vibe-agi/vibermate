package desktopcontrol

import (
	"context"
	"errors"
	"net/http"

	"github.com/vibe-agi/vibermate/internal/connectionpolicy"
)

// ReasonConnectionRulesUnavailable reports a runtime built without an editable
// rule set. It is not a permissive state: the proxy keeps evaluating whatever
// rules it was started with.
const ReasonConnectionRulesUnavailable ReasonCode = "connection_rules_unavailable"

// ConnectionRuleController is the editable rule set behind the control API.
type ConnectionRuleController interface {
	Current() connectionpolicy.Snapshot
	Replace(
		ctx context.Context,
		expectedRevision uint64,
		rules []connectionpolicy.Rule,
		mode connectionpolicy.Mode,
	) (connectionpolicy.Snapshot, error)
}

// ConnectionRuleInput is one rule as a person writes it. The match language is
// closed on purpose: a wildcard or regular expression is how an allow list
// quietly becomes an allow everything.
type ConnectionRuleInput struct {
	ID       string `json:"id"`
	Priority uint32 `json:"priority"`
	Decision string `json:"decision"`
	Match    string `json:"match"`
	Host     string `json:"host,omitempty"`
	Port     uint16 `json:"port,omitempty"`
}

// ConnectionRuleSetInput replaces the whole set. Rules are never changed one at
// a time, because a set that would not construct must be refused before
// anything is stored.
type ConnectionRuleSetInput struct {
	Rules []ConnectionRuleInput `json:"rules"`
	Mode  string                `json:"mode"`
}

type ConnectionRuleSetResponse struct {
	Revision uint64                `json:"revision"`
	Rules    []ConnectionRuleInput `json:"rules"`
	Mode     string                `json:"mode"`
}

func (input ConnectionRuleInput) rule() connectionpolicy.Rule {
	return connectionpolicy.Rule{
		ID:       input.ID,
		Priority: input.Priority,
		Decision: connectionpolicy.Decision(input.Decision),
		Match: connectionpolicy.Match{
			Kind: connectionpolicy.MatchKind(input.Match),
			Host: input.Host,
			Port: input.Port,
		},
	}
}

func connectionRuleView(rule connectionpolicy.Rule) ConnectionRuleInput {
	return ConnectionRuleInput{
		ID:       rule.ID,
		Priority: rule.Priority,
		Decision: string(rule.Decision),
		Match:    string(rule.Match.Kind),
		Host:     rule.Match.Host,
		Port:     rule.Match.Port,
	}
}

func connectionRuleSetView(
	snapshot connectionpolicy.Snapshot,
) ConnectionRuleSetResponse {
	response := ConnectionRuleSetResponse{
		Revision: snapshot.Revision,
		Rules:    make([]ConnectionRuleInput, 0, len(snapshot.Rules)),
		Mode:     string(snapshot.Mode),
	}
	for _, rule := range snapshot.Rules {
		response.Rules = append(response.Rules, connectionRuleView(rule))
	}
	return response
}

func (handler *Handler) getConnectionRules(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if handler.connectionRules == nil {
		writeProblem(
			writer,
			http.StatusServiceUnavailable,
			ReasonConnectionRulesUnavailable,
		)
		return
	}
	writeJSON(
		writer,
		http.StatusOK,
		connectionRuleSetView(handler.connectionRules.Current()),
	)
}

func (handler *Handler) replaceConnectionRules(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if handler.connectionRules == nil {
		writeProblem(
			writer,
			http.StatusServiceUnavailable,
			ReasonConnectionRulesUnavailable,
		)
		return
	}
	expected, _, err := mutationHeaders(request)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	body, err := readJSONBody(request)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	var input ConnectionRuleSetInput
	if decodeStrictJSON(body, &input) != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	rules := make([]connectionpolicy.Rule, 0, len(input.Rules))
	for _, rule := range input.Rules {
		rules = append(rules, rule.rule())
	}
	snapshot, err := handler.connectionRules.Replace(
		request.Context(),
		expected,
		rules,
		connectionpolicy.Mode(input.Mode),
	)
	if err != nil {
		status := http.StatusUnprocessableEntity
		reason := ReasonInvalidRequest
		if errors.Is(err, connectionpolicy.ErrRevisionConflict) {
			status = http.StatusConflict
			reason = ReasonRevisionConflict
		}
		writeProblem(writer, status, reason)
		return
	}
	writeJSON(writer, http.StatusOK, connectionRuleSetView(snapshot))
}
