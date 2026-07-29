package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/log"
)

// The MIT License (MIT)
//
// Copyright (c) 2016 Ian Denhardt <ian@zenhack.net>
//
// Permission is hereby granted, free of charge, to any person obtaining a copy of
// this software and associated documentation files (the "Software"), to deal in
// the Software without restriction, including without limitation the rights to
// use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
// the Software, and to permit persons to whom the Software is furnished to do so,
// subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
// FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
// COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
// IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
// CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

type HTTPService struct {
	bindAddr string
	dialer   contextDialer
	observer core.ConnectionObserver
	client   *http.Client
	server   *http.Server
	listen   func(network, address string) (net.Listener, error)

	mu          sync.Mutex
	listener    net.Listener
	closed      bool
	stopContext func() bool
	runErr      error
	connections map[*proxyConnection]struct{}

	startOnce sync.Once
	startErr  error
	closeOnce sync.Once
	closeErr  error
	closeDone chan struct{}
	runDone   chan struct{}
}

var _ core.Component = (*HTTPService)(nil)

type contextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

func NewHTTPService(bindAddr string, dialer contextDialer) *HTTPService {
	return NewHTTPServiceWithObserver(bindAddr, dialer, nil)
}

func NewHTTPServiceWithObserver(bindAddr string, dialer contextDialer, observer core.ConnectionObserver) *HTTPService {
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	service := &HTTPService{
		bindAddr: bindAddr,
		dialer:   dialer,
		observer: observer,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		connections: make(map[*proxyConnection]struct{}),
		runDone:     make(chan struct{}),
		closeDone:   make(chan struct{}),
		listen:      net.Listen,
	}
	service.server = &http.Server{Handler: http.HandlerFunc(service.handle)}
	return service
}

func (service *HTTPService) Start(ctx context.Context) error {
	service.startOnce.Do(func() {
		service.startErr = service.start(ctx)
	})
	return service.startErr
}

func (service *HTTPService) start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	listener, err := service.listen("tcp", service.bindAddr)
	if err != nil {
		return fmt.Errorf("listen HTTP proxy on %s: %w", service.bindAddr, err)
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

	log.Printf("HTTP server listening on %s", listener.Addr())
	go func() {
		err := service.server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			err = nil
		}
		service.mu.Lock()
		service.runErr = err
		service.mu.Unlock()
		close(service.runDone)
	}()
	return nil
}

func (service *HTTPService) handle(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodConnect {
		service.handleConnect(writer, request)
		return
	}

	requestContext, cancel := context.WithCancel(request.Context())
	request = request.WithContext(requestContext)
	destination := request.URL.Host
	if destination == "" {
		destination = request.Host
	}
	activity := openConnection(service.observer, core.ConnectionMetadata{
		Inbound:     "http",
		Source:      request.RemoteAddr,
		Network:     "tcp",
		Destination: destination,
	}, func() error {
		cancel()
		return nil
	})
	defer func() {
		if activity != nil {
			_ = activity.Close()
		}
	}()
	if request.Body != nil && activity != nil {
		request.Body = &countingReadCloser{reader: request.Body, closer: request.Body, record: activity.RecordUploaded}
	}

	request.RequestURI = ""
	response, err := service.client.Do(request)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	defer response.Body.Close()
	for key, values := range response.Header {
		writer.Header()[key] = values
	}
	writer.WriteHeader(response.StatusCode)
	if activity == nil {
		_, _ = io.Copy(writer, response.Body)
		return
	}
	_, _ = io.Copy(writer, &countingReadCloser{reader: response.Body, closer: response.Body, record: activity.RecordDownloaded})
}

func (service *HTTPService) handleConnect(writer http.ResponseWriter, request *http.Request) {
	serverConn, err := service.dialer.DialContext(request.Context(), "tcp", request.Host)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	activity := openConnection(service.observer, core.ConnectionMetadata{
		Inbound:               "http",
		Outbound:              core.RouteInfoOf(serverConn).Outbound,
		RouteReason:           core.RouteInfoOf(serverConn).Reason,
		Source:                request.RemoteAddr,
		Network:               "tcp",
		Destination:           request.Host,
		TransportConnectionID: connectionID(serverConn),
	}, serverConn.Close)
	serverConn = observeConnection(serverConn, activity)

	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		_ = serverConn.Close()
		http.Error(writer, "connection hijacking unavailable", http.StatusInternalServerError)
		return
	}
	clientConn, buffered, err := hijacker.Hijack()
	if err != nil {
		_ = serverConn.Close()
		return
	}
	if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = clientConn.Close()
		_ = serverConn.Close()
		return
	}
	if err := buffered.Flush(); err != nil {
		_ = clientConn.Close()
		_ = serverConn.Close()
		return
	}

	connection := &proxyConnection{client: clientConn, server: serverConn}
	service.mu.Lock()
	if service.closed {
		service.mu.Unlock()
		_ = connection.Close()
		return
	}
	service.connections[connection] = struct{}{}
	service.mu.Unlock()
	go func() {
		_ = relay(clientConn, serverConn)
		_ = connection.Close()
		service.mu.Lock()
		delete(service.connections, connection)
		service.mu.Unlock()
	}()
}

func (service *HTTPService) Close(ctx context.Context) error {
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

func (service *HTTPService) closeResources() error {
	service.mu.Lock()
	service.closed = true
	listener := service.listener
	stopContext := service.stopContext
	connections := make([]*proxyConnection, 0, len(service.connections))
	for connection := range service.connections {
		connections = append(connections, connection)
	}
	service.mu.Unlock()
	if stopContext != nil {
		stopContext()
	}

	var connectionErrors []error
	for _, connection := range connections {
		connectionErrors = append(connectionErrors, connection.Close())
	}
	if transport, ok := service.client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
	shutdownErr := service.server.Close()
	if listener != nil {
		<-service.runDone
		service.mu.Lock()
		runErr := service.runErr
		service.mu.Unlock()
		shutdownErr = errors.Join(shutdownErr, runErr)
	}
	return errors.Join(shutdownErr, errors.Join(connectionErrors...))
}

func (service *HTTPService) Addr() net.Addr {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.listener == nil {
		return nil
	}
	return service.listener.Addr()
}

func (service *HTTPService) Done() <-chan struct{} { return service.runDone }

func (service *HTTPService) Err() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.runErr
}

type proxyConnection struct {
	client net.Conn
	server net.Conn
	once   sync.Once
	err    error
}

func (connection *proxyConnection) Close() error {
	connection.once.Do(func() {
		connection.err = errors.Join(connection.client.Close(), connection.server.Close())
	})
	return connection.err
}
