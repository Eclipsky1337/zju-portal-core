package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

type authHandlerFunc func(context.Context, core.AuthChallenge) (core.AuthResponse, error)

func (handler authHandlerFunc) Handle(ctx context.Context, challenge core.AuthChallenge) (core.AuthResponse, error) {
	return handler(ctx, challenge)
}

func TestSessionHandleChallenge(t *testing.T) {
	wantChallenge := core.AuthChallenge{ID: "sms-1", Kind: core.AuthChallengeSMS, Prompt: "SMS code"}
	session := NewSession("gateway.test")
	session.SetAuthHandler(authHandlerFunc(func(_ context.Context, challenge core.AuthChallenge) (core.AuthResponse, error) {
		if !reflect.DeepEqual(challenge, wantChallenge) {
			t.Fatalf("challenge = %#v, want %#v", challenge, wantChallenge)
		}
		return core.AuthResponse{ChallengeID: challenge.ID, Value: "123456"}, nil
	}))

	response, err := session.handleChallenge(context.Background(), wantChallenge)
	if err != nil {
		t.Fatalf("handleChallenge() error = %v", err)
	}
	if response.Value != "123456" {
		t.Fatalf("response = %#v", response)
	}
}

func TestSessionHandleChallengeRejectsMissingHandler(t *testing.T) {
	session := NewSession("gateway.test")
	_, err := session.handleChallenge(context.Background(), core.AuthChallenge{ID: "sms-1", Kind: core.AuthChallengeSMS})
	if code := core.ErrorCodeOf(err); code != core.ErrorCodeAuthHandlerUnavailable {
		t.Fatalf("error code = %q", code)
	}
}

func TestSessionHandleChallengePreservesHandlerError(t *testing.T) {
	wantErr := errors.New("frontend closed")
	session := NewSession("gateway.test")
	session.SetAuthHandler(authHandlerFunc(func(context.Context, core.AuthChallenge) (core.AuthResponse, error) {
		return core.AuthResponse{}, wantErr
	}))

	_, err := session.handleChallenge(context.Background(), core.AuthChallenge{ID: "sms-1", Kind: core.AuthChallengeSMS})
	if !errors.Is(err, wantErr) {
		t.Fatalf("handleChallenge() error = %v", err)
	}
}

func TestSessionHandleChallengeValidatesResponse(t *testing.T) {
	session := NewSession("gateway.test")
	session.SetAuthHandler(authHandlerFunc(func(context.Context, core.AuthChallenge) (core.AuthResponse, error) {
		return core.AuthResponse{ChallengeID: "wrong", Value: "123456"}, nil
	}))

	_, err := session.handleChallenge(context.Background(), core.AuthChallenge{ID: "sms-1", Kind: core.AuthChallengeSMS})
	if code := core.ErrorCodeOf(err); code != core.ErrorCodeAuthResponseInvalid {
		t.Fatalf("error code = %q", code)
	}
}

func TestLoginContextHonorsCancellationBeforeRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	session := NewSession("gateway.test")

	_, err := session.LoginContext(ctx, nil, LoginOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("LoginContext() error = %v", err)
	}
}

func TestSessionHandleChallengePropagatesContext(t *testing.T) {
	type contextKey string
	const key contextKey = "request-id"
	ctx := context.WithValue(context.Background(), key, "request-1")
	session := NewSession("gateway.test")
	session.SetAuthHandler(authHandlerFunc(func(handlerCtx context.Context, challenge core.AuthChallenge) (core.AuthResponse, error) {
		if handlerCtx.Value(key) != "request-1" {
			t.Fatalf("handler context value = %v", handlerCtx.Value(key))
		}
		return core.AuthResponse{ChallengeID: challenge.ID, Value: "123456"}, nil
	}))

	_, err := session.handleChallenge(ctx, core.AuthChallenge{ID: "sms-1", Kind: core.AuthChallengeSMS})
	if err != nil {
		t.Fatalf("handleChallenge() error = %v", err)
	}
}

func TestLoginContextCancelsInFlightHTTPRequest(t *testing.T) {
	requestStarted := make(chan struct{})
	session := NewSession("gateway.test")
	session.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := session.LoginContext(ctx, nil, LoginOptions{})
		result <- err
	}()
	<-requestStarted
	cancel()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("LoginContext() error = %v", err)
	}
}

func TestLoginContextFallsBackWhenSavedSessionIsRejected(t *testing.T) {
	var loginCalls atomic.Int32
	method := loginMethodFunc{
		authType:    "auth/test",
		loginDomain: "default",
		loginFunc: func(_ context.Context, session *Session, _ AuthInfo) error {
			loginCalls.Add(1)
			cookies := session.client.Jar.Cookies(&url.URL{Host: session.baseHost, Scheme: "https"})
			if len(cookies) != 1 || cookies[0].Name != "sid" || cookies[0].Value != "expired-sid" {
				t.Fatalf("saved cookies passed to fallback login = %#v", cookies)
			}
			session.ticket = "new-ticket"
			session.client.Jar.SetCookies(&url.URL{Host: session.baseHost, Scheme: "https"}, []*http.Cookie{{Name: "sid", Value: "new-sid"}})
			return nil
		},
	}
	session := NewSession("gateway.test")
	session.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/passport/v1/public/authConfig":
			return jsonResponse(request, `{"data":{"isLogin":0,"security":{"csrfToken":"csrf"},"authServerInfoList":[{"loginDomain":"default","authType":"auth/test"}]}}`), nil
		case "/controller/v1/public/reportEnv":
			return jsonResponse(request, `{"code":0}`), nil
		case "/passport/v1/auth/authCheck":
			return jsonResponse(request, `{"code":0,"data":{}}`), nil
		case "/passport/v1/user/onlineInfo":
			return jsonResponse(request, `{"code":0,"data":{"username":"user"}}`), nil
		default:
			return nil, fmt.Errorf("unexpected request path %q", request.URL.Path)
		}
	})

	result, err := session.LoginContext(context.Background(), method, LoginOptions{
		DeviceID: "device-id",
		Cookies:  []Cookie{{Host: "gateway.test", Scheme: "https", Name: "sid", Value: "expired-sid"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if loginCalls.Load() != 1 || result.Reused || result.SID != "new-sid" || result.Username != "user" {
		t.Fatalf("fallback result/calls = %#v/%d", result, loginCalls.Load())
	}
}

type loginMethodFunc struct {
	authType    string
	loginDomain string
	loginFunc   func(context.Context, *Session, AuthInfo) error
}

func (method loginMethodFunc) AuthType() string    { return method.authType }
func (method loginMethodFunc) LoginDomain() string { return method.loginDomain }
func (method loginMethodFunc) login(ctx context.Context, session *Session, info AuthInfo) error {
	return method.loginFunc(ctx, session, info)
}
