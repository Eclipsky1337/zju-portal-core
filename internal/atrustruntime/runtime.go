package atrustruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	clientpkg "github.com/Eclipsky1337/zju-portal-core/client"
	atrustclient "github.com/Eclipsky1337/zju-portal-core/client/atrust"
	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/internal/networkruntime"
	"github.com/Eclipsky1337/zju-portal-core/log"
)

type Config struct {
	ResumeState             *core.ResumeState
	ServerAddress           string
	ServerPort              int
	Username                string
	Password                string
	Phone                   string
	LoginDomain             string
	AuthType                string
	GraphCodeFile           string
	CASTicket               string
	OAuth2Code              string
	SID                     string
	DeviceID                string
	SignKey                 string
	ClientDataFile          string
	ResourceFile            string
	UpdateBestNodesInterval int
	BindInterface           string
	AutoDetectInterface     bool
	SkipTCPTunnelWait       bool
	DisableAutoReconnect    bool
	SetupNetwork            bool
	TCPTunnelMode           bool
	DisableRemoteDNS        bool
	RemoteDNSServer         string
	SecondaryDNSServer      string
	DNSTTL                  uint64
	DNSBind                 string
	Hosts                   map[string]string
	SOCKSBind               string
	SOCKSUsername           string
	SOCKSPassword           string
	HTTPBind                string
	TUNEnabled              bool
	TUNName                 string
	TUNAddress              string
	TUNMTU                  uint32
	TUNAutoRoute            bool
	TUNRouteAll             bool
	TUNStrictRoute          bool
	TUNStack                string
	TUNOutboundInterface    string
	TUNUDPTimeoutSeconds    int
	TUNUDPMaxFlows          int
	TUNDNSHijack            bool
	TUNFakeIP               bool
	TUNFakeIPRange          string
	RoutingMode             core.RoutingMode
	InternetOutbound        core.InternetOutboundConfig
	NetworkRuntime          replaceableNetworkRuntime
	AuthHandler             core.AuthHandler
	NodeSelectionHandler    func(map[string]string)
}

type Runtime struct {
	stateMu      sync.RWMutex
	client       *atrustclient.Client
	outbound     *networkSession
	resumeState  core.ResumeState
	closeClient  func()
	closeMu      sync.Mutex
	ownsOutbound bool
	closeOnce    sync.Once
	closeErr     error
}

func (r *Runtime) Client() clientpkg.Client {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.client
}

func (r *Runtime) Outbound() core.Outbound {
	if r.outbound == nil {
		return nil
	}
	return r.outbound.outbound
}

func (r *Runtime) DetachOutbound() *networkSession {
	r.closeMu.Lock()
	r.ownsOutbound = false
	r.closeMu.Unlock()
	return r.outbound
}

func (r *Runtime) Done() <-chan struct{} {
	if r.outbound == nil {
		return nil
	}
	return r.outbound.Done()
}

func (r *Runtime) Err() error {
	if r.outbound == nil {
		return nil
	}
	return r.outbound.Err()
}

func (r *Runtime) ResumeState() (core.ResumeState, error) {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	if r.resumeState.Data == "" {
		return core.ResumeState{}, core.WrapError(core.ErrorCodeResumeStateUnavailable, "resume state is unavailable", true, nil)
	}
	return r.resumeState, nil
}

func (r *Runtime) adoptClient(candidate *Runtime) func() {
	candidate.stateMu.Lock()
	client := candidate.client
	resumeState := candidate.resumeState
	closeClient := candidate.closeClient
	candidate.client = nil
	candidate.closeClient = nil
	candidate.stateMu.Unlock()

	r.stateMu.Lock()
	oldClient := r.client
	oldCloseClient := r.closeClient
	r.client = client
	r.resumeState = resumeState
	r.closeClient = closeClient
	r.stateMu.Unlock()
	return func() {
		if oldCloseClient != nil {
			oldCloseClient()
		} else if oldClient != nil {
			oldClient.Close()
		}
	}
}

func (r *Runtime) Services() []core.ServiceStatus {
	if r.outbound == nil {
		return []core.ServiceStatus{}
	}
	services, _ := r.outbound.Services()
	return services
}

func (r *Runtime) TrafficStats() (core.TrafficStats, error) {
	if r.outbound == nil {
		return core.TrafficStats{}, core.WrapError(core.ErrorCodeOutboundUnavailable, "traffic statistics are unavailable", true, nil)
	}
	return r.outbound.TrafficStats()
}

func (r *Runtime) Connections() ([]core.ConnectionInfo, error) {
	if r.outbound == nil {
		return nil, core.WrapError(core.ErrorCodeOutboundUnavailable, "connection statistics are unavailable", true, nil)
	}
	return r.outbound.Connections()
}

