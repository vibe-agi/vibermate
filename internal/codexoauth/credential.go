// Package codexoauth owns imported Codex ChatGPT OAuth credentials, safe
// identity projections, and refresh-token rotation. Tokens remain secret bytes;
// JWT claims are decoded only as unverified display and consistency metadata.
package codexoauth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	credentialWireVersion = 1
	maxCredentialBytes    = 32 << 10
	maxTokenBytes         = 16 << 10
	maxIdentityBytes      = 512
)

var (
	ErrInvalidAuthJSON   = errors.New("Codex OAuth auth.json is invalid")
	ErrInvalidCredential = errors.New("Codex OAuth credential is invalid")
	ErrIdentityMismatch  = errors.New("Codex OAuth account identity does not match the token set")
)

type Profile struct {
	AccountID   string
	Email       string
	UserID      string
	PlanType    string
	FedRAMP     bool
	ExpiresAt   time.Time
	LastRefresh time.Time
}

type Credential struct {
	idToken      []byte
	accessToken  []byte
	refreshToken []byte
	accountID    string
	lastRefresh  time.Time
	state        State
}

type importedAuth struct {
	AuthMode     string  `json:"auth_mode"`
	OpenAIAPIKey *string `json:"OPENAI_API_KEY"`
	Tokens       struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
	LastRefresh string `json:"last_refresh"`
}

