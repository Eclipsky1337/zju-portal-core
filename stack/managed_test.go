package stack_test

import (
	"testing"

	stackpkg "github.com/Eclipsky1337/zju-portal-core/stack"
	"github.com/Eclipsky1337/zju-portal-core/stack/gvisor"
	"github.com/Eclipsky1337/zju-portal-core/stack/tcptunnel"
)

func TestDefaultStacksImplementManagedLifecycle(t *testing.T) {
	var _ stackpkg.Managed = (*gvisor.Stack)(nil)
	var _ stackpkg.Managed = (*tcptunnel.Stack)(nil)
}