func (r *Runtime) CloseConnection(id string) error {
	if r.outbound == nil {
		return core.WrapError(core.ErrorCodeOutboundUnavailable, "connection control is unavailable", true, nil)
	}
	return r.outbound.CloseConnection(id)
}

func (r *Runtime) TransportConnections() ([]core.TransportConnectionInfo, error) {
	if r.outbound == nil {
		return nil, core.WrapError(core.ErrorCodeOutboundUnavailable, "transport connection statistics are unavailable", true, nil)
	}
	return r.outbound.TransportConnections()
}

func (r *Runtime) Close() {
	_ = r.CloseContext(context.Background())
}

func (r *Runtime) CloseContext(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		r.closeOnce.Do(func() {
			r.closeMu.Lock()
			ownsOutbound := r.ownsOutbound
			outbound := r.outbound
			r.closeMu.Unlock()
			if ownsOutbound && outbound != nil {
				r.closeErr = outbound.Close(context.Background())
			}
			r.stateMu.Lock()
			client := r.client
			closeClient := r.closeClient
			r.client = nil
			r.closeClient = nil
			r.stateMu.Unlock()
			if closeClient != nil {
				closeClient()
			} else if client != nil {
				client.Close()
			}
		})
		close(done)
	}()
	select {
	case <-done:
		return r.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

type dependencies struct {
	readFile       func(string) ([]byte, error)
	writeFile      func(string, []byte) error
	newClient      func(context.Context, string, string, string, string) *atrustclient.Client
	closeClient    func(*atrustclient.Client)
	setAuthHandler func(*atrustclient.Client, core.AuthHandler)
	setup          func(context.Context, *atrustclient.Client, Config, []byte, []byte, func(atrustclient.SetupStage)) ([]byte, error)
	setupNetwork   func(context.Context, clientpkg.Client, Config) (core.Outbound, error)
	readResources  func(clientpkg.Client) (core.Resources, error)
	wait           func(context.Context, time.Duration) error
}

func defaultDependencies() dependencies {
	return dependencies{
		readFile:  os.ReadFile,
		writeFile: writeFileAtomically,
		newClient: atrustclient.NewClientContext,
		closeClient: func(client *atrustclient.Client) {
			client.Close()
		},
		setAuthHandler: func(client *atrustclient.Client, handler core.AuthHandler) {
			client.SetAuthHandler(handler)
		},
		setup: func(ctx context.Context, client *atrustclient.Client, config Config, clientData, resourceData []byte, stageHandler func(atrustclient.SetupStage)) ([]byte, error) {
			return client.SetupContext(ctx, atrustclient.SetupConfig{
				ServerAddress:           config.ServerAddress,
				ServerPort:              config.ServerPort,
				Username:                config.Username,
				Password:                config.Password,
				Phone:                   config.Phone,
				LoginDomain:             config.LoginDomain,
				AuthType:                config.AuthType,
				GraphCodeFile:           config.GraphCodeFile,
				CASTicket:               config.CASTicket,
				OAuth2Code:              config.OAuth2Code,
				UpdateBestNodesInterval: config.UpdateBestNodesInterval,
				BindInterface:           config.BindInterface,
				AutoDetectInterface:     config.AutoDetectInterface,
				StageHandler:            stageHandler,
				NodeSelectionHandler:    config.NodeSelectionHandler,
			}, clientData, resourceData)
		},
		setupNetwork: func(ctx context.Context, client clientpkg.Client, config Config) (core.Outbound, error) {
			networkConfig := networkruntime.Config{
				TCPTunnelMode:        config.TCPTunnelMode,
				DisableRemoteDNS:     config.DisableRemoteDNS,
				RemoteDNSServer:      config.RemoteDNSServer,
				SecondaryDNSServer:   config.SecondaryDNSServer,
				DNSTTL:               config.DNSTTL,
				DNSBind:              config.DNSBind,
				Hosts:                config.Hosts,
				SOCKSBind:            config.SOCKSBind,
				SOCKSUsername:        config.SOCKSUsername,
				SOCKSPassword:        config.SOCKSPassword,
				HTTPBind:             config.HTTPBind,
				TUNEnabled:           config.TUNEnabled,
				TUNName:              config.TUNName,
				TUNAddress:           config.TUNAddress,
				TUNMTU:               config.TUNMTU,
				TUNAutoRoute:         config.TUNAutoRoute,
				TUNRouteAll:          config.TUNRouteAll,
				TUNStrictRoute:       config.TUNStrictRoute,
				TUNStack:             config.TUNStack,
				TUNOutboundInterface: config.TUNOutboundInterface,
				TUNUDPTimeoutSeconds: config.TUNUDPTimeoutSeconds,
				TUNUDPMaxFlows:       config.TUNUDPMaxFlows,
				TUNDNSHijack:         config.TUNDNSHijack,
				TUNFakeIP:            config.TUNFakeIP,
				TUNFakeIPRange:       config.TUNFakeIPRange,
				ControlServerHost:    config.ServerAddress,
				RoutingMode:          config.RoutingMode,
				InternetOutbound:     config.InternetOutbound,
			}
			if config.NetworkRuntime != nil {
				if err := config.NetworkRuntime.ReplaceVPN(ctx, client, networkConfig); err != nil {
					return nil, err
				}
				return config.NetworkRuntime, nil
			}
			return networkruntime.New(ctx, client, networkConfig)
		},
		readResources: snapshotResources,
		wait: func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
}

func start(ctx context.Context, config Config, deps dependencies) (*Runtime, error) {
	return startWithStageHandler(ctx, config, deps, nil)
}

func startWithStageHandler(ctx context.Context, config Config, deps dependencies, stageHandler func(atrustclient.SetupStage)) (*Runtime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var resourceData []byte
	if config.ResourceFile != "" {
		var err error
		resourceData, err = deps.readFile(config.ResourceFile)
		if err != nil {
			return nil, core.WrapError(
				core.ErrorCodeResourceDataReadFailed,
				fmt.Sprintf("read resource file %q", config.ResourceFile),
				false,
				err,
			)
		}
	}

	var clientData []byte
	var resumeRevision uint64
	if config.ResumeState != nil && config.ClientDataFile != "" {
		return nil, core.WrapError(core.ErrorCodeInvalidRequest, "resume_state and client_data_file cannot be used together", false, nil)
	}
	if config.ResumeState != nil {
		var err error
		clientData, resumeRevision, err = decodeResumeState(config, *config.ResumeState)
		if err != nil {
			return nil, err
		}
	} else if config.ClientDataFile != "" {
		var err error
		clientData, err = deps.readFile(config.ClientDataFile)
		if err != nil {
			log.Printf("Read client data file error: %s", err)
			log.Println("Will create a new client data file if log in successfully")
		}
	}

	atrustClient := deps.newClient(ctx, config.Username, config.SID, config.DeviceID, config.SignKey)
	atrustClient.SetSkipTCPTunnelWait(config.SkipTCPTunnelWait)
	deps.setAuthHandler(atrustClient, config.AuthHandler)
	runtime := &Runtime{
		client: atrustClient,
		closeClient: func() {
			deps.closeClient(atrustClient)
		},
	}

	clientData, err := deps.setup(ctx, atrustClient, config, clientData, resourceData, stageHandler)
	if err != nil {
		runtime.Close()
		return nil, wrapATrustSetupError(err)
	}
	runtime.resumeState = encodeResumeState(config, atrustClient, clientData, resumeRevision+1)

	if config.ClientDataFile != "" {
		if err := deps.writeFile(config.ClientDataFile, clientData); err != nil {
			runtime.Close()
			return nil, core.WrapError(
				core.ErrorCodeClientDataWriteFailed,
				fmt.Sprintf("write client data file %q", config.ClientDataFile),
				false,
				err,
			)
		}
		log.Printf("Client data saved to %s", config.ClientDataFile)
	}

	if config.SetupNetwork {
		outbound, err := deps.setupNetwork(ctx, atrustClient, config)
		if err != nil {
			runtime.Close()
			if code := core.ErrorCodeOf(err); code != core.ErrorCodeUnknown {
				return nil, core.WrapError(code, "setup VPN network runtime", core.IsRetryable(err), err)
			}
			return nil, core.WrapError(core.ErrorCodeNetworkSetupFailed, "setup VPN network runtime", false, err)
		}
		if outbound == nil {
			runtime.Close()
			return nil, core.WrapError(core.ErrorCodeNetworkSetupFailed, "setup VPN network runtime", false, fmt.Errorf("network runtime returned no outbound"))
		}
		runtime.outbound = wrapNetwork(outbound)
		runtime.ownsOutbound = true
	}

	return runtime, nil
}

func wrapATrustSetupError(err error) error {
	code := core.ErrorCodeOf(err)
	if code == core.ErrorCodeUnknown {
		code = core.ErrorCodeATrustSetupFailed
	}
	return core.WrapError(code, "setup aTrust client", isRetryableATrustSetupError(err), err)
}

func isRetryableATrustSetupError(err error) bool {
	if core.ErrorCodeOf(err) != core.ErrorCodeUnknown {
		return core.IsRetryable(err)
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkError net.Error
	if errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary()) {
		return true
	}
	for _, retryable := range []error{
		syscall.ECONNABORTED,
		syscall.ECONNREFUSED,
		syscall.ECONNRESET,
		syscall.EHOSTUNREACH,
		syscall.ENETDOWN,
		syscall.ENETRESET,
		syscall.ENETUNREACH,
		syscall.ETIMEDOUT,
	} {
		if errors.Is(err, retryable) {
			return true
		}
	}
	return false
}
