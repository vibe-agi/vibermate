package provideraccount

import (
	"context"
	"strings"

	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"golang.org/x/net/http/httpguts"
)

// ReadOverwriteHeader uses the same revision-bound lease as an outbound
// request. Rotation/deletion cannot race the read, and no latest-value fallback
// or primary credential readback is exposed by this interface.
func (manager *Manager) ReadOverwriteHeader(ctx context.Context, lookup providerauth.HeaderLookup) (string, error) {
	if manager == nil || ctx == nil || !httpguts.ValidHeaderFieldName(lookup.Name) || len(lookup.Name) > 256 ||
		lookup.AccountRevision == 0 || lookup.CredentialEpoch == 0 || lookup.UpstreamEndpointRevision == 0 {
		return "", ErrInvalidAccount
	}
	id, err := NewID(lookup.AccountID)
	if err != nil {
		return "", ErrInvalidAccount
	}
	account, err := manager.account(id)
	if err != nil {
		return "", err
	}
	lease, err := manager.acquire(ctx, accountLeaseScope{
		id: id, accountRevision: lookup.AccountRevision, realmID: account.RealmID,
		upstreamEndpointID: lookup.UpstreamEndpointID, upstreamEndpointRevision: lookup.UpstreamEndpointRevision,
	})
	if err != nil {
		return "", err
	}
	defer lease.Release()
	ref, ok := lease.Account()
	if !ok || ref.CredentialEpoch != lookup.CredentialEpoch {
		return "", ErrCredentialMissing
	}
	value, err := manager.secrets.ReadAtRevision(ctx, lease.Secret(), secretstore.Revision(lookup.CredentialEpoch))
	value, err = secretstore.ValidateReaderResult(value, err)
	if err != nil {
		return "", ErrCredentialMissing
	}
	defer value.Destroy()
	encoded, err := value.CopyBytes()
	if err != nil {
		return "", ErrCredentialMissing
	}
	defer clear(encoded)
	material, err := providerauth.ParseMaterial(encoded)
	if err != nil {
		return "", ErrCredentialMissing
	}
	defer material.Destroy()
	for _, field := range material.HeaderPolicy().Set {
		if strings.EqualFold(field.Name, lookup.Name) {
			return field.Value, nil
		}
	}
	return "", ErrCredentialMissing
}
