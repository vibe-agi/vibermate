// Package loopbackclient owns pinned HTTP transport for local capability
// control. It cannot resolve ambient proxies or dial outside one literal IPv4
// loopback origin.
package loopbackclient

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Client struct {
	origin    string
	client    *http.Client
	transport *http.Transport
}

func New(origin string, timeout time.Duration) (*Client, error) {
	parsed, err := url.Parse(origin)
	if err != nil ||
		parsed.Scheme != "http" ||
		parsed.Hostname() != "127.0.0.1" ||
		parsed.Port() == "" ||
		parsed.User != nil ||
		parsed.Path != "" ||
		parsed.RawPath != "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" ||
		origin != "http://"+parsed.Host ||
		timeout <= 0 {
		return nil, errors.New("loopback control origin or timeout is invalid")
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || host != "127.0.0.1" {
		return nil, errors.New("loopback control origin or timeout is invalid")
	}
	numericPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil || numericPort == 0 {
		return nil, errors.New("loopback control origin or timeout is invalid")
	}
	expectedAddress := parsed.Host
	dialer := &net.Dialer{
		Timeout:   min(timeout, 5*time.Second),
		KeepAlive: 15 * time.Second,
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DisableCompression:    true,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   4,
		MaxConnsPerHost:       4,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: timeout,
		DialContext: func(
			ctx context.Context,
			network string,
			address string,
		) (net.Conn, error) {
			if network != "tcp" && network != "tcp4" {
				return nil, errors.New("loopback control requires TCP")
			}
			if address != expectedAddress {
				return nil, errors.New("loopback control dial escaped the pinned origin")
			}
			return dialer.DialContext(ctx, "tcp4", expectedAddress)
		},
	}
	return &Client{
		origin: origin,
		client: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(
				*http.Request,
				[]*http.Request,
			) error {
				return errors.New("loopback control redirects are forbidden")
			},
		},
		transport: transport,
	}, nil
}

func (client *Client) Do(request *http.Request) (*http.Response, error) {
	if client == nil ||
		request == nil ||
		request.URL == nil ||
		request.URL.Scheme != "http" ||
		request.URL.User != nil ||
		request.URL.Fragment != "" ||
		request.URL.Scheme+"://"+request.URL.Host != client.origin ||
		(request.Host != "" && request.Host != request.URL.Host) {
		return nil, errors.New("loopback control request escaped the pinned origin")
	}
	return client.client.Do(request)
}

func (client *Client) Close() {
	if client != nil && client.transport != nil {
		client.transport.CloseIdleConnections()
	}
}
