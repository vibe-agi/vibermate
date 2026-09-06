package providerauth

import "context"

// HeaderLookup names one frozen credential-owned overwrite, not an arbitrary
// secret. The consumer must authorize disclosure and verify that the returned
// value matches its historical evidence before exposing it.
type HeaderLookup struct {
	AccountID                string
	AccountRevision          uint64
	CredentialEpoch          uint64
	UpstreamEndpointID       string
	UpstreamEndpointRevision uint64
	Name                     string
}

type HeaderReader interface {
	ReadOverwriteHeader(context.Context, HeaderLookup) (string, error)
}
