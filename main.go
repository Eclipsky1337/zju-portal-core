package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	controlrest "github.com/Eclipsky1337/zju-portal-core/control/rest"
	controlv1 "github.com/Eclipsky1337/zju-portal-core/control/v1"
	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/daemonconfig"
	"github.com/Eclipsky1337/zju-portal-core/daemonruntime"
	zlog "github.com/Eclipsky1337/zju-portal-core/log"
	coremanager "github.com/Eclipsky1337/zju-portal-core/manager"
)

var CommitID string

const coreVersionNumber = "v0.1.0-alpha"
const daemonShutdownTimeout = 5 * time.Second

var controlCapabilities = []string{
	"atrust", "password", "sms", "cas", "oauth2",
	"socks5", "http", "dns", "tun",
	"config", "resource_snapshots", "resource_refresh", "service_status",
	"traffic_stats", "connections", "connection_close", "transport_connections",
	"resume_state", "routing_modes", "events",
	"stdio", "rest", "sse",
	"limitation_icmp", "limitation_socks5_udp_associate",
}

type daemonOptions struct {
	configPath string
	testConfig bool
	stdio      bool
	rest       string
	version    bool
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	if err := runDaemon(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, func() core.Manager { return coremanager.New() }); err != nil {
		fmt.Fprintln(os.Stderr, "ZJU Portal Core:", err)
		os.Exit(1)
	}
}

func runDaemon(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, newManager func() core.Manager) (returnErr error) {
	options, err := parseDaemonOptions(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if options.version {
		_, _ = fmt.Fprintln(stdout, coreVersion())
		return nil
	}

	config := daemonconfig.Default()
	config.Session.AutoStart = false
	if options.configPath != "" {
		config, err = daemonconfig.Load(options.configPath)
		if err != nil {
			return err
		}
	}
	if options.stdio {
		config.Control.Stdio.Enabled = true
	}
	if options.rest != "" {
		config.Control.REST.Enabled = true
		config.Control.REST.Listen = options.rest
	}
	if err := config.Validate(); err != nil {
		return err
	}
	if options.testConfig {
		if options.configPath == "" {
			return fmt.Errorf("--test-config requires --config")
		}
		_, _ = fmt.Fprintln(stdout, "configuration is valid")
		return nil
	}
	if !config.Session.AutoStart && !config.Control.Stdio.Enabled && !config.Control.REST.Enabled {
		return fmt.Errorf("no session or control transport is enabled")
	}

	logCloser, err := configureLog(config.Log, stderr)
	if err != nil {
		return err
	}
	if logCloser != nil {
		defer logCloser.Close()
	}

	manager := newManager()
	controller := daemonruntime.New(manager, options.configPath)
	if config.State.ResumeFile != "" {
		resumeState, loadErr := loadResumeState(config.State.ResumeFile)
		if loadErr != nil {
			return loadErr
		}
		controller.SetInitialResumeState(resumeState)
	}
	service := controlv1.NewService(controller, coreVersion(), controlCapabilities)
	var restServer *http.Server
	defer func() {
		returnErr = errors.Join(returnErr, shutdownControl(restServer, service))
	}()
	if config.State.ResumeFile != "" {
		go persistResumeStateEvents(ctx, service.Subscribe(ctx), manager, config.State.ResumeFile)
	}

	errCh := make(chan error, 4)
	if config.Control.REST.Enabled {
		restServer, err = startRESTServer(ctx, service, config.Control.REST, stderr, errCh)
		if err != nil {
			return err
		}
	}
	if config.Control.Stdio.Enabled {
		server := controlv1.NewServerWithService(service)
		go func() { errCh <- server.Serve(ctx, stdin, stdout) }()
	}
	go func() {
		if initErr := controller.Initialize(ctx, config); initErr != nil {
			zlog.Printf("initialize configuration failed: %v", initErr)
		}
	}()

	select {
	case <-ctx.Done():
	case runErr := <-errCh:
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			return runErr
		}
	}
	return nil
}

