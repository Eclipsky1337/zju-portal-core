package v1

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

const maxJSONLMessageSize = 4 * 1024 * 1024
const shutdownTimeout = 5 * time.Second

type Server struct {
	service    *Service
	ownService bool

	writeMu sync.Mutex
	writer  io.Writer
}

func NewServer(manager core.Manager, coreVersion string, capabilities []string) *Server {
	return &Server{service: NewService(manager, coreVersion, capabilities), ownService: true}
}

func NewServerWithService(service *Service) *Server {
	return &Server{service: service}
}

func (server *Server) Serve(ctx context.Context, reader io.Reader, writer io.Writer) error {
	server.writer = writer
	requestCtx, cancelRequests := context.WithCancel(ctx)
	defer cancelRequests()
	eventCtx, cancelEvents := context.WithCancel(context.Background())
	defer cancelEvents()
	events := server.service.Subscribe(eventCtx)

	var eventWaitGroup sync.WaitGroup
	eventWaitGroup.Add(1)
	go func() {
		defer eventWaitGroup.Done()
		server.forwardEvents(eventCtx, events)
	}()

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxJSONLMessageSize)
	var requestWaitGroup sync.WaitGroup
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		requestWaitGroup.Add(1)
		go func() {
			defer requestWaitGroup.Done()
			server.handleLine(requestCtx, line)
		}()
	}
	cancelRequests()
	requestWaitGroup.Wait()

	var closeErr error
	if server.ownService {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
		closeErr = server.service.Close(shutdownCtx)
		cancelShutdown()
	} else {
		cancelEvents()
	}
	eventWaitGroup.Wait()
	scanErr := scanner.Err()
	if ctx.Err() != nil {
		scanErr = nil
	}
	return errors.Join(scanErr, closeErr)
}

func (server *Server) handleLine(ctx context.Context, line []byte) {
	var request Request
	if err := json.Unmarshal(line, &request); err != nil {
		server.writeResponse(Response{
			ID:    json.RawMessage("null"),
			Error: core.WrapError(core.ErrorCodeInvalidRequest, "decode JSONL request", false, err),
		})
		return
	}
	if len(request.ID) == 0 || request.Method == "" {
		server.writeResponse(Response{
			ID:    normalizeID(request.ID),
			Error: core.WrapError(core.ErrorCodeInvalidRequest, "request ID and method are required", false, nil),
		})
		return
	}

	result, err := server.service.Call(ctx, request.Method, request.Params)
	response := Response{ID: request.ID, Result: result}
	if err != nil {
		response.Result = nil
		response.Error = asCoreError(err)
	}
	server.writeResponse(response)
}

func (server *Server) forwardEvents(ctx context.Context, events <-chan core.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			server.write(Event{Event: event.Type, Params: event})
		}
	}
}

func (server *Server) writeResponse(response Response) {
	server.write(response)
}

func (server *Server) write(value any) {
	server.writeMu.Lock()
	defer server.writeMu.Unlock()
	encoder := json.NewEncoder(server.writer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func normalizeID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}
