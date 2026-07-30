//go:build darwin

package systemdns

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNetworkServicesForInterface(t *testing.T) {
	output := `An asterisk (*) denotes that a network service is disabled.
(1) USB 10/100/1000 LAN
(Hardware Port: USB 10/100/1000 LAN, Device: en7)
(2) Wi-Fi
(Hardware Port: Wi-Fi, Device: en0)
(3) Thunderbolt Bridge
(Hardware Port: Thunderbolt Bridge, Device: bridge0)
`
	if got := networkServicesForInterface(output, "en0"); !reflect.DeepEqual(got, []string{"Wi-Fi"}) {
		t.Fatalf("services = %#v", got)
	}
}

func TestParseDNSServers(t *testing.T) {
	if got := parseDNSServers("1.1.1.1\n8.8.8.8\n"); !reflect.DeepEqual(got, []string{"1.1.1.1", "8.8.8.8"}) {
		t.Fatalf("servers = %#v", got)
	}
	if got := parseDNSServers("There aren't any DNS Servers set on Wi-Fi.\n"); got != nil {
		t.Fatalf("automatic servers = %#v", got)
	}
}

func TestDarwinControllerRestoresOriginalServers(t *testing.T) {
	runner := &runnerStub{responses: map[string]string{
		"-listnetworkserviceorder":                      "(1) Wi-Fi\n(Hardware Port: Wi-Fi, Device: en0)\n",
		"-getdnsservers\x00Wi-Fi":                       "1.1.1.1\n8.8.8.8\n",
		"-setdnsservers\x00Wi-Fi\x00172.19.0.2":         "",
		"-setdnsservers\x00Wi-Fi\x001.1.1.1\x008.8.8.8": "",
	}}
	controller := testDarwinController(t, runner)
	if err := controller.Apply(context.Background(), "172.19.0.2"); err != nil {
		t.Fatal(err)
	}
	if err := controller.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-listnetworkserviceorder",
		"-getdnsservers\x00Wi-Fi",
		"-setdnsservers\x00Wi-Fi\x00172.19.0.2",
		"-setdnsservers\x00Wi-Fi\x001.1.1.1\x008.8.8.8",
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v", runner.calls)
	}
}

func TestDarwinControllerRollsBackPartialApply(t *testing.T) {
	runner := &runnerStub{
		responses: map[string]string{
			"-listnetworkserviceorder":       "(1) Wi-Fi\n(Hardware Port: Wi-Fi, Device: en0)\n(2) Backup Wi-Fi\n(Hardware Port: Wi-Fi, Device: en0)\n",
			"-getdnsservers\x00Wi-Fi":        "There aren't any DNS Servers set on Wi-Fi.\n",
			"-getdnsservers\x00Backup Wi-Fi": "9.9.9.9\n",
		},
		failures: map[string]error{
			"-setdnsservers\x00Backup Wi-Fi\x00172.19.0.2": errors.New("apply failed"),
		},
	}
	controller := testDarwinController(t, runner)
	err := controller.Apply(context.Background(), "172.19.0.2")
	if err == nil || !strings.Contains(err.Error(), "apply failed") {
		t.Fatalf("Apply() error = %v", err)
	}
	if got := runner.calls[len(runner.calls)-1]; got != "-setdnsservers\x00Wi-Fi\x00Empty" {
		t.Fatalf("rollback call = %q", got)
	}
	if _, statErr := os.Stat(controller.statePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("snapshot remains after rollback: %v", statErr)
	}
}

func TestDarwinControllerRecoversStaleSnapshot(t *testing.T) {
	runner := &runnerStub{responses: map[string]string{
		"-listnetworkserviceorder":              "(1) Wi-Fi\n(Hardware Port: Wi-Fi, Device: en0)\n",
		"-getdnsservers\x00Wi-Fi":               "1.1.1.1\n",
		"-setdnsservers\x00Wi-Fi\x009.9.9.9":    "",
		"-setdnsservers\x00Wi-Fi\x00172.19.0.2": "",
	}}
	controller := testDarwinController(t, runner)
	if err := os.WriteFile(controller.statePath, []byte(`[{"service":"Wi-Fi","servers":["9.9.9.9"]}]`), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := controller.Apply(context.Background(), "172.19.0.2"); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) == 0 || runner.calls[0] != "-setdnsservers\x00Wi-Fi\x009.9.9.9" {
		t.Fatalf("stale recovery calls = %#v", runner.calls)
	}
	if err := controller.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func testDarwinController(t *testing.T, runner commandRunner) *darwinController {
	t.Helper()
	directory := t.TempDir()
	return &darwinController{
		interfaceName: "en0",
		runner:        runner,
		statePath:     filepath.Join(directory, "system-dns.json"),
		lockPath:      filepath.Join(directory, "system-dns.lock"),
	}
}

type runnerStub struct {
	responses map[string]string
	failures  map[string]error
	calls     []string
}

func (runner *runnerStub) Run(_ context.Context, args ...string) ([]byte, error) {
	key := strings.Join(args, "\x00")
	runner.calls = append(runner.calls, key)
	if err := runner.failures[key]; err != nil {
		return nil, err
	}
	return []byte(runner.responses[key]), nil
}
