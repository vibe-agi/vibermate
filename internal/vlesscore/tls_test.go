package vlesscore

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The old REALITY-only normalization must fail this test by behavior, not compilation.
func TestTLSConfigAcceptsOrdinaryFlow(t *testing.T) {
	c := tlsConfig()
	normalized, err := c.normalized()
	if err != nil {
		t.Fatalf("ordinary TLS config rejected: %v", err)
	}
	data, err := normalized.coreJSON(12345, "private-user", "private-password", "/fixture/u", [16]byte{})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	stream := config["outbounds"].([]any)[0].(map[string]any)["streamSettings"].(map[string]any)
	if stream["security"] != "tls" {
		t.Fatalf("ordinary TLS emitted security %v", stream["security"])
	}
}

func tlsConfig() Config {
	return Config{Executable: "fixture-core", Server: netip.MustParseAddrPort("127.0.0.1:443"), UUID: "00112233-4455-6677-8899-aabbccddeeff", ServerName: "tls.fixture.invalid", Security: SecurityTLS}
}

func TestTLSConfigPreservesExplicitFingerprint(t *testing.T) {
	c := tlsConfig()
	c.Fingerprint = "firefox"
	data, err := c.coreJSON(12345, "user", "pass", "/fixture/u", [16]byte{})
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	stream := result["outbounds"].([]any)[0].(map[string]any)["streamSettings"].(map[string]any)
	if stream["tlsSettings"].(map[string]any)["fingerprint"] != "firefox" {
		t.Fatal("explicit TLS fingerprint was discarded")
	}
}

