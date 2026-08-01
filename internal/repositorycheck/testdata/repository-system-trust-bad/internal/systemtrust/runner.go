package systemtrust

import "context"
import "os/exec"

type CommandSpec struct{}
type CommandResult struct{}
type Runner struct{}

func (Runner) Execute(context.Context, CommandSpec) (CommandResult, error) {
	return CommandResult{}, exec.Command("true").Run()
}
