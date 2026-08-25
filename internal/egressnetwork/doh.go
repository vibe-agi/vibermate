package egressnetwork

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const maximumDNSMessageBytes = 65535

type dohResolver struct {
	endpoint string
	client   *http.Client
}

func newDoHResolver(
	endpoint string,
	dialer ContextDialer,
	tlsConfig *tls.Config,
) (*dohResolver, error) {
	if dialer == nil || tlsConfig == nil {
		return nil, errors.New("DoH transport dependencies are incomplete")
	}
	canonical, err := normalizeDoHURL(endpoint)
	if err != nil {
		return nil, fmt.Errorf("build DoH resolver: %w", err)
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		ForceAttemptHTTP2:      true,
		DisableCompression:     true,
		DisableKeepAlives:      true,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  10 * time.Second,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		TLSClientConfig:        tlsConfig.Clone(),
	}
	return &dohResolver{
		endpoint: canonical,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (resolver *dohResolver) LookupNetIP(
	ctx context.Context,
	network string,
	host string,
) ([]netip.Addr, error) {
	if resolver == nil || resolver.client == nil || ctx == nil {
		return nil, errors.New("DoH lookup is incomplete")
	}
	host, err := normalizeHost(host)
	if err != nil {
		return nil, fmt.Errorf("DoH hostname: %w", err)
	}
	if address, parseErr := netip.ParseAddr(host); parseErr == nil {
		return []netip.Addr{address.Unmap()}, nil
	}
	var types []dnsmessage.Type
	switch network {
	case "ip4":
		types = []dnsmessage.Type{dnsmessage.TypeA}
	case "ip6":
		types = []dnsmessage.Type{dnsmessage.TypeAAAA}
	case "ip":
		types = []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA}
	default:
		return nil, errors.New("DoH lookup network is unsupported")
	}
	var addresses []netip.Addr
	var failures []error
	for _, queryType := range types {
		resolved, queryErr := resolver.query(ctx, host, queryType)
		if queryErr != nil {
			failures = append(failures, queryErr)
			continue
		}
		addresses = append(addresses, resolved...)
	}
	if len(addresses) != 0 {
		return addresses, nil
	}
	if len(failures) != 0 {
		return nil, fmt.Errorf("DoH lookup failed: %w", errors.Join(failures...))
	}
	return nil, errors.New("DoH response contained no address")
}

func (resolver *dohResolver) query(
	ctx context.Context,
	host string,
	queryType dnsmessage.Type,
) ([]netip.Addr, error) {
	name, err := dnsmessage.NewName(host + ".")
	if err != nil {
		return nil, errors.New("construct DoH question name")
	}
	var idBytes [2]byte
	if _, err := io.ReadFull(rand.Reader, idBytes[:]); err != nil {
		return nil, errors.New("construct DoH question identity")
	}
	id := binary.BigEndian.Uint16(idBytes[:])
	question := dnsmessage.Question{
		Name: name, Type: queryType, Class: dnsmessage.ClassINET,
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID: id, RecursionDesired: true,
	})
	if err := builder.StartQuestions(); err != nil {
		return nil, fmt.Errorf("start DoH questions: %w", err)
	}
	if err := builder.Question(question); err != nil {
		return nil, fmt.Errorf("append DoH question: %w", err)
	}
	payload, err := builder.Finish()
	if err != nil {
		return nil, fmt.Errorf("encode DoH question: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		resolver.endpoint,
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, fmt.Errorf("construct DoH request: %w", err)
	}
	request.Header.Set("Accept", "application/dns-message")
	request.Header.Set("Content-Type", "application/dns-message")
	request.ContentLength = int64(len(payload))
	response, err := resolver.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("send DoH request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DoH response status is %d", response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/dns-message" {
		return nil, errors.New("DoH response Content-Type is invalid")
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, maximumDNSMessageBytes+1))
	if err != nil || len(encoded) > maximumDNSMessageBytes {
		return nil, errors.New("DoH response exceeded its bound")
	}
	return parseDoHResponse(encoded, id, question)
}

func parseDoHResponse(
	payload []byte,
	id uint16,
	question dnsmessage.Question,
) ([]netip.Addr, error) {
	var parser dnsmessage.Parser
	header, err := parser.Start(payload)
	if err != nil || !header.Response || header.ID != id ||
		header.RCode != dnsmessage.RCodeSuccess {
		return nil, errors.New("DoH response header is invalid")
	}
	actualQuestion, err := parser.Question()
	if err != nil || actualQuestion != question {
		return nil, errors.New("DoH response question is invalid")
	}
	if _, err := parser.Question(); !errors.Is(err, dnsmessage.ErrSectionDone) {
		return nil, errors.New("DoH response has unexpected questions")
	}
	var addresses []netip.Addr
	for {
		answer, err := parser.AnswerHeader()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}
		if err != nil {
			return nil, errors.New("DoH response answer is invalid")
		}
		switch answer.Type {
		case dnsmessage.TypeA:
			resource, resourceErr := parser.AResource()
			if resourceErr != nil {
				return nil, errors.New("DoH A answer is invalid")
			}
			addresses = append(addresses, netip.AddrFrom4(resource.A))
		case dnsmessage.TypeAAAA:
			resource, resourceErr := parser.AAAAResource()
			if resourceErr != nil {
				return nil, errors.New("DoH AAAA answer is invalid")
			}
			addresses = append(addresses, netip.AddrFrom16(resource.AAAA).Unmap())
		default:
			if err := parser.SkipAnswer(); err != nil {
				return nil, errors.New("DoH response answer cannot be skipped")
			}
		}
	}
	if len(addresses) == 0 {
		return nil, errors.New("DoH response contained no address")
	}
	return addresses, nil
}

var _ Resolver = (*net.Resolver)(nil)