func syntheticCertificate(t *testing.T, name string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name}, DNSNames: []string{name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

func TestTLSOptionsRejectsPartialBundles(t *testing.T) {
	cert, key := syntheticCertificate(t, "tls.fixture.invalid")
	malformed := "-----BEGIN CERTIFICATE-----\n%%%\n-----END CERTIFICATE-----\n"
	for name, material := range map[string]string{
		"empty": "", "whitespace": " \n\t", "key": key, "other-block": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte{1}})),
		"invalid-DER": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1, 2, 3}})), "malformed": malformed,
		"junk-prefix": "sentinel-junk" + cert, "junk-suffix": cert + "sentinel-junk", "junk-middle": cert + "junk\n" + cert,
		"key-suffix": cert + key, "malformed-prefix": malformed + cert, "malformed-suffix": cert + malformed,
		"nested-begin": "-----BEGIN CERTIFICATE-----\njunk\n" + cert,
		"headers":      strings.Replace(cert, "-----BEGIN CERTIFICATE-----\n", "-----BEGIN CERTIFICATE-----\nSentinel: secret\n\n", 1),
		"oversize":     cert + strings.Repeat(" ", (64<<10)+1-len(cert)),
	} {
		t.Run(name, func(t *testing.T) {
			o := TLSOptions{Mode: TrustCustomCA, CAPEM: material}
			if err := o.Validate(); !errors.Is(err, ErrInvalidTLS) {
				t.Fatalf("invalid bundle accepted (error=%v)", err)
			}
			c := tlsConfig()
			c.TLS = &o
			if _, err := c.coreJSON(12345, "user", "pass", "/fixture/u", [16]byte{}); !errors.Is(err, ErrInvalidTLS) {
				t.Fatalf("invalid trust reached JSON (error=%v)", err)
			}
		})
	}
	for name, material := range map[string]string{"single": cert, "bundle": " \n\t" + cert + "\n\t" + cert + " \r\n", "bound": cert + strings.Repeat(" ", (64<<10)-len(cert))} {
		t.Run(name, func(t *testing.T) {
			if err := (TLSOptions{Mode: TrustCustomCA, CAPEM: material}).Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTLSConfigTrustAndSecurityIsolation(t *testing.T) {
	cert, _ := syntheticCertificate(t, "tls.fixture.invalid")
	for _, options := range []*TLSOptions{nil, {Mode: TrustSystem}, {Mode: TrustCustomCA, CAPEM: cert}} {
		c := tlsConfig()
		c.TLS = options
		data, err := c.coreJSON(12345, "user", "pass", "/fixture/u", [16]byte{})
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		outbound := result["outbounds"].([]any)[0].(map[string]any)
		stream := outbound["streamSettings"].(map[string]any)
		settings := stream["tlsSettings"].(map[string]any)
		custom := options != nil && options.Mode == TrustCustomCA
		if stream["network"] != "raw" || stream["security"] != "tls" || stream["realitySettings"] != nil || settings["serverName"] != "tls.fixture.invalid" || settings["allowInsecure"] != false || settings["disableSystemRoot"] != custom {
			t.Fatal("unsafe or incorrect TLS mapping")
		}
		if custom {
			certificates := settings["certificates"].([]any)
			entry := certificates[0].(map[string]any)
			lines := entry["certificate"].([]any)
			var material []string
			for _, line := range lines {
				material = append(material, line.(string))
			}
			if len(certificates) != 1 || len(entry) != 2 || entry["usage"] != "verify" || strings.Join(material, "\n") != strings.TrimSpace(cert) || len(settings) != 4 {
				t.Fatal("custom verification certificate mapping changed")
			}
		} else if len(settings) != 3 {
			t.Fatal("system mode emitted extra trust settings")
		}
		user := outbound["settings"].(map[string]any)["vnext"].([]any)[0].(map[string]any)["users"].([]any)[0].(map[string]any)
		if user["flow"] != "" || user["encryption"] != "none" {
			t.Fatal("ordinary TLS flow changed")
		}
	}
	for _, options := range []TLSOptions{{}, {Mode: "unknown"}, {Mode: TrustSystem, CAPEM: cert}} {
		if !errors.Is(options.Validate(), ErrInvalidTLS) {
			t.Fatal("invalid trust mode accepted")
		}
	}
	for name, mutate := range map[string]func(*Config){"key": func(c *Config) { c.Password = base64.RawURLEncoding.EncodeToString(make([]byte, 32)) }, "short-id": func(c *Config) { c.ShortID = "01" }, "unknown-security": func(c *Config) { c.Security = "unknown" }, "flow": func(c *Config) { c.Flow = "unknown" }} {
		t.Run(name, func(t *testing.T) {
			c := tlsConfig()
			mutate(&c)
			if _, err := c.normalized(); err == nil {
				t.Fatal("incompatible TLS configuration accepted")
			}
		})
	}
	for _, flow := range []string{"", "xtls-rprx-vision", "xtls-rprx-vision-udp443"} {
		c := tlsConfig()
		c.Flow = flow
		n, err := c.normalized()
		if err != nil || n.Flow != flow {
			t.Fatal("TLS flow rejected or changed")
		}
	}
	reality := tlsConfig()
	reality.Security = ""
	reality.Password = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	reality.ShortID = "0102"
	n, err := reality.normalized()
	if err != nil || n.Security != SecurityREALITY || n.Flow != "xtls-rprx-vision" || n.Fingerprint != "chrome" {
		t.Fatal("legacy REALITY defaults changed")
	}
	data, err := reality.coreJSON(12345, "user", "pass", "/fixture/u", [16]byte{})
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	json.Unmarshal(data, &result)
	stream := result["outbounds"].([]any)[0].(map[string]any)["streamSettings"].(map[string]any)
	if stream["security"] != "reality" || stream["tlsSettings"] != nil || !reflect.DeepEqual(stream["realitySettings"], map[string]any{"show": false, "fingerprint": "chrome", "serverName": reality.ServerName, "password": reality.Password, "shortId": "0102", "spiderX": "/"}) {
		t.Fatal("legacy REALITY settings changed")
	}
	reality.TLS = &TLSOptions{Mode: TrustSystem}
	if _, err := reality.normalized(); !errors.Is(err, ErrInvalidTLS) {
		t.Fatal("REALITY accepted TLS options")
	}
}

func TestTLSFormattingRedactsMaterial(t *testing.T) {
	o := TLSOptions{Mode: TrustCustomCA, CAPEM: "PEM-secret-sentinel"}
	c := tlsConfig()
	c.UUID = "UUID-secret-sentinel"
	c.Password = "key-secret-sentinel"
	c.TLS = &o
	for _, value := range []any{o, &o, c, &c} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			output := fmt.Sprintf(format, value)
			if strings.Contains(output, "sentinel") {
				t.Fatal("format leaked private material")
			}
		}
		data, err := json.Marshal(value)
		if err != nil || string(data) != "{}" {
			t.Fatal("JSON exposed private configuration")
		}
	}
}
