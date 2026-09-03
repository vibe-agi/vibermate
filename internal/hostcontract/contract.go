// Package hostcontract defines the host-neutral runtime contract.
package hostcontract

import (
	"errors"
	"fmt"
)

// Kind identifies a ProductRuntime host without granting host capabilities.
type Kind string

const (
	KindDesktop Kind = "desktop"
	KindServer  Kind = "server"
)

// AuthenticationBoundary identifies a language-independent authentication
// boundary enforced by a host before a request reaches ProductRuntime.
type AuthenticationBoundary string

const (
	AuthenticationDesktopCapability AuthenticationBoundary = "desktop_capability"
	AuthenticationCaptureCapability AuthenticationBoundary = "capture_capability"
	AuthenticationWebSession        AuthenticationBoundary = "web_session"
	AuthenticationProxyClient       AuthenticationBoundary = "proxy_client"
)

var ErrInvalidContract = errors.New("invalid host contract")

// Contract is a closed value produced by a host-specific constructor.
//
// Its fields are intentionally private so callers cannot assemble a dangerous
// combination that mixes Desktop and Server authentication.
type Contract struct {
	kind                  Kind
	managementAuth        AuthenticationBoundary
	proxyAuth             AuthenticationBoundary
	supportsCaptureRuns   bool
	supportsWebSessions   bool
	supportsProxyClients  bool
	supportsCustomReports bool
}

// Desktop returns the Desktop host contract.
func Desktop() Contract {
	return Contract{
		kind:                  KindDesktop,
		managementAuth:        AuthenticationDesktopCapability,
		proxyAuth:             AuthenticationCaptureCapability,
		supportsCaptureRuns:   true,
		supportsCustomReports: true,
	}
}

// Server returns the Server host contract.
func Server() Contract {
	return Contract{
		kind:                 KindServer,
		managementAuth:       AuthenticationWebSession,
		proxyAuth:            AuthenticationProxyClient,
		supportsCaptureRuns:  true,
		supportsWebSessions:  true,
		supportsProxyClients: true,
	}
}

// Validate rejects zero values and mixed authentication boundaries.
func (c Contract) Validate() error {
	switch c {
	case Desktop(), Server():
		return nil
	default:
		return fmt.Errorf("%w: unsupported descriptor", ErrInvalidContract)
	}
}

func (c Contract) Kind() Kind {
	return c.kind
}

func (c Contract) ManagementAuthentication() AuthenticationBoundary {
	return c.managementAuth
}

func (c Contract) ProxyAuthentication() AuthenticationBoundary {
	return c.proxyAuth
}

func (c Contract) SupportsCaptureRuns() bool {
	return c.supportsCaptureRuns
}

func (c Contract) SupportsWebSessions() bool {
	return c.supportsWebSessions
}

func (c Contract) SupportsProxyClients() bool {
	return c.supportsProxyClients
}

func (c Contract) SupportsCustomReports() bool {
	return c.supportsCustomReports
}
