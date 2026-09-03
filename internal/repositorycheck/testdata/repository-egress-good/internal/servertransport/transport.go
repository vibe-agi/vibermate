package servertransport

import (
	"net"
	"net/http"
)

func New() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{}).DialContext,
		},
	}
}
