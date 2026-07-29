package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/Eclipsky1337/zju-portal-core/core"
)

func TestInteractiveCASUsesAuthenticationHandler(t *testing.T) {
	session := NewSession("gateway.test")
	session.SetAuthHandler(authHandlerFunc(func(_ context.Context, challenge core.AuthChallenge) (core.AuthResponse, error) {
		if challenge.Kind != core.AuthChallengeCASCallback || challenge.URL != "https://cas.example.edu.cn/login" {
			t.Fatalf("challenge = %#v", challenge)
		}
		return core.AuthResponse{
			ChallengeID: challenge.ID,
			Value:       "https://gateway.test/passport/v1/auth/cas?ticket=cas-ticket",
		}, nil
	}))

	callback, err := session.interactiveCas(context.Background(), "https://cas.example.edu.cn/login")
	if err != nil {
		t.Fatalf("interactiveCas() error = %v", err)
	}
	if callback != "https://gateway.test/passport/v1/auth/cas?ticket=cas-ticket" {
		t.Fatalf("callback = %q", callback)
	}
}

func TestInteractiveOAuthUsesAuthenticationHandler(t *testing.T) {
	session := NewSession("gateway.test")
	session.SetAuthHandler(authHandlerFunc(func(_ context.Context, challenge core.AuthChallenge) (core.AuthResponse, error) {
		if challenge.Kind != core.AuthChallengeOAuthCallback || challenge.URL != "https://oauth.example.edu.cn/login" {
			t.Fatalf("challenge = %#v", challenge)
		}
		return core.AuthResponse{
			ChallengeID: challenge.ID,
			Value:       "https://gateway.test/passport/v1/auth/httpsOauth2?code=oauth-code",
		}, nil
	}))

	callback, err := session.interactiveHttpsOauth2(context.Background(), "https://oauth.example.edu.cn/login")
	if err != nil {
		t.Fatalf("interactiveHttpsOauth2() error = %v", err)
	}
	if callback != "https://gateway.test/passport/v1/auth/httpsOauth2?code=oauth-code" {
		t.Fatalf("callback = %q", callback)
	}
}

func TestWithGraphCheckCodeUsesAuthenticationHandler(t *testing.T) {
	session := NewSession("gateway.test")
	session.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/passport/v1/public/checkCode":
			return jsonResponse(request, "captcha-image"), nil
		case "/passport/v1/public/authConfig":
			return jsonResponse(request, `{"data":{}}`), nil
		default:
			return nil, fmt.Errorf("unexpected request path %q", request.URL.Path)
		}
	})}
	session.SetAuthHandler(authHandlerFunc(func(_ context.Context, challenge core.AuthChallenge) (core.AuthResponse, error) {
		if challenge.Kind != core.AuthChallengeGraphClick || string(challenge.Image) != "captcha-image" {
			t.Fatalf("challenge = %#v", challenge)
		}
		return core.AuthResponse{ChallengeID: challenge.ID, Value: `{"coordinates":[[1,2]],"width":100,"height":50}`}, nil
	}))

	processCalls := 0
	err := session.withGraphCheckCode(context.Background(), func(code string) (int, error) {
		processCalls++
		if processCalls == 1 {
			if code != "" {
				t.Fatalf("initial graph code = %q", code)
			}
			return 1, nil
		}
		if code == "" {
			t.Fatal("submitted graph code is empty")
		}
		return 0, nil
	}, "")
	if err != nil {
		t.Fatalf("withGraphCheckCode() error = %v", err)
	}
	if processCalls != 2 {
		t.Fatalf("process calls = %d", processCalls)
	}
}

func TestSecondarySMSUsesAuthenticationHandlerResponse(t *testing.T) {
	session := NewSession("gateway.test")
	session.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/passport/v1/auth/sms" {
			return nil, fmt.Errorf("unexpected request path %q", request.URL.Path)
		}
		if err := request.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		if request.Form.Get("code") != "654321" || request.Form.Get("skipSecondaryAuth") != "1" {
			t.Fatalf("SMS form = %#v", request.Form)
		}
		return jsonResponse(request, `{"code":0,"data":{}}`), nil
	})}
	session.SetAuthHandler(authHandlerFunc(func(_ context.Context, challenge core.AuthChallenge) (core.AuthResponse, error) {
		if challenge.Kind != core.AuthChallengeSecondarySMS || !challenge.AllowSkip {
			t.Fatalf("challenge = %#v", challenge)
		}
		return core.AuthResponse{ChallengeID: challenge.ID, Value: "654321", Skip: true}, nil
	}))

	_, err := session.smsCheckCode(context.Background(), authStep{Service: "auth/sms", SMSMode: smsWithoutAuthID})
	if err != nil {
		t.Fatalf("smsCheckCode() error = %v", err)
	}
}
