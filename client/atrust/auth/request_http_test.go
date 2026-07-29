package auth

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestAuthConfigAndAuthCheckGatewayResponses(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body string
		switch request.URL.Path {
		case "/passport/v1/public/authConfig":
			if request.Method != http.MethodGet {
				t.Errorf("authConfig method = %s", request.Method)
			}
			for key, want := range map[string]string{
				"clientType": "SDPClient",
				"platform":   "Linux",
				"lang":       "en-US",
				"mod":        "1",
				"needTicket": "1",
			} {
				if got := request.URL.Query().Get(key); got != want {
					t.Errorf("authConfig query %s = %q, want %q", key, got, want)
				}
			}
			if got := request.Header.Get("User-Agent"); got != UserAgent {
				t.Errorf("authConfig User-Agent = %q", got)
			}
			if got := request.Header.Get("x-sdp-rid"); got != base64.StdEncoding.EncodeToString([]byte("gateway.test")) {
				t.Errorf("authConfig x-sdp-rid = %q", got)
			}
			if request.Header.Get("x-sdp-traceid") == "" {
				t.Error("authConfig x-sdp-traceid is empty")
			}

			body = `{
			"data": {
				"isLogin": 1,
				"security": {"csrfToken": "csrf-from-security"},
				"pubKey": "public-key",
				"pubKeyExp": "010001",
				"antiReplayRand": "anti-replay",
				"authServerInfoList": [{
					"loginDomain": "default",
					"authType": "auth/psw",
					"authName": "Password",
					"loginUrl": "/login"
				}]
			}
		}`
		case "/passport/v1/auth/authCheck":
			if got := request.Header.Get("x-csrf-token"); got != "csrf-from-security" {
				t.Errorf("authCheck x-csrf-token = %q", got)
			}
			body = `{
			"code": 0,
			"message": "success",
			"data": {
				"nextService": "auth/sms",
				"nextServiceList": [
					{"authId": "totp-id", "authType": "auth/totp"},
					{"authId": "sms-id", "authType": "auth/sms"}
				]
			}
		}`
		default:
			return nil, fmt.Errorf("unexpected request path %q", request.URL.Path)
		}
		return jsonResponse(request, body), nil
	})

	session := NewSession("gateway.test")
	session.client = &http.Client{Transport: transport}

	isLogin, authInfo, err := session.authConfig(context.Background(), true, true)
	if err != nil {
		t.Fatalf("authConfig() error = %v", err)
	}
	if isLogin != 1 {
		t.Fatalf("authConfig() isLogin = %d", isLogin)
	}
	wantAuthInfo := []AuthInfo{{LoginDomain: "default", AuthType: "auth/psw", AuthName: "Password", LoginURL: "/login"}}
	if !reflect.DeepEqual(authInfo, wantAuthInfo) {
		t.Fatalf("authConfig() authInfo = %#v, want %#v", authInfo, wantAuthInfo)
	}
	if session.csrfToken != "csrf-from-security" || session.pubKey != "public-key" || session.pubKeyExp != "010001" || session.antiReplayRand != "anti-replay" {
		t.Fatalf("authConfig() session state = csrf:%q pubKey:%q pubKeyExp:%q antiReplay:%q", session.csrfToken, session.pubKey, session.pubKeyExp, session.antiReplayRand)
	}

	step, err := session.authCheck(context.Background())
	if err != nil {
		t.Fatalf("authCheck() error = %v", err)
	}
	wantStep := authStep{Service: "auth/sms", AuthID: "sms-id", SMSMode: smsWithAuthID}
	if step != wantStep {
		t.Fatalf("authCheck() = %#v, want %#v", step, wantStep)
	}
}

func TestPhoneNumberGatewayResponseFallback(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/passport/v1/public/phoneNumber" {
			t.Errorf("phoneNumber path = %q", request.URL.Path)
		}
		if got := request.URL.Query().Get("authId"); got != "secondary-auth-id" {
			t.Errorf("phoneNumber authId = %q", got)
		}
		return jsonResponse(request, `{
			"code": 0,
			"message": "success",
			"data": {
				"phoneNumber": null,
				"maskIdentifierValue": "138****0000"
			}
		}`), nil
	})

	session := NewSession("gateway.test")
	session.client = &http.Client{Transport: transport}

	got, err := session.phoneNumber(context.Background(), "secondary-auth-id")
	if err != nil {
		t.Fatalf("phoneNumber() error = %v", err)
	}
	want := []string{"138****0000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("phoneNumber() = %#v, want %#v", got, want)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func jsonResponse(request *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func TestAuthConfigRejectsMalformedServerURL(t *testing.T) {
	session := NewSession("%")
	if _, _, err := session.authConfig(context.Background(), false, false); err == nil {
		t.Fatal("authConfig() accepted malformed server URL")
	}
}