type credentialWire struct {
	Version      int    `json:"version"`
	State        State  `json:"state"`
	IDToken      string `json:"idToken"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	AccountID    string `json:"accountId"`
	LastRefresh  string `json:"lastRefresh"`
}

type jwtClaims struct {
	ExpiresAt int64  `json:"exp"`
	Email     string `json:"email"`
	Profile   struct {
		Email string `json:"email"`
	} `json:"https://api.openai.com/profile"`
	Auth struct {
		PlanType   string `json:"chatgpt_plan_type"`
		UserID     string `json:"chatgpt_user_id"`
		LegacyUser string `json:"user_id"`
		AccountID  string `json:"chatgpt_account_id"`
		FedRAMP    bool   `json:"chatgpt_account_is_fedramp"`
	} `json:"https://api.openai.com/auth"`
}

func ImportAuthJSON(encoded []byte) (*Credential, error) {
	if len(encoded) == 0 || len(encoded) > maxCredentialBytes || bytes.IndexByte(encoded, 0) >= 0 {
		return nil, ErrInvalidAuthJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	var wire importedAuth
	if err := decoder.Decode(&wire); err != nil {
		return nil, ErrInvalidAuthJSON
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) ||
		wire.AuthMode != "chatgpt" ||
		(wire.OpenAIAPIKey != nil && *wire.OpenAIAPIKey != "") {
		return nil, ErrInvalidAuthJSON
	}
	lastRefresh, err := time.Parse(time.RFC3339Nano, wire.LastRefresh)
	if err != nil || lastRefresh.IsZero() {
		return nil, ErrInvalidAuthJSON
	}
	credential := &Credential{
		idToken:      []byte(wire.Tokens.IDToken),
		accessToken:  []byte(wire.Tokens.AccessToken),
		refreshToken: []byte(wire.Tokens.RefreshToken),
		accountID:    wire.Tokens.AccountID,
		lastRefresh:  lastRefresh.UTC(),
		state:        StateReady,
	}
	if credential.accountID == "" {
		if claims, ok := parseJWTClaims(credential.idToken); ok {
			credential.accountID = claims.Auth.AccountID
		}
	}
	if err := credential.validate(); err != nil {
		credential.Destroy()
		if errors.Is(err, ErrIdentityMismatch) {
			return nil, err
		}
		return nil, ErrInvalidAuthJSON
	}
	return credential, nil
}

func ParseCredential(encoded []byte) (*Credential, error) {
	if len(encoded) == 0 || len(encoded) > maxCredentialBytes || bytes.IndexByte(encoded, 0) >= 0 {
		return nil, ErrInvalidCredential
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var wire credentialWire
	if err := decoder.Decode(&wire); err != nil {
		return nil, ErrInvalidCredential
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || wire.Version != credentialWireVersion {
		return nil, ErrInvalidCredential
	}
	lastRefresh, err := time.Parse(time.RFC3339Nano, wire.LastRefresh)
	if err != nil || lastRefresh.IsZero() {
		return nil, ErrInvalidCredential
	}
	credential := &Credential{
		idToken:      []byte(wire.IDToken),
		accessToken:  []byte(wire.AccessToken),
		refreshToken: []byte(wire.RefreshToken),
		accountID:    wire.AccountID,
		lastRefresh:  lastRefresh.UTC(),
		state:        wire.State,
	}
	if err := credential.validate(); err != nil {
		credential.Destroy()
		return nil, err
	}
	return credential, nil
}

func (credential *Credential) validate() error {
	if credential == nil || !validToken(credential.idToken) ||
		!validToken(credential.accessToken) || !validToken(credential.refreshToken) ||
		!validIdentity(credential.accountID) || credential.lastRefresh.IsZero() ||
		(credential.state != StateReady && credential.state != StateReconnectRequired) {
		return ErrInvalidCredential
	}
	for _, token := range [][]byte{credential.idToken, credential.accessToken} {
		if claims, ok := parseJWTClaims(token); ok && claims.Auth.AccountID != "" &&
			claims.Auth.AccountID != credential.accountID {
			return ErrIdentityMismatch
		}
	}
	return nil
}

func (credential *Credential) MarshalBinary() ([]byte, error) {
	if err := credential.validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(credentialWire{
		Version: credentialWireVersion, State: credential.state,
		IDToken: string(credential.idToken), AccessToken: string(credential.accessToken),
		RefreshToken: string(credential.refreshToken), AccountID: credential.accountID,
		LastRefresh: credential.lastRefresh.UTC().Format(time.RFC3339Nano),
	})
	if err != nil || len(encoded) > maxCredentialBytes {
		clear(encoded)
		return nil, ErrInvalidCredential
	}
	return encoded, nil
}

func (credential *Credential) Profile() Profile {
	if credential == nil {
		return Profile{}
	}
	profile := Profile{AccountID: credential.accountID, LastRefresh: credential.lastRefresh}
	accessClaims, accessOK := parseJWTClaims(credential.accessToken)
	idClaims, idOK := parseJWTClaims(credential.idToken)
	if idOK || accessOK {
		profile.Email = firstSafeClaim(
			idClaims.Email, idClaims.Profile.Email,
			accessClaims.Email, accessClaims.Profile.Email,
		)
		profile.UserID = firstSafeClaim(
			idClaims.Auth.UserID, idClaims.Auth.LegacyUser,
			accessClaims.Auth.UserID, accessClaims.Auth.LegacyUser,
		)
		profile.PlanType = firstSafeClaim(idClaims.Auth.PlanType, accessClaims.Auth.PlanType)
		profile.FedRAMP = idClaims.Auth.FedRAMP || accessClaims.Auth.FedRAMP
	}
	if accessOK && accessClaims.ExpiresAt > 0 {
		expiresAt := time.Unix(accessClaims.ExpiresAt, 0).UTC()
		if year := expiresAt.Year(); year >= 1970 && year <= 9999 {
			profile.ExpiresAt = expiresAt
		}
	}
	return profile
}

type Authorization struct {
	accessToken []byte
	accountID   string
	fedRAMP     bool
}

func (credential *Credential) Authorization() (*Authorization, error) {
	if err := credential.validate(); err != nil {
		return nil, err
	}
	if credential.state == StateReconnectRequired {
		return nil, ErrReconnectRequired
	}
	return &Authorization{
		accessToken: bytes.Clone(credential.accessToken),
		accountID:   credential.accountID,
		fedRAMP:     credential.Profile().FedRAMP,
	}, nil
}

func (authorization *Authorization) AccessTokenBytes() []byte {
	if authorization == nil {
		return nil
	}
	return bytes.Clone(authorization.accessToken)
}

func (authorization *Authorization) AccountID() string {
	if authorization == nil {
		return ""
	}
	return authorization.accountID
}

func (authorization *Authorization) FedRAMP() bool {
	return authorization != nil && authorization.fedRAMP
}

func (authorization *Authorization) Destroy() {
	if authorization == nil {
		return
	}
	clear(authorization.accessToken)
	authorization.accessToken = nil
	authorization.accountID = ""
	authorization.fedRAMP = false
}

func (credential *Credential) Destroy() {
	if credential == nil {
		return
	}
	clear(credential.idToken)
	clear(credential.accessToken)
	clear(credential.refreshToken)
	credential.idToken = nil
	credential.accessToken = nil
	credential.refreshToken = nil
	credential.accountID = ""
	credential.lastRefresh = time.Time{}
	credential.state = ""
}

func parseJWTClaims(token []byte) (jwtClaims, bool) {
	parts := bytes.Split(token, []byte("."))
	if len(parts) != 3 || len(parts[0]) == 0 || len(parts[1]) == 0 || len(parts[2]) == 0 {
		return jwtClaims{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(string(parts[1]))
	if err != nil || len(payload) == 0 || len(payload) > maxCredentialBytes {
		clear(payload)
		return jwtClaims{}, false
	}
	defer clear(payload)
	var claims jwtClaims
	if json.Unmarshal(payload, &claims) != nil {
		return jwtClaims{}, false
	}
	return claims, true
}

func validToken(value []byte) bool {
	return len(value) > 0 && len(value) <= maxTokenBytes && utf8.Valid(value) &&
		bytes.IndexAny(value, "\x00\r\n") < 0
}

func validIdentity(value string) bool {
	if value == "" || len(value) > maxIdentityBytes || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func firstSafeClaim(values ...string) string {
	for _, value := range values {
		if validIdentity(value) {
			return value
		}
	}
	return ""
}
