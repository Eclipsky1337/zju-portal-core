package systemdns

import "context"

type Controller interface {
	Apply(context.Context, string) error
	Restore(context.Context) error
}

func New(interfaceName string) Controller {
	return newPlatformController(interfaceName)
}