func shutdownControl(restServer *http.Server, service *controlv1.Service) error {
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), daemonShutdownTimeout)
	defer cancelShutdown()

	var restDone <-chan error
	if restServer != nil {
		done := make(chan error, 1)
		restDone = done
		go func() {
			done <- restServer.Shutdown(shutdownCtx)
		}()
	}
	serviceErr := service.Close(shutdownCtx)
	var restErr error
	if restDone != nil {
		restErr = <-restDone
	}
	if errors.Is(restErr, http.ErrServerClosed) {
		restErr = nil
	}
	return errors.Join(restErr, serviceErr)
}

func parseDaemonOptions(args []string, output io.Writer) (daemonOptions, error) {
	flags := flag.NewFlagSet("zju-portal-core", flag.ContinueOnError)
	flags.SetOutput(output)
	var options daemonOptions
	flags.StringVar(&options.configPath, "f", "", "YAML configuration file")
	flags.StringVar(&options.configPath, "config", "", "YAML configuration file")
	flags.BoolVar(&options.testConfig, "t", false, "validate configuration and exit")
	flags.BoolVar(&options.testConfig, "test-config", false, "validate configuration and exit")
	flags.BoolVar(&options.stdio, "stdio", false, "enable JSONL control over stdio")
	flags.StringVar(&options.rest, "rest", "", "enable REST/SSE control on address")
	flags.BoolVar(&options.version, "version", false, "print Core version")
	if err := flags.Parse(args); err != nil {
		return daemonOptions{}, err
	}
	if flags.NArg() != 0 {
		return daemonOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	return options, nil
}

func configureLog(config daemonconfig.LogConfig, fallback io.Writer) (io.Closer, error) {
	writer := fallback
	var closer io.Closer
	if config.Output != "" && config.Output != "stderr" {
		file, err := os.OpenFile(config.Output, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return nil, fmt.Errorf("open log output: %w", err)
		}
		writer, closer = file, file
	}
	zlog.SetOutput(writer)
	if config.Level == "debug" {
		zlog.EnableDebug()
	} else {
		zlog.DisableDebug()
	}
	zlog.Init()
	return closer, nil
}

func startRESTServer(ctx context.Context, service *controlv1.Service, config daemonconfig.RESTConfig, stderr io.Writer, errCh chan<- error) (*http.Server, error) {
	listener, err := net.Listen("tcp", config.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen REST control on %s: %w", config.Listen, err)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.IsLoopback() {
		_ = listener.Close()
		return nil, fmt.Errorf("REST control must listen on loopback, got %s", listener.Addr())
	}
	secret, err := restSecret(config)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	if secret == "" {
		data := make([]byte, 32)
		if _, err := rand.Read(data); err != nil {
			_ = listener.Close()
			return nil, err
		}
		secret = hex.EncodeToString(data)
		_, _ = fmt.Fprintln(stderr, "REST control token:", secret)
	}
	server := &http.Server{Handler: controlrest.NewServerContext(ctx, service, secret), ReadHeaderTimeout: 5 * time.Second}
	go func() { errCh <- server.Serve(listener) }()
	_, _ = fmt.Fprintln(stderr, "REST control listening on http://"+listener.Addr().String()+controlrest.APIBasePath)
	return server, nil
}

func restSecret(config daemonconfig.RESTConfig) (string, error) {
	if config.SecretFile == "" {
		return config.Secret, nil
	}
	data, err := os.ReadFile(config.SecretFile)
	if err != nil {
		return "", fmt.Errorf("read REST secret file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func loadResumeState(path string) (*core.ResumeState, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read resume state: %w", err)
	}
	var state core.ResumeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode resume state: %w", err)
	}
	return &state, nil
}

func saveResumeState(path string, state core.ResumeState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0644); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	removeTemporary = false
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

type resumeStateProvider interface {
	ResumeState(core.SessionID) (core.ResumeState, error)
}

func persistResumeStateEvents(ctx context.Context, events <-chan core.Event, provider resumeStateProvider, path string) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			if event.Type != core.EventTypeResumeStateUpdated {
				continue
			}
			state, err := provider.ResumeState(event.SessionID)
			if err != nil {
				zlog.Printf("read updated resume state: %v", err)
				continue
			}
			if err := saveResumeState(path, state); err != nil {
				zlog.Printf("save resume state: %v", err)
			}
		}
	}
}

func coreVersion() string {
	if CommitID == "" {
		return coreVersionNumber
	}
	return coreVersionNumber + "-" + CommitID
}
