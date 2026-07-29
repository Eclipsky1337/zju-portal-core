package rest

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	controlv1 "github.com/Eclipsky1337/zju-portal-core/control/v1"
	"github.com/Eclipsky1337/zju-portal-core/core"
)

const (
	APIBasePath        = "/api/v1"
	maxRequestBodySize = 4 * 1024 * 1024
	eventKeepAlive     = 15 * time.Second
)

type Server struct {
	service      *controlv1.Service
	token        string
	lifecycleCtx context.Context
	mux          *http.ServeMux
}

type responseEnvelope struct {
	Result any         `json:"result,omitempty"`
	Error  *core.Error `json:"error,omitempty"`
}

func NewServer(service *controlv1.Service, token string) *Server {
	return NewServerContext(context.Background(), service, token)
}

func NewServerContext(ctx context.Context, service *controlv1.Service, token string) *Server {
	server := &Server{service: service, token: token, lifecycleCtx: ctx, mux: http.NewServeMux()}
	server.mux.HandleFunc(APIBasePath+"/hello", server.handleHello)
	server.mux.HandleFunc(APIBasePath+"/sessions", server.handleSessions)
	server.mux.HandleFunc(APIBasePath+"/sessions/", server.handleSession)
	server.mux.HandleFunc(APIBasePath+"/auth/responses", server.handleAuthResponse)
	server.mux.HandleFunc(APIBasePath+"/events", server.handleEvents)
	server.mux.HandleFunc(APIBasePath+"/config", server.handleConfig)
	server.mux.HandleFunc(APIBasePath+"/config/reload", server.handleConfigReload)
	return server
}

func (server *Server) handleConfig(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		server.call(writer, request, controlv1.MethodConfigGet, nil)
	case http.MethodPut:
		var params controlv1.ConfigSetParams
		if err := decodeBody(writer, request, &params.Config); err != nil {
			return
		}
		raw, _ := json.Marshal(params)
		server.callContext(writer, server.lifecycleCtx, controlv1.MethodConfigSet, raw)
	default:
		methodNotAllowed(writer, http.MethodGet, http.MethodPut)
	}
}

func (server *Server) handleConfigReload(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, http.MethodPost)
		return
	}
	server.callContext(writer, server.lifecycleCtx, controlv1.MethodConfigReload, nil)
}

func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if !server.authorized(request) {
		writeError(writer, http.StatusUnauthorized, core.WrapError(core.ErrorCodeInvalidRequest, "unauthorized control request", false, nil))
		return
	}
	if !sameOrigin(request) {
		writeError(writer, http.StatusForbidden, core.WrapError(core.ErrorCodeInvalidRequest, "cross-origin control request is forbidden", false, nil))
		return
	}
	server.mux.ServeHTTP(writer, request)
}

func (server *Server) handleHello(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	params, _ := json.Marshal(controlv1.HelloParams{ProtocolVersion: controlv1.ProtocolVersion})
	server.call(writer, request, controlv1.MethodHello, params)
}

func (server *Server) handleSessions(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, http.MethodPost)
		return
	}
	var params controlv1.SessionStartParams
	if err := decodeBody(writer, request, &params); err != nil {
		return
	}
	raw, _ := json.Marshal(params)
	server.callContext(writer, server.lifecycleCtx, controlv1.MethodSessionStart, raw)
}

func (server *Server) handleSession(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, APIBasePath+"/sessions/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(writer, request)
		return
	}
	sessionID, err := url.PathUnescape(parts[0])
	if err != nil {
		writeError(writer, http.StatusBadRequest, core.WrapError(core.ErrorCodeInvalidRequest, "invalid session ID", false, err))
		return
	}
	params, _ := json.Marshal(controlv1.SessionIDParams{SessionID: core.SessionID(sessionID)})

	if len(parts) == 1 {
		switch request.Method {
		case http.MethodGet:
			server.call(writer, request, controlv1.MethodSessionStatus, params)
		case http.MethodDelete:
			server.call(writer, request, controlv1.MethodSessionStop, params)
		default:
			methodNotAllowed(writer, http.MethodGet, http.MethodDelete)
		}
		return
	}
	if len(parts) == 3 && parts[1] == "connections" {
		if request.Method != http.MethodDelete {
			methodNotAllowed(writer, http.MethodDelete)
			return
		}
		connectionID, err := url.PathUnescape(parts[2])
		if err != nil {
			writeError(writer, http.StatusBadRequest, core.WrapError(core.ErrorCodeInvalidRequest, "invalid connection ID", false, err))
			return
		}
		closeParams, _ := json.Marshal(controlv1.ConnectionCloseParams{SessionID: core.SessionID(sessionID), ConnectionID: connectionID})
		server.call(writer, request, controlv1.MethodConnectionClose, closeParams)
		return
	}
	if len(parts) == 3 && parts[1] == "resources" && parts[2] == "refresh" {
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, http.MethodPost)
			return
		}
		server.call(writer, request, controlv1.MethodResourcesRefresh, params)
		return
	}
	if len(parts) == 2 && parts[1] == "routing" {
		switch request.Method {
		case http.MethodGet:
			server.call(writer, request, controlv1.MethodRoutingModeGet, params)
		case http.MethodPut:
			var routingParams controlv1.RoutingModeSetParams
			if err := decodeBody(writer, request, &routingParams); err != nil {
				return
			}
			routingParams.SessionID = core.SessionID(sessionID)
			raw, _ := json.Marshal(routingParams)
			server.call(writer, request, controlv1.MethodRoutingModeSet, raw)
		default:
			methodNotAllowed(writer, http.MethodGet, http.MethodPut)
		}
		return
	}
	if len(parts) != 2 || request.Method != http.MethodGet {
		if len(parts) == 2 {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		http.NotFound(writer, request)
		return
	}
	switch parts[1] {
	case "resources":
		server.call(writer, request, controlv1.MethodResourcesGet, params)
	case "services":
		server.call(writer, request, controlv1.MethodServicesGet, params)
	case "traffic":
		server.call(writer, request, controlv1.MethodTrafficGet, params)
	case "connections":
		server.call(writer, request, controlv1.MethodConnectionsList, params)
	case "transport-connections":
		server.call(writer, request, controlv1.MethodTransportConnectionsList, params)
	case "resume-state":
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Pragma", "no-cache")
		server.call(writer, request, controlv1.MethodResumeStateGet, params)
	default:
		http.NotFound(writer, request)
	}
}

