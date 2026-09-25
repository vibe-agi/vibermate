package runlauncher

import "errors"

type ACPLaunchRequest struct {
	Command       []string
	RecordContent bool
}

var ErrACPUnavailable = errors.New("Runtime does not support ACP observations; update the App or Server and terminal command together")
