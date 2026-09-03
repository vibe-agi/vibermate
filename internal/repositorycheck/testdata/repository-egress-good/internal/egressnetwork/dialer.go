package egressnetwork

import (
	"net"
	"net/http"
)

func NewBoundary() (*net.Dialer, *net.Resolver, *http.Client, *http.Transport) {
	return &net.Dialer{}, net.DefaultResolver, &http.Client{}, &http.Transport{}
}
