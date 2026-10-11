package vlesscore

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
)

type Config struct {
	Executable  string         `json:"-"`
	Server      netip.AddrPort `json:"-"`
	UUID        string         `json:"-"`
	ServerName  string         `json:"-"`
	Password    string         `json:"-"`
	ShortID     string         `json:"-"`
	Fingerprint string         `json:"-"`
	Flow        string         `json:"-"`
	Security    Security       `json:"-"`
	TLS         *TLSOptions    `json:"-"`
}

func (Config) String() string   { return "vlesscore.Config{redacted}" }
func (Config) GoString() string { return "vlesscore.Config{redacted}" }

func (c Config) normalized() (Config, error) {
	if c.Executable == "" || !validTarget(c.Server) || c.UUID == "" || c.ServerName == "" {
		return Config{}, errors.New("vlesscore: incomplete core configuration")
	}
	id := strings.ReplaceAll(c.UUID, "-", "")
	if b, err := hex.DecodeString(id); err != nil || len(b) != 16 || len(c.UUID) != 36 {
		return Config{}, errors.New("vlesscore: invalid client identity")
	}
	if c.Security == "" {
		c.Security = SecurityREALITY
	}
	switch c.Security {
	case SecurityREALITY:
		if c.TLS != nil {
			return Config{}, ErrInvalidTLS
		}
		if b, err := base64.RawURLEncoding.DecodeString(c.Password); err != nil || len(b) != 32 {
			return Config{}, errors.New("vlesscore: invalid REALITY authentication parameter")
		}
		if b, err := hex.DecodeString(c.ShortID); err != nil || len(b) > 8 {
			return Config{}, errors.New("vlesscore: invalid REALITY short ID")
		}
		if c.Fingerprint == "" {
			c.Fingerprint = "chrome"
		}
		if c.Flow == "" {
			c.Flow = "xtls-rprx-vision"
		}
	case SecurityTLS:
		if c.Password != "" || c.ShortID != "" {
			return Config{}, ErrInvalidTLS
		}
		if c.TLS == nil {
			c.TLS = &TLSOptions{Mode: TrustSystem}
		}
		if err := c.TLS.Validate(); err != nil {
			return Config{}, err
		}
	default:
		return Config{}, errors.New("vlesscore: unsupported transport security")
	}
	if c.Flow != "" && c.Flow != "xtls-rprx-vision" && c.Flow != "xtls-rprx-vision-udp443" {
		return Config{}, errors.New("vlesscore: unsupported VLESS flow")
	}
	return c, nil
}
func randomCredential() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("vlesscore: private credential generation failed")
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func randomUUID() ([16]byte, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return id, errors.New("vlesscore: private identity generation failed")
	}
	id[6] = (id[6] & 15) | 64
	id[8] = (id[8] & 63) | 128
	return id, nil
}

func (c Config) coreJSON(port int, username, password, udpPath string, udpID [16]byte) ([]byte, error) {
	var err error
	c, err = c.normalized()
	if err != nil {
		return nil, err
	}
	streamSettings := map[string]any{"network": "raw", "security": "reality", "realitySettings": map[string]any{"show": false, "fingerprint": c.Fingerprint, "serverName": c.ServerName, "password": c.Password, "shortId": c.ShortID, "spiderX": "/"}}
	if c.Security == SecurityTLS {
		tlsSettings := map[string]any{"serverName": c.ServerName, "allowInsecure": false, "disableSystemRoot": c.TLS.Mode == TrustCustomCA}
		if c.Fingerprint != "" {
			tlsSettings["fingerprint"] = c.Fingerprint
		}
		if c.TLS.Mode == TrustCustomCA {
			tlsSettings["certificates"] = []any{map[string]any{"usage": "verify", "certificate": strings.Split(strings.TrimSpace(c.TLS.CAPEM), "\n")}}
		}
		streamSettings = map[string]any{"network": "raw", "security": "tls", "tlsSettings": tlsSettings}
	}
	id := hex.EncodeToString(udpID[:])
	uuid := id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
	return json.Marshal(map[string]any{
		"log": map[string]any{"loglevel": "none"},
		"inbounds": []any{
			map[string]any{"tag": "private-auth-socks", "listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": map[string]any{"auth": "password", "accounts": []any{map[string]any{"user": username, "pass": password}}, "udp": false}},
			map[string]any{"tag": "private-auth-datagrams", "listen": udpPath, "protocol": "vless", "settings": map[string]any{"clients": []any{map[string]any{"id": uuid}}, "decryption": "none"}, "streamSettings": map[string]any{"network": "raw", "security": "none"}},
		},
		"outbounds": []any{map[string]any{"tag": "vless", "protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": c.Server.Addr().String(), "port": c.Server.Port(), "users": []any{map[string]any{"id": c.UUID, "encryption": "none", "flow": c.Flow}}}}}, "streamSettings": streamSettings}},
	})
}
