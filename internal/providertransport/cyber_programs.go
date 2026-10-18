package providertransport

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/vibe-agi/vibermate/internal/cyberaccess"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

const cyberCatalogTTL = 45 * time.Second
const maxCyberCatalogEntries = 128

type cyberCatalogKey struct {
	account           providerauth.AccountRef
	origin, targetRef string
	driver            providerauth.DriverRef
	secret            secretstore.Reference
}
type cachedCyberCatalog struct {
	programs cyberaccess.Catalog
	expires  time.Time
}

// AccountCyberPrograms reads the directory using the immutable model request's
// existing credential scope. It never reacquires an owner credential or consults
// the currently selected account. No client headers, prompt, or tools are sent.
// The caller retains the original account lease until this read has completed.
func (client *Client) AccountCyberPrograms(ctx context.Context, request Request) (cyberaccess.Catalog, error) {
	if ctx == nil || client == nil {
		return nil, errors.New("cyber program discovery is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	account, ok := request.AccountRef()
	if !ok || account.Validate() != nil || !account.DaybreakBlueEnabled ||
		!upstreamendpoint.IsChatGPTCodexOrigin(request.Target().Origin()) || request.targetRef == "" ||
		request.secretReference.String() == "" || request.authDriverRef.String() == "" {
		return nil, errors.New("cyber program discovery scope is invalid")
	}
	key := cyberCatalogKey{account: account, origin: request.target.Origin().String(), targetRef: request.targetRef, driver: request.authDriverRef, secret: request.secretReference}
	now := client.clock()
	client.mu.Lock()
	if client.closing {
		client.mu.Unlock()
		return nil, ErrClientClosing
	}
	if cached, ok := client.cyberCatalogs[key]; ok && now.Before(cached.expires) {
		result := cached.programs.Clone()
		client.mu.Unlock()
		return result, nil
	}
	client.mu.Unlock()
	readContext, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	response, err := client.fetchRuntimeJSON(readContext, runtimeFetchSpec{
		purpose: egressaudit.PurposeUpstreamModelDiscovery, targetRef: request.targetRef, target: request.target,
		relativePath: upstreamendpoint.ModelsPath(request.target.Origin()),
		credential:   requestModelCredential{account: account, secret: request.secretReference, driver: request.authDriverRef},
	})
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("cyber program directory request was rejected")
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil || mediaType != "application/json" {
			return nil, cyberaccess.ErrInvalidCatalog
		}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, cyberaccess.MaxCatalogBytes+1))
	if err != nil {
		return nil, err
	}
	programs, err := cyberaccess.ParseCatalog(body)
	if err != nil {
		return nil, err
	}
	if err := readContext.Err(); err != nil {
		return nil, err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing {
		return nil, ErrClientClosing
	}
	if client.cyberCatalogs == nil {
		client.cyberCatalogs = make(map[cyberCatalogKey]cachedCyberCatalog)
	}
	for existing, cached := range client.cyberCatalogs {
		if !now.Before(cached.expires) {
			delete(client.cyberCatalogs, existing)
		}
	}
	if len(client.cyberCatalogs) >= maxCyberCatalogEntries {
		var oldest cyberCatalogKey
		var expires time.Time
		for existing, cached := range client.cyberCatalogs {
			if expires.IsZero() || cached.expires.Before(expires) {
				oldest = existing
				expires = cached.expires
			}
		}
		delete(client.cyberCatalogs, oldest)
	}
	client.cyberCatalogs[key] = cachedCyberCatalog{programs: programs.Clone(), expires: client.clock().Add(cyberCatalogTTL)}
	return programs, nil
}

// This borrowed view cannot release or refresh the Exchange-owned lease.
type requestModelCredential struct {
	account providerauth.AccountRef
	secret  secretstore.Reference
	driver  providerauth.DriverRef
}

func (requestModelCredential) Mode() providerauth.CredentialMode {
	return providerauth.CredentialManaged
}
func (credential requestModelCredential) Driver() providerauth.DriverRef { return credential.driver }
func (credential requestModelCredential) Secret() secretstore.Reference  { return credential.secret }
func (credential requestModelCredential) Account() (providerauth.AccountRef, bool) {
	return credential.account, true
}
func (requestModelCredential) Release() {}
