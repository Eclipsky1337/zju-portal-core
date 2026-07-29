package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/log"
	"github.com/things-go/go-socks5"
)

type Socks5Service struct {
	bindAddr string
	server   *socks5.Server
	listen   func(network, address string) (net.Listener, error)

	mu          sync.Mutex
	listener    net.Listener
	closed      bool
	stopContext func() bool
	runErr      error

	startOnce sync.Once
	startErr  error
	closeOnce sync.Once
	closeErr  error
	closeDone chan struct{}
	runDone   chan struct{}
}

var _ core.Component = (*Socks5Service)(nil)

func NewSocks5Service(bindAddr string, dialer contextDialer, resolver socks5.NameResolver, user, password string) *Socks5Service {
	return NewSocks5ServiceWithObserver(bindAddr, dialer, resolver, user, password, nil)
}

func NewSocks5ServiceWithObserver(bindAddr string, dialer contextDialer, resolver socks5.NameResolver, user, password string, observer core.ConnectionObserver) *Socks5Service {
	var authMethods []socks5.Authenticator
	if user != "" && password != "" {
		authMethods = append(authMethods, socks5.UserPassAuthenticator{
			Credentials: socks5.StaticCredentials{user: password},
		})

		log.Println("Neither traffic nor credentials are encrypted in the SOCKS5 protocol!")
		log.Println("DO NOT deploy it to the public network. All consequences and responsibilities have nothing to do with the developer")
	} else {
		authMethods = append(authMethods, socks5.NoAuthAuthenticator{})
	}

	return &Socks5Service{
		bindAddr: bindAddr,
		server: socks5.NewServer(
			socks5.WithAuthMethods(authMethods),
			socks5.WithResolver(resolver),
			socks5.WithDialAndRequest(func(ctx context.Context, network, address string, request *socks5.Request) (net.Conn, error) {
				conn, err := dialer.DialContext(ctx, network, address)
				if err != nil {
					return nil, err
				}
				source := ""
				if request.RemoteAddr != nil {
					source = request.RemoteAddr.String()
				}
				activity := openConnection(observer, core.ConnectionMetadata{
					Inbound:               "socks5",
					Outbound:              core.RouteInfoOf(conn).Outbound,
					RouteReason:           core.RouteInfoOf(conn).Reason,
					Source:                source,
					Network:               network,
					Destination:           address,
					TransportConnectionID: connectionID(conn),
				}, conn.Close)
				return observeConnection(conn, activity), nil
			}),
			socks5.WithLogger(socks5.NewLogger(log.NewLogger("[SOCKS5] "))),
		),
		runDone:   make(chan struct{}),
		closeDone: make(chan struct{}),
		listen:    net.Listen,
	}
}

func (service *Socks5Service) Start(ctx context.Context) error {
	service.startOnce.Do(func() {
		service.startErr = service.start(ctx)
	})
	return service.startErr
}

func (service *Socks5Service) start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	listener, err := service.listen("tcp", service.bindAddr)
	if err != nil {
		return fmt.Errorf("listen SOCKS5 on %s: %w", service.bindAddr, err)
	}

	service.mu.Lock()
	if service.closed {
		service.mu.Unlock()
		_ = listener.Close()
		return context.Canceled
	}
	service.listener = listener
	service.stopContext = context.AfterFunc(ctx, func() {
		_ = service.Close(context.Background())
	})
	service.mu.Unlock()

	log.Printf("SOCKS5 server listening on %s", listener.Addr())
	go func() {
		err := service.server.Serve(listener)
		if errors.Is(err, net.ErrClosed) {
			err = nil
		}
		service.mu.Lock()
		service.runErr = err
		service.mu.Unlock()
		close(service.runDone)
	}()
	return nil
}

func (service *Socks5Service) Close(ctx context.Context) error {
	service.closeOnce.Do(func() {
		go func() {
			service.closeErr = service.closeResources()
			close(service.closeDone)
		}()
	})
	select {
	case <-service.closeDone:
		return service.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (service *Socks5Service) closeResources() error {
	service.mu.Lock()
	service.closed = true
	listener := service.listener
	stopContext := service.stopContext
	service.mu.Unlock()
	if stopContext != nil {
		stopContext()
	}
	var closeErr error
	if listener != nil {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			closeErr = fmt.Errorf("close SOCKS5 listener: %w", err)
		}
		<-service.runDone
		service.mu.Lock()
		runErr := service.runErr
		service.mu.Unlock()
		closeErr = errors.Join(closeErr, runErr)
	}
	return closeErr
}

func (service *Socks5Service) Addr() net.Addr {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.listener == nil {
		return nil
	}
	return service.listener.Addr()
}

func (service *Socks5Service) Done() <-chan struct{} { return service.runDone }

func (service *Socks5Service) Err() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.runErr
}
