package serverhost

import (
	"github.com/vibe-agi/vibermate/internal/capturecontrol"
	"github.com/vibe-agi/vibermate/internal/capturegrant"
	"github.com/vibe-agi/vibermate/internal/productruntime"
)

func Start(options any) (any, error) {
	runtime, err := productruntime.Start(options)
	if err != nil {
		return nil, err
	}
	return startAttached(runtime)
}

func startAttached(runtime any) (any, error) {
	_, _ = capturegrant.New()
	_, _ = capturecontrol.NewManualHandler()
	_, _ = capturecontrol.New()
	return runtime, nil
}
