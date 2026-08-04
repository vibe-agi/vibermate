package transportprofile

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Fingerprint is a value-only, publishable summary of one ClientHello. It
// intentionally omits the raw record, SNI value, session identifiers, key
// shares, random bytes, and extension payloads.
type Fingerprint struct {
	JA3           string `json:"ja3"`
	JA3Hash       string `json:"ja3_hash"`
	JA4           string `json:"ja4"`
	JA4R          string `json:"ja4_r"`
	Peetprint     string `json:"peetprint"`
	PeetprintHash string `json:"peetprint_hash"`
}

// Fingerprint returns stable, value-only fingerprint formats for the captured
// ClientHello. These formats are observations, not authentication evidence.
func (observation Observation) Fingerprint() (Fingerprint, error) {
	if !observation.valid {
		return Fingerprint{}, ErrClientHelloUnavailable
	}
	parsed, err := parseFingerprintHello(observation.fingerprintRecord)
	if err != nil {
		return Fingerprint{}, errors.Join(ErrClientHelloInvalid, err)
	}

	ja3 := parsed.ja3()
	ja4, ja4r := parsed.ja4()
	peetprint := parsed.peetprint()
	return Fingerprint{
		JA3:           ja3,
		JA3Hash:       md5Hex(ja3),
		JA4:           ja4,
		JA4R:          ja4r,
		Peetprint:     peetprint,
		PeetprintHash: md5Hex(peetprint),
	}, nil
}

type fingerprintExtension struct {
	id      uint16
	payload []byte
}

type fingerprintHello struct {
	legacyVersion uint16
	ciphers       []uint16
	extensions    []fingerprintExtension
	alpn          []string
}

func parseFingerprintHello(record []byte) (fingerprintHello, error) {
	if len(record) < tlsRecordHeaderBytes+tlsHandshakeHeaderSize ||
		record[0] != tlsRecordHandshake {
		return fingerprintHello{}, errors.New("fingerprint record is truncated")
	}
	recordLength := int(binary.BigEndian.Uint16(record[3:5]))
	if recordLength != len(record)-tlsRecordHeaderBytes {
		return fingerprintHello{}, errors.New("fingerprint record length is invalid")
	}
	handshake := record[tlsRecordHeaderBytes:]
	if handshake[0] != tlsHandshakeClientHello ||
		readUint24(handshake[1:4]) != len(handshake)-tlsHandshakeHeaderSize {
		return fingerprintHello{}, errors.New("fingerprint handshake is invalid")
	}
	body := handshake[tlsHandshakeHeaderSize:]
	if len(body) < 34 {
		return fingerprintHello{}, errors.New("fingerprint ClientHello is truncated")
	}
	parsed := fingerprintHello{legacyVersion: binary.BigEndian.Uint16(body[:2])}
	cursor := byteCursor{data: body}
	if !cursor.skip(2 + 32) {
		return fingerprintHello{}, errors.New("fingerprint fixed fields are truncated")
	}
	if _, ok := cursor.vector8(); !ok {
		return fingerprintHello{}, errors.New("fingerprint session ID is invalid")
	}
	cipherBytes, ok := cursor.vector16()
	if !ok || len(cipherBytes) == 0 || len(cipherBytes)%2 != 0 {
		return fingerprintHello{}, errors.New("fingerprint cipher suites are invalid")
	}
	for len(cipherBytes) != 0 {
		parsed.ciphers = append(parsed.ciphers, binary.BigEndian.Uint16(cipherBytes[:2]))
		cipherBytes = cipherBytes[2:]
	}
	if methods, methodsOK := cursor.vector8(); !methodsOK || len(methods) == 0 {
		return fingerprintHello{}, errors.New("fingerprint compression methods are invalid")
	}
	if cursor.remaining() == 0 {
		return parsed, nil
	}
	extensionBytes, ok := cursor.vector16()
	if !ok || cursor.remaining() != 0 {
		return fingerprintHello{}, errors.New("fingerprint extensions are invalid")
	}
	extensionCursor := byteCursor{data: extensionBytes}
	for extensionCursor.remaining() != 0 {
		id, idOK := extensionCursor.uint16()
		payload, payloadOK := extensionCursor.vector16()
		if !idOK || !payloadOK {
			return fingerprintHello{}, errors.New("fingerprint extension is truncated")
		}
		parsed.extensions = append(parsed.extensions, fingerprintExtension{
			id:      id,
			payload: payload,
		})
		if id == tlsExtensionALPN {
			parsed.alpn, ok = parseALPN(payload)
			if !ok {
				return fingerprintHello{}, errors.New("fingerprint ALPN is invalid")
			}
		}
	}
	return parsed, nil
}

