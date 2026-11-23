//go:build !darwin && !linux && !windows

package service

import "context"

type unsupported struct{}

func newManager(Names, string, Runner) Manager { return unsupported{} }

func (unsupported) Installed(context.Context) (Definition, bool, error) {
	return Definition{}, false, ErrUnsupported
}
func (unsupported) Install(context.Context, Definition) (bool, error) { return false, ErrUnsupported }
func (unsupported) Uninstall(context.Context) error                   { return ErrUnsupported }
func (unsupported) Start(context.Context) error                       { return ErrUnsupported }
func (unsupported) Active(context.Context) (bool, error)              { return false, ErrUnsupported }
func (unsupported) ManagerEnv(context.Context) map[string]string      { return nil }
func (unsupported) Graphical(context.Context) bool                    { return false }
