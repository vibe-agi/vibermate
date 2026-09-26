package providertransport

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

const resetConsumePath = "/backend-api/wham/rate-limit-reset-credits/consume"

var resetRequestIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[45][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// A single banked credit has one logical redemption identity across browser
// sessions and Runtime restarts. SHA-1 here is the RFC 4122 UUIDv5 name hash,
// not a secret or an integrity mechanism.
func ResetRequestID(accountID, creditID string) (string, error) {
	if accountID == "" || !ValidResetCreditID(creditID) {
		return "", errors.New("reset credit identity is invalid")
	}
	namespace := [16]byte{0xfd, 0x37, 0x6a, 0x42, 0x0d, 0x80, 0x43, 0xd2, 0x9f, 0xb7, 0xe8, 0xa1, 0x69, 0xbd, 0x54, 0x71}
	digest := sha1.Sum(append(namespace[:], []byte(accountID+"\x00"+creditID)...))
	bytes := digest[:16]
	bytes[6] = bytes[6]&0x0f | 0x50
	bytes[8] = bytes[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:]), nil
}

// ResetRedemption carries one owner-authorized, explicit credit and stable
// attempt ID. The caller cannot supply an HTTP destination, method or header.
type ResetRedemption struct {
	origin     originidentity.ProviderOrigin
	creditID   string
	requestID  string
	credential providerauth.Lease
	targetRef  string
}

func NewOwnedResetRedemption(origin originidentity.ProviderOrigin, creditID, requestID string, credential providerauth.Lease) (ResetRedemption, error) {
	if !upstreamendpoint.IsChatGPTCodexOrigin(origin) ||
		!ValidResetRedemptionInput(creditID, requestID) ||
		credential == nil || credential.Mode() != providerauth.CredentialManaged ||
		credential.Driver() != providerauth.CodexOAuthDriverRef() {
		return ResetRedemption{}, errors.New("Codex reset redemption scope is invalid")
	}
	account, ok := credential.Account()
	if !ok || account.Validate() != nil {
		return ResetRedemption{}, errors.New("Codex reset account is invalid")
	}
	return ResetRedemption{origin: origin, creditID: creditID, requestID: requestID, credential: credential, targetRef: account.ID}, nil
}

func ValidResetRedemptionInput(creditID, requestID string) bool {
	return resetRequestIDPattern.MatchString(requestID) && ValidResetCreditID(creditID)
}

func ValidResetCreditID(creditID string) bool {
	return creditID != "" && len(creditID) <= 256 && utf8.ValidString(creditID) &&
		strings.IndexFunc(creditID, unicode.IsControl) < 0
}

// ConsumeResetCredit uses the same authenticated Runtime transport, Offline
// Hold and terminal audit as a quota read, but records a distinct write purpose.
func (client *Client) ConsumeResetCredit(ctx context.Context, command ResetRedemption) (*http.Response, error) {
	if _, err := NewOwnedResetRedemption(command.origin, command.creditID, command.requestID, command.credential); err != nil || command.targetRef == "" {
		return nil, errors.New("Codex reset redemption is invalid")
	}
	body, err := json.Marshal(struct {
		RedeemRequestID string `json:"redeem_request_id"`
		CreditID        string `json:"credit_id"`
	}{RedeemRequestID: command.requestID, CreditID: command.creditID})
	if err != nil {
		return nil, err
	}
	defer clear(body)
	target, err := NewTarget(command.origin)
	if err != nil {
		return nil, err
	}
	return client.fetchRuntimeJSON(ctx, runtimeFetchSpec{
		purpose:   egressaudit.PurposeUpstreamAccountAction,
		targetRef: command.targetRef, target: target,
		relativePath: resetConsumePath, credential: command.credential,
		method: http.MethodPost, body: body, contentType: "application/json",
	})
}