func (hello fingerprintHello) ja3() string {
	ciphers := make([]string, 0, len(hello.ciphers))
	for _, cipher := range hello.ciphers {
		if !isGREASE(cipher) {
			ciphers = append(ciphers, strconv.FormatUint(uint64(cipher), 10))
		}
	}
	extensions := make([]string, 0, len(hello.extensions))
	groups := []string{}
	pointFormats := []string{}
	for _, extension := range hello.extensions {
		if !isGREASE(extension.id) {
			extensions = append(extensions, strconv.FormatUint(uint64(extension.id), 10))
		}
		switch extension.id {
		case 10:
			groups = decimalUint16Vector(extension.payload, true)
		case 11:
			pointFormats = decimalUint8Vector(extension.payload)
		}
	}
	return strings.Join([]string{
		strconv.FormatUint(uint64(hello.legacyVersion), 10),
		strings.Join(ciphers, "-"),
		strings.Join(extensions, "-"),
		strings.Join(groups, "-"),
		strings.Join(pointFormats, "-"),
	}, ",")
}

func (hello fingerprintHello) ja4() (string, string) {
	version := hello.legacyVersion
	if supported := hello.extension(43); supported != nil && len(supported) > 0 {
		length := int(supported[0])
		if length == len(supported)-1 && length%2 == 0 {
			for offset := 1; offset+1 < len(supported); offset += 2 {
				candidate := binary.BigEndian.Uint16(supported[offset : offset+2])
				if !isGREASE(candidate) && candidate > version {
					version = candidate
				}
			}
		}
	}
	versionCode := "00"
	switch version {
	case 0x0304:
		versionCode = "13"
	case 0x0303:
		versionCode = "12"
	case 0x0302:
		versionCode = "11"
	case 0x0301:
		versionCode = "10"
	}
	sniCode := "i"
	if hello.extension(0) != nil {
		sniCode = "d"
	}
	ciphers := make([]string, 0, len(hello.ciphers))
	for _, cipher := range hello.ciphers {
		if !isGREASE(cipher) {
			ciphers = append(ciphers, fmt.Sprintf("%04x", cipher))
		}
	}
	extensions := make([]string, 0, len(hello.extensions))
	var signatureAlgorithms []string
	for _, extension := range hello.extensions {
		if isGREASE(extension.id) || extension.id == 0 || extension.id == tlsExtensionALPN {
			continue
		}
		extensions = append(extensions, fmt.Sprintf("%04x", extension.id))
		if extension.id == 13 {
			signatureAlgorithms = hexUint16Vector(extension.payload, true)
		}
	}
	alpnFirst, alpnLast := "00", "00"
	if len(hello.alpn) != 0 {
		alpnFirst = shortALPN(hello.alpn[0])
		alpnLast = shortALPN(hello.alpn[len(hello.alpn)-1])
	}
	prefix := fmt.Sprintf(
		"t%s%s%02d%02d%s%s",
		versionCode,
		sniCode,
		len(ciphers),
		len(extensions),
		alpnFirst,
		alpnLast,
	)
	sortedCiphers := append([]string(nil), ciphers...)
	sortedExtensions := append([]string(nil), extensions...)
	sort.Strings(sortedCiphers)
	sort.Strings(sortedExtensions)
	extensionVector := strings.Join(sortedExtensions, ",")
	if len(signatureAlgorithms) != 0 {
		extensionVector += "_" + strings.Join(signatureAlgorithms, ",")
	}
	ja4 := strings.Join([]string{
		prefix,
		truncatedSHA256(strings.Join(sortedCiphers, ",")),
		truncatedSHA256(extensionVector),
	}, "_")
	ja4r := strings.Join([]string{
		prefix,
		strings.Join(sortedCiphers, ","),
		strings.Join(sortedExtensions, ","),
		strings.Join(signatureAlgorithms, ","),
	}, "_")
	return ja4, ja4r
}