func (server *Server) handleAuthResponse(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, http.MethodPost)
		return
	}
	var response core.AuthResponse
	if err := decodeBody(writer, request, &response); err != nil {
		return
	}
	raw, _ := json.Marshal(response)
	server.call(writer, request, controlv1.MethodAuthRespond, raw)
}

func (server *Server) handleEvents(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, http.StatusInternalServerError, core.WrapError(core.ErrorCodeUnknown, "streaming is unsupported", false, nil))
		return
	}
	events := server.service.Subscribe(request.Context())
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("X-Accel-Buffering", "no")
	_, _ = fmt.Fprint(writer, ": connected\n\n")
	flusher.Flush()

	ticker := time.NewTicker(eventKeepAlive)
	defer ticker.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case <-ticker.C:
			_, _ = fmt.Fprint(writer, ": keepalive\n\n")
			flusher.Flush()
		case event, ok := <-events:
			if !ok {
				return
			}
			data, err := json.Marshal(event)
			if err != nil {
				return
			}
			_, _ = fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event.Type, data)
			flusher.Flush()
		}
	}
}

func (server *Server) call(writer http.ResponseWriter, request *http.Request, method string, params json.RawMessage) {
	server.callContext(writer, request.Context(), method, params)
}

func (server *Server) callContext(writer http.ResponseWriter, ctx context.Context, method string, params json.RawMessage) {
	result, err := server.service.Call(ctx, method, params)
	if err != nil {
		coreError := asCoreError(err)
		writeError(writer, statusForError(coreError), coreError)
		return
	}
	writeJSON(writer, http.StatusOK, responseEnvelope{Result: result})
}

func (server *Server) authorized(request *http.Request) bool {
	if server.token == "" {
		return true
	}
	provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	if len(provided) == len(server.token) && subtle.ConstantTimeCompare([]byte(provided), []byte(server.token)) == 1 {
		return true
	}
	if request.URL.Path == APIBasePath+"/events" {
		provided = request.URL.Query().Get("access_token")
		return len(provided) == len(server.token) && subtle.ConstantTimeCompare([]byte(provided), []byte(server.token)) == 1
	}
	return false
}

func sameOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Host == request.Host && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func decodeBody(writer http.ResponseWriter, request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBodySize)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(writer, http.StatusBadRequest, core.WrapError(core.ErrorCodeInvalidRequest, "decode request body", false, err))
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		decodeErr := err
		if err == nil {
			decodeErr = errors.New("request body contains multiple JSON values")
		}
		writeError(writer, http.StatusBadRequest, core.WrapError(core.ErrorCodeInvalidRequest, "decode request body", false, decodeErr))
		return decodeErr
	}
	return nil
}

func methodNotAllowed(writer http.ResponseWriter, methods ...string) {
	writer.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(writer, http.StatusMethodNotAllowed, core.WrapError(core.ErrorCodeMethodNotFound, "HTTP method is not supported", false, nil))
}

func writeError(writer http.ResponseWriter, status int, err *core.Error) {
	writeJSON(writer, status, responseEnvelope{Error: err})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func statusForError(err *core.Error) int {
	switch err.Code {
	case core.ErrorCodeInvalidRequest, core.ErrorCodeAuthResponseInvalid, core.ErrorCodeAuthChallengeInvalid, core.ErrorCodeConfigInvalid:
		return http.StatusBadRequest
	case core.ErrorCodeSessionNotFound, core.ErrorCodeAuthChallengeNotFound, core.ErrorCodeConnectionNotFound:
		return http.StatusNotFound
	case core.ErrorCodeSessionNotReady, core.ErrorCodeSessionStartFailed, core.ErrorCodeSessionCloseFailed, core.ErrorCodeRestartRequired:
		return http.StatusConflict
	case core.ErrorCodeAddressInUse:
		return http.StatusConflict
	case core.ErrorCodePermissionDenied:
		return http.StatusForbidden
	case core.ErrorCodeTUNUnavailable, core.ErrorCodeInterfaceUnavailable, core.ErrorCodeRouteSetupFailed, core.ErrorCodeDNSStartFailed, core.ErrorCodeOutboundUnavailable:
		return http.StatusServiceUnavailable
	case core.ErrorCodeMethodNotFound, core.ErrorCodeConfigUnavailable:
		return http.StatusNotFound
	case core.ErrorCodeProtocolUnsupported:
		return http.StatusUpgradeRequired
	default:
		return http.StatusInternalServerError
	}
}

func asCoreError(err error) *core.Error {
	var coreError *core.Error
	if errors.As(err, &coreError) {
		return coreError
	}
	return core.WrapError(core.ErrorCodeUnknown, err.Error(), false, err)
}
