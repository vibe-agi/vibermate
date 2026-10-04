package productruntime

import (
	"github.com/vibe-agi/vibermate/internal/loopbackproxy"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
)

func selectOriginalTransport(options Options) (originaltransport.Transport, error) {
	if options.OriginalTransport != nil {
		return options.OriginalTransport, nil
	}
	return originaltransport.NewProductionTransport()
}

func selectBlindTunnels(options Options) (loopbackproxy.BlindTunnelDialer, error) {
	if options.BlindTunnels != nil {
		return options.BlindTunnels, nil
	}
	return newBlindTunnelDialer(options.OfflineHold)
}