func (hello fingerprintHello) peetprint() string {
	versions := decimalUint16Vector8(hello.extension(43), true, true)
	protocols := make([]string, 0, len(hello.alpn))
	seenProtocols := make(map[string]struct{})
	for _, protocol := range hello.alpn {
		value := strings.TrimPrefix(protocol, "http/")
		if protocol == "h2" || strings.HasPrefix(protocol, "spdy/") {
			value = "2"
		}
		if _, seen := seenProtocols[value]; !seen {
			seenProtocols[value] = struct{}{}
			protocols = append(protocols, value)
		}
	}
	groups := decimalUint16VectorWithGREASE(hello.extension(10))
	signatures := decimalUint16VectorWithGREASE(hello.extension(13))
	pskModes := decimalUint8Vector(hello.extension(45))
	if len(pskModes) == 0 {
		pskModes = []string{"0"}
	}
	compression := decimalUint16VectorWithGREASE(hello.extension(27))
	ciphers := make([]string, 0, len(hello.ciphers))
	for _, cipher := range hello.ciphers {
		if isGREASE(cipher) {
			ciphers = append(ciphers, "GREASE")
		} else {
			ciphers = append(ciphers, strconv.FormatUint(uint64(cipher), 10))
		}
	}
	extensions := make([]string, 0, len(hello.extensions))
	for _, extension := range hello.extensions {
		if isGREASE(extension.id) {
			extensions = append(extensions, "GREASE")
		} else {
			extensions = append(extensions, strconv.FormatUint(uint64(extension.id), 10))
		}
	}
	sort.Strings(extensions)
	return strings.Join([]string{
		strings.Join(versions, "-"),
		strings.Join(protocols, "-"),
		strings.Join(groups, "-"),
		strings.Join(signatures, "-"),
		strings.Join(pskModes, "-"),
		strings.Join(compression, "-"),
		strings.Join(ciphers, "-"),
		strings.Join(extensions, "-"),
	}, "|")
}

func (hello fingerprintHello) extension(id uint16) []byte {
	for _, extension := range hello.extensions {
		if extension.id == id {
			return extension.payload
		}
	}
	return nil
}

func decimalUint16Vector8(
	payload []byte,
	omitGREASE bool,
	labelGREASE bool,
) []string {
	if len(payload) < 1 {
		return nil
	}
	length := int(payload[0])
	if length != len(payload)-1 || length%2 != 0 {
		return nil
	}
	return decimalUint16List(payload[1:], omitGREASE, labelGREASE)
}

func decimalUint16Vector(payload []byte, omitGREASE bool) []string {
	if len(payload) < 2 {
		return nil
	}
	length := int(binary.BigEndian.Uint16(payload[:2]))
	if length != len(payload)-2 || length%2 != 0 {
		return nil
	}
	return decimalUint16List(payload[2:], omitGREASE, false)
}

func decimalUint16VectorWithGREASE(payload []byte) []string {
	if len(payload) < 2 {
		return nil
	}
	length := int(binary.BigEndian.Uint16(payload[:2]))
	if length != len(payload)-2 || length%2 != 0 {
		return nil
	}
	return decimalUint16List(payload[2:], false, true)
}

func decimalUint16List(payload []byte, omitGREASE, labelGREASE bool) []string {
	if len(payload)%2 != 0 {
		return nil
	}
	values := make([]string, 0, len(payload)/2)
	for len(payload) != 0 {
		value := binary.BigEndian.Uint16(payload[:2])
		payload = payload[2:]
		if isGREASE(value) {
			if omitGREASE {
				continue
			}
			if labelGREASE {
				values = append(values, "GREASE")
				continue
			}
		}
		values = append(values, strconv.FormatUint(uint64(value), 10))
	}
	return values
}

func hexUint16Vector(payload []byte, omitGREASE bool) []string {
	if len(payload) < 2 {
		return nil
	}
	length := int(binary.BigEndian.Uint16(payload[:2]))
	if length != len(payload)-2 || length%2 != 0 {
		return nil
	}
	values := make([]string, 0, length/2)
	for offset := 2; offset < len(payload); offset += 2 {
		value := binary.BigEndian.Uint16(payload[offset : offset+2])
		if omitGREASE && isGREASE(value) {
			continue
		}
		values = append(values, fmt.Sprintf("%04x", value))
	}
	return values
}

func decimalUint8Vector(payload []byte) []string {
	if len(payload) < 1 || int(payload[0]) != len(payload)-1 {
		return nil
	}
	return decimalUint8List(payload[1:], false)
}

func decimalUint8List(payload []byte, labelGREASE bool) []string {
	values := make([]string, 0, len(payload))
	for _, value := range payload {
		if labelGREASE && value == 0x0a {
			values = append(values, "GREASE")
		} else {
			values = append(values, strconv.FormatUint(uint64(value), 10))
		}
	}
	return values
}

func shortALPN(protocol string) string {
	switch protocol {
	case "h2":
		return "h2"
	case "http/1.1", "http/1.0":
		return "h1"
	default:
		if len(protocol) >= 2 {
			return protocol[:2]
		}
		return "00"
	}
}

func isGREASE(value uint16) bool {
	return value&0x0f0f == 0x0a0a && byte(value>>8) == byte(value)
}

func truncatedSHA256(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])[:12]
}

func md5Hex(value string) string {
	digest := md5.Sum([]byte(value)) // #nosec G401 -- protocol fingerprint, not security.
	return hex.EncodeToString(digest[:])
}
