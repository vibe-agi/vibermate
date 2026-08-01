package systemtrust

import "context"

type CommandExecutor interface {
	Execute(context.Context) error
}
