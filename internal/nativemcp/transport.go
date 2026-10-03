package nativemcp

import (
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
)

func headerMemory(headers http.Header) int64 {
	var bytes int64
	for name, values := range headers {
		bytes += int64(len(name) + 64)
		for _, value := range values {
			bytes += int64(len(value) + 32)
		}
	}
	return bytes
}

func exactTarget(request *http.Request, plan environment.ClientToolPlan) error {
	if request == nil || request.URL == nil || plan.Validate() != nil {
		return errProtocol
	}
	u := request.URL
	route := plan.Route()
	if u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" || u.Fragment != "" || u.Opaque != "" || u.Path != route.Path || "https://"+u.Host != route.TargetOrigin.String() || request.Host != "" && request.Host != u.Host {
		return errProtocol
	}
	return nil
}
func validateRequestHeaders(request *http.Request) error {
	for _, name := range []string{"Content-Type", "Mcp-Session-Id", "Mcp-Protocol-Version", "Last-Event-Id", "Authorization", "Cookie"} {
		if len(request.Header.Values(name)) > 1 {
			return errProtocol
		}
	}
	if request.Header.Get("Mcp-Method") != "" || request.Header.Get("Mcp-Name") != "" || request.Header.Get("Content-Encoding") != "" {
		return errProtocol
	}
	if v := request.Header.Get("Mcp-Protocol-Version"); v != "" && v != "2025-06-18" {
		return errProtocol
	}
	if request.Method == http.MethodPost {
		media, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			return errProtocol
		}
	}
	return nil
}
func outbound(ctx context.Context, incoming *http.Request, body []byte, plan environment.ClientToolPlan) (*http.Request, error) {
	if err := exactTarget(incoming, plan); err != nil {
		return nil, err
	}
	if err := validateRequestHeaders(incoming); err != nil {
		return nil, err
	}
	r := incoming.Clone(ctx)
	r.RequestURI = ""
	r.GetBody = nil
	r.Header = incoming.Header.Clone()
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.TransferEncoding = nil
	r.Trailer = nil
	for _, value := range r.Header.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			r.Header.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Proxy-Authorization", "Proxy-Authenticate", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		r.Header.Del(name)
	}
	return r, nil
}
func nativeProbe(plan environment.ClientToolPlan) offlinehold.ProbeTarget {
	route := plan.Route()
	return offlinehold.ProbeTarget{Kind: offlinehold.EgressAuxiliary, Transport: offlinehold.ProbeTransportStrictTLS, TargetRef: route.ID.String(), NetworkOrigin: route.TargetOrigin.String(), HTTPAuthority: route.TargetOrigin.HTTPAuthority(), TLSServerName: route.TargetOrigin.Host(), PlanRevision: uint64(plan.EnvironmentRevision()), PlanDigest: plan.EnvironmentDigest().String(), EgressPolicy: route.EgressProfile.Policy}
}

// A receipt is consumed once in this executor even when verification fails.
// Verification is point-in-time; revocation cannot retract already sent bytes.
func (e *executor) sendCommitted(ctx context.Context, request *http.Request, plan environment.ClientToolPlan, receipt nativetool.StartReceipt) (*http.Response, error) {
	if ctx == nil || ctx.Err() != nil || e.dependencies.OwnerContext.Err() != nil {
		return nil, nativetool.ErrDenied
	}
	e.mu.Lock()
	id := receipt.OperationID()
	if e.closed || id == "" || e.claimed[id] {
		e.mu.Unlock()
		return nil, nativetool.ErrReplay
	}
	if !e.budget.acquire(128) {
		e.mu.Unlock()
		return nil, errBudget
	}
	e.claimed[id] = true
	e.mu.Unlock()
	if err := exactTarget(request, plan); err != nil {
		return nil, err
	}
	if err := e.dependencies.Store.VerifyStartReceipt(ctx, receipt, e.dependencies.Clock().UTC()); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return e.dependencies.Transport.RoundTrip(request, plan.Route().EgressProfile.Policy)
}
