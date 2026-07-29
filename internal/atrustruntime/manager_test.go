package atrustruntime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	atrustclient "github.com/Eclipsky1337/zju-portal-core/client/atrust"
	"github.com/Eclipsky1337/zju-portal-core/core"
)

func TestManagerClosesActiveSessionBeforeStartingReplacement(t *testing.T) {
	deps := successfulSessionDependencies()
	var setupCount atomic.Int32
	var closeCount atomic.Int32
	var replacementStartedEarly atomic.Bool
	deps.closeClient = func(*atrustclient.Client) {
		closeCount.Add(1)
	}
	baseSetup := deps.setup
	deps.setup = func(ctx context.Context, client *atrustclient.Client, config Config, clientData, resourceData []byte, stageHandler func(atrustclient.SetupStage)) ([]byte, error) {
		if setupCount.Add(1) == 2 && closeCount.Load() != 1 {
			replacementStartedEarly.Store(true)
		}
		return baseSetup(ctx, client, config, clientData, resourceData, stageHandler)
	}
	manager := newManager(deps)

	first, err := manager.Start(context.Background(), "session-1", Config{})
	if err != nil {
		t.Fatalf("first Start() error = %v", err)
	}
	second, err := manager.Start(context.Background(), "session-2", Config{})
	if err != nil {
		t.Fatalf("second Start() error = %v", err)
	}

	if replacementStartedEarly.Load() {
		t.Fatal("replacement setup started before the active runtime closed")
	}
	if state := first.Status().State; state != core.SessionStateStopped {
		t.Fatalf("first session state = %q", state)
	}
	if active := manager.Active(); active != second {
		t.Fatalf("active session = %p, want %p", active, second)
	}
	if got := closeCount.Load(); got != 1 {
		t.Fatalf("close count before final stop = %d, want 1", got)
	}
	if _, err := manager.Stop(context.Background(), "session-2"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestManagerSerializesConcurrentStarts(t *testing.T) {
	deps := successfulSessionDependencies()
	var inSetup atomic.Int32
	var concurrentSetup atomic.Bool
	baseSetup := deps.setup
	deps.setup = func(ctx context.Context, client *atrustclient.Client, config Config, clientData, resourceData []byte, stageHandler func(atrustclient.SetupStage)) ([]byte, error) {
		if inSetup.Add(1) != 1 {
			concurrentSetup.Store(true)
		}
		defer inSetup.Add(-1)
		time.Sleep(10 * time.Millisecond)
		return baseSetup(ctx, client, config, clientData, resourceData, stageHandler)
	}
	manager := newManager(deps)

	var waitGroup sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []core.SessionID{"session-1", "session-2"} {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, err := manager.Start(context.Background(), id, Config{})
			errs <- err
		}()
	}
	waitGroup.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Start() error = %v", err)
		}
	}
	if concurrentSetup.Load() {
		t.Fatal("authentication setups ran concurrently")
	}
	if manager.Active() == nil {
		t.Fatal("active session = nil")
	}
	if _, err := manager.Stop(context.Background(), manager.Active().id); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestManagerDoesNotStartReplacementUntilRuntimeActuallyCloses(t *testing.T) {
	deps := successfulSessionDependencies()
	releaseClose := make(chan struct{})
	var setupCount atomic.Int32
	deps.closeClient = func(*atrustclient.Client) {
		<-releaseClose
	}
	baseSetup := deps.setup
	deps.setup = func(ctx context.Context, client *atrustclient.Client, config Config, clientData, resourceData []byte, stageHandler func(atrustclient.SetupStage)) ([]byte, error) {
		setupCount.Add(1)
		return baseSetup(ctx, client, config, clientData, resourceData, stageHandler)
	}
	manager := newManager(deps)
	if _, err := manager.Start(context.Background(), "session-1", Config{}); err != nil {
		t.Fatalf("first Start() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, err := manager.Start(ctx, "session-2", Config{})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("replacement Start() error = %v", err)
	}
	if got := setupCount.Load(); got != 1 {
		t.Fatalf("setup count while old runtime is closing = %d, want 1", got)
	}

	close(releaseClose)
	second, err := manager.Start(context.Background(), "session-2", Config{})
	if err != nil {
		t.Fatalf("replacement retry Start() error = %v", err)
	}
	if got := setupCount.Load(); got != 2 {
		t.Fatalf("setup count after old runtime closed = %d, want 2", got)
	}
	if _, err := manager.Stop(context.Background(), second.id); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestNewManagersShareProcessSessionCoordinator(t *testing.T) {
	first := NewManager()
	second := NewManager()
	if first.coordinator != second.coordinator {
		t.Fatal("NewManager() instances do not share the process session coordinator")
	}
}
