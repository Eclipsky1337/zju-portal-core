//go:build darwin

package systemdns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	restoreTimeout   = 5 * time.Second
	defaultStatePath = "/var/run/zju-portal-core/system-dns.json"
	defaultLockPath  = "/var/run/zju-portal-core/system-dns.lock"
)

var (
	servicePattern = regexp.MustCompile(`^\([0-9]+\)\s+(.+)$`)
	devicePattern  = regexp.MustCompile(`Device:\s*([^,)]+)`)
)

type commandRunner interface {
	Run(context.Context, ...string) ([]byte, error)
}

type networksetupRunner struct{}

type darwinController struct {
	interfaceName string
	runner        commandRunner
	statePath     string
	lockPath      string

	mu        sync.Mutex
	snapshots []dnsSnapshot
	lockFile  *os.File
}

type dnsSnapshot struct {
	Service string   `json:"service"`
	Servers []string `json:"servers,omitempty"`
}

func newPlatformController(interfaceName string) Controller {
	return &darwinController{interfaceName: interfaceName, runner: networksetupRunner{}, statePath: defaultStatePath, lockPath: defaultLockPath}
}

func (networksetupRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	output, err := exec.CommandContext(ctx, "networksetup", args...).CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("networksetup %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func (controller *darwinController) Apply(ctx context.Context, dnsServer string) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if len(controller.snapshots) != 0 {
		return nil
	}
	if controller.interfaceName == "" {
		return fmt.Errorf("system DNS control requires an outbound interface")
	}
	if dnsServer == "" {
		return fmt.Errorf("system DNS server is empty")
	}
	if err := controller.acquireLock(); err != nil {
		return err
	}
	if err := controller.recoverStaleSnapshot(); err != nil {
		controller.releaseLock()
		return err
	}

	output, err := controller.runner.Run(ctx, "-listnetworkserviceorder")
	if err != nil {
		controller.releaseLock()
		return err
	}
	services := networkServicesForInterface(string(output), controller.interfaceName)
	if len(services) == 0 {
		controller.releaseLock()
		return fmt.Errorf("find macOS network service for interface %q", controller.interfaceName)
	}

	snapshots := make([]dnsSnapshot, 0, len(services))
	for _, service := range services {
		servers, snapshotErr := controller.readServers(ctx, service)
		if snapshotErr != nil {
			controller.releaseLock()
			return snapshotErr
		}
		snapshots = append(snapshots, dnsSnapshot{Service: service, Servers: servers})
	}
	if err := controller.writeSnapshot(snapshots); err != nil {
		controller.releaseLock()
		return err
	}
	for _, snapshot := range snapshots {
		service := snapshot.Service
		if _, applyErr := controller.runner.Run(ctx, "-setdnsservers", service, dnsServer); applyErr != nil {
			restoreErr := controller.restoreSnapshots(snapshots)
			if restoreErr == nil {
				_ = os.Remove(controller.statePath)
			}
			controller.releaseLock()
			return errors.Join(applyErr, restoreErr)
		}
	}
	controller.snapshots = snapshots
	return nil
}

func (controller *darwinController) Restore(ctx context.Context) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if len(controller.snapshots) == 0 {
		return nil
	}
	err := controller.restoreSnapshotsContext(ctx, controller.snapshots)
	if err == nil {
		controller.snapshots = nil
		err = os.Remove(controller.statePath)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		controller.releaseLock()
	}
	return err
}

func (controller *darwinController) readServers(ctx context.Context, service string) ([]string, error) {
	output, err := controller.runner.Run(ctx, "-getdnsservers", service)
	if err != nil {
		return nil, err
	}
	return parseDNSServers(string(output)), nil
}

func (controller *darwinController) restoreSnapshots(snapshots []dnsSnapshot) error {
	ctx, cancel := context.WithTimeout(context.Background(), restoreTimeout)
	defer cancel()
	return controller.restoreSnapshotsContext(ctx, snapshots)
}

func (controller *darwinController) restoreSnapshotsContext(ctx context.Context, snapshots []dnsSnapshot) error {
	var restoreErrors []error
	for index := len(snapshots) - 1; index >= 0; index-- {
		snapshot := snapshots[index]
		args := []string{"-setdnsservers", snapshot.Service}
		if len(snapshot.Servers) == 0 {
			args = append(args, "Empty")
		} else {
			args = append(args, snapshot.Servers...)
		}
		_, err := controller.runner.Run(ctx, args...)
		restoreErrors = append(restoreErrors, err)
	}
	return errors.Join(restoreErrors...)
}

func (controller *darwinController) acquireLock() error {
	if controller.statePath == "" {
		controller.statePath = defaultStatePath
	}
	if controller.lockPath == "" {
		controller.lockPath = defaultLockPath
	}
	if err := os.MkdirAll(filepath.Dir(controller.lockPath), 0o777); err != nil {
		return fmt.Errorf("create system DNS state directory: %w", err)
	}
	lockFile, err := os.OpenFile(controller.lockPath, os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		return fmt.Errorf("open system DNS lock: %w", err)
	}
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lockFile.Close()
		return fmt.Errorf("another system DNS controller is active: %w", err)
	}
	controller.lockFile = lockFile
	return nil
}

func (controller *darwinController) releaseLock() {
	if controller.lockFile == nil {
		return
	}
	_ = unix.Flock(int(controller.lockFile.Fd()), unix.LOCK_UN)
	_ = controller.lockFile.Close()
	controller.lockFile = nil
}

func (controller *darwinController) recoverStaleSnapshot() error {
	data, err := os.ReadFile(controller.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read stale system DNS snapshot: %w", err)
	}
	var snapshots []dnsSnapshot
	if err := json.Unmarshal(data, &snapshots); err != nil {
		return fmt.Errorf("decode stale system DNS snapshot: %w", err)
	}
	if err := controller.restoreSnapshots(snapshots); err != nil {
		return fmt.Errorf("restore stale system DNS snapshot: %w", err)
	}
	if err := os.Remove(controller.statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale system DNS snapshot: %w", err)
	}
	return nil
}

func (controller *darwinController) writeSnapshot(snapshots []dnsSnapshot) error {
	data, err := json.Marshal(snapshots)
	if err != nil {
		return fmt.Errorf("encode system DNS snapshot: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(controller.statePath), 0o777); err != nil {
		return fmt.Errorf("create system DNS state directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(controller.statePath), ".system-dns-*")
	if err != nil {
		return fmt.Errorf("create system DNS snapshot: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write system DNS snapshot: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync system DNS snapshot: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close system DNS snapshot: %w", err)
	}
	if err := os.Rename(temporaryName, controller.statePath); err != nil {
		return fmt.Errorf("install system DNS snapshot: %w", err)
	}
	return nil
}

func networkServicesForInterface(output, interfaceName string) []string {
	var services []string
	var service string
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if matches := servicePattern.FindStringSubmatch(line); len(matches) == 2 {
			service = strings.TrimSpace(matches[1])
			continue
		}
		if service == "" {
			continue
		}
		if matches := devicePattern.FindStringSubmatch(line); len(matches) == 2 {
			if strings.TrimSpace(matches[1]) == interfaceName {
				services = append(services, service)
			}
			service = ""
		}
	}
	return services
}

func parseDNSServers(output string) []string {
	if strings.Contains(output, "There aren't any DNS Servers set on") {
		return nil
	}
	var servers []string
	for _, rawLine := range strings.Split(output, "\n") {
		if server := strings.TrimSpace(rawLine); server != "" {
			servers = append(servers, server)
		}
	}
	return servers
}
