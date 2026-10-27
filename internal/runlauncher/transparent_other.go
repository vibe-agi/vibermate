//go:build !darwin && !linux

package runlauncher

type pausedRun struct{}

func pauseRun(int, string) pausedRun { return pausedRun{} }

func (pausedRun) resume() {}
