package productruntime

import (
	"github.com/vibe-agi/vibermate/internal/blindtunnel"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/loopbackproxy"
	"github.com/vibe-agi/vibermate/internal/originaltransport"
)

func selectOriginalTransport(options Options, builders ...*egressnetwork.Builder) (originaltransport.Transport, error) {
	if options.OriginalTransport != nil {
		return options.OriginalTransport, nil
	}
	if len(builders) > 0 {
		return originaltransport.NewProductionTransportWithBuilder(builders[0])
	}
	return originaltransport.NewProductionTransport()
}

func selectBlindTunnels(options Options, builders ...*egressnetwork.Builder) (loopbackproxy.BlindTunnelDialer, error) {
	if options.BlindTunnels != nil {
		return options.BlindTunnels, nil
	}
	if len(builders) > 0 {
		dialer, err := blindtunnel.NewDialerWithBuilder(options.OfflineHold, builders[0])
		if err != nil {
			return nil, err
		}
		return &blindTunnelDialer{coordinator: options.OfflineHold, dialer: dialer}, nil
	}
	return newBlindTunnelDialer(options.OfflineHold)
}
