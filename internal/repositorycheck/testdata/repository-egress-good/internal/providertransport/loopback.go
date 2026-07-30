package providertransport

import (
	"net"
	"net/http"
)

func ExplicitLoopbackTransport() (*net.Dialer, *http.Transport) {
	return &net.Dialer{}, &http.Transport{Proxy: nil}
}
