package auth

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/log"
)

const (
	UserAgent    = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) aTrustTray/2.4.10.50 Chrome/83.0.4103.94 Electron/9.0.2 Safari/537.36 aTrustTray-Linux-Plat-Ubuntu-x64 SPCClientType"
	maxAttempts  = 5
	maxAuthSteps = 8
)

var sharedParams = url.Values{
	"clientType": {"SDPClient"},
	"platform":   {"Linux"},
	"lang":       {"en-US"},
}

func WithSharedParams(extra url.Values) url.Values {
	combined := make(url.Values, len(sharedParams)+len(extra))
	for k, v := range sharedParams {
		combined[k] = append([]string(nil), v...)
	}

	for k, v := range extra {
		for _, val := range v {
			// notice: not Add()
			combined.Set(k, val)
		}
	}

	return combined
}

type Cookie struct {
	Host   string `json:"host"`
	Scheme string `json:"scheme"`
	Name   string `json:"name"`
	Value  string `json:"value"`
}

type ClientAuthData struct {
	Cookies  []Cookie `json:"cookies"`
	DeviceID string   `json:"device_id"`
}

type Session struct {
	client   *http.Client
	deviceID string

	baseHost string
	baseURL  string

	rid            string
	env            string
	csrfToken      string
	pubKey         string
	pubKeyExp      string
	antiReplayRand string
	ticket         string
	authHandler    core.AuthHandler

	response map[string]json.RawMessage
}

func NewSession(server string, dialContext ...func(context.Context, string, string) (net.Conn, error)) *Session {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	if len(dialContext) > 0 && dialContext[0] != nil {
		tr.DialContext = dialContext[0]
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: tr, Jar: jar, Timeout: 20 * time.Second}

	rid := base64.StdEncoding.EncodeToString([]byte(server))

	return &Session{
		client:   client,
		baseHost: server,
		baseURL:  "https://" + server,
		rid:      rid,
		response: make(map[string]json.RawMessage),
	}
}

func (s *Session) SetAuthHandler(handler core.AuthHandler) {
	s.authHandler = handler
}

func (s *Session) handleChallenge(ctx context.Context, challenge core.AuthChallenge) (core.AuthResponse, error) {
	if err := challenge.Validate(); err != nil {
		return core.AuthResponse{}, err
	}
	if s.authHandler == nil {
		return core.AuthResponse{}, core.WrapError(
			core.ErrorCodeAuthHandlerUnavailable,
			"authentication handler is unavailable",
			false,
			nil,
		)
	}

	response, err := s.authHandler.Handle(ctx, challenge)
	if err != nil {
		return core.AuthResponse{}, err
	}
	if err := response.Validate(challenge); err != nil {
		return core.AuthResponse{}, err
	}
	return response, nil
}

type AuthInfo struct {
	LoginDomain string `json:"loginDomain"`
	AuthType    string `json:"authType"`
	AuthName    string `json:"authName"`
	LoginURL    string `json:"loginUrl"`
}

type LoginOptions struct {
	DeviceID string
	Cookies  []Cookie
}

type LoginResult struct {
	Username string
	SID      string
	Cookies  []Cookie
	Reused   bool
}

type LoginMethod interface {
	AuthType() string
	LoginDomain() string
	login(context.Context, *Session, AuthInfo) error
}

func (s *Session) randSdpId(n ...int) string {
	length := 8
	if len(n) > 0 {
		length = n[0]
	}
	hexes := make([]byte, length)
	for i := 0; i < length; i++ {
		hexes[i] = "0123456789abcdef"[mathrand.Intn(16)]
	}
	return string(hexes)
}

func (s *Session) withGraphCheckCode(ctx context.Context, process func(string) (int, error), _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	graphCheckCodeEnable, err := process("")
	if err != nil {
		return err
	}

	for attempt := 1; graphCheckCodeEnable == 1 && attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if attempt > 1 {
			log.Printf("Captcha attempt %d/%d", attempt, maxAttempts)
		}

		imgData, err := s.checkCode(ctx)
		if err != nil {
			return err
		}

		_, _, err = s.authConfig(ctx, false, true)
		if err != nil {
			return err
		}

		response, err := s.handleChallenge(ctx, core.AuthChallenge{
			ID:     s.randSdpId(16),
			Kind:   core.AuthChallengeGraphClick,
			Prompt: "Enter graph check code JSON: ",
			Image:  imgData,
		})
		if err != nil {
			return fmt.Errorf("get graph check code: %w", err)
		}
		graphCheckCode := response.Value

		log.DebugPrintf("graphCheckCode submitted: %s", graphCheckCode)

		graphCheckCodeEnable, err = process(graphCheckCode)
		if err != nil {
			return err
		}

		if graphCheckCodeEnable == 0 {
			return nil
		}

		log.Printf("Captcha verification failed (attempt %d/%d), retrying with new captcha...", attempt, maxAttempts)
	}

	if graphCheckCodeEnable != 0 {
		return fmt.Errorf("captcha verification failed after %d attempts", maxAttempts)
	}
	return nil
}

func (s *Session) GetAuthInfoList() ([]AuthInfo, error) {
	_, list, err := s.authConfig(context.Background(), false, true)
	return list, err
}

func (s *Session) continueAuth(ctx context.Context, step authStep) error {
	for attempt := 0; attempt < maxAuthSteps; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		log.DebugPrintf("Continue authentication: service=%s smsMode=%d", step.Service, step.SMSMode)

		var err error
		switch step.Service {
		case "":
			return nil
		case "auth/authCheck":
			step, err = s.authCheck(ctx)
		case "auth/sms":
			step, err = s.completeSMS(ctx, step)
		case "auth/customSms":
			step, err = s.completeCustomSMS(ctx)
		default:
			return fmt.Errorf("unsupported next authentication service: %s", step.Service)
		}
		if err != nil {
			return err
		}
	}

	return fmt.Errorf("authentication chain exceeded %d steps", maxAuthSteps)
}

func (s *Session) completeSMS(ctx context.Context, step authStep) (authStep, error) {
	switch step.SMSMode {
	case smsWithAuthID:
		// HITSZ-style gateways refresh the ticket-bearing auth config before
		// querying the phone number and sending the SMS.
		if _, _, err := s.authConfig(ctx, true, true); err != nil {
			return authStep{}, err
		}
	case smsWithoutAuthID:
		// SARI-style gateways refresh auth config after sending the SMS.
	default:
		return authStep{}, fmt.Errorf("unknown SMS authentication mode")
	}

	phoneNumbers, err := s.phoneNumber(ctx, step.AuthID)
	if err != nil {
		log.Printf("Warning: failed to get phone number: %v", err)
	} else if len(phoneNumbers) > 0 {
		log.Printf("Phone number: %s", strings.Join(phoneNumbers, ", "))
	}

	if err := s.authSms(ctx, step); err != nil {
		return authStep{}, err
	}

	if step.SMSMode == smsWithoutAuthID {
		if _, _, err := s.authConfig(ctx, true, true); err != nil {
			return authStep{}, err
		}
	}

	return s.smsCheckCode(ctx, step)
}

func (s *Session) Login(method LoginMethod, opts LoginOptions) (LoginResult, error) {
	return s.LoginContext(context.Background(), method, opts)
}

func (s *Session) LoginContext(ctx context.Context, method LoginMethod, opts LoginOptions) (LoginResult, error) {
	if err := ctx.Err(); err != nil {
		return LoginResult{}, err
	}
	sid := ""
	if len(opts.Cookies) > 0 {
		for _, cookie := range opts.Cookies {
			if cookie.Host == s.baseHost && cookie.Scheme == "https" && cookie.Name == "sid" {
				sid = cookie.Value
			}

			c := &http.Cookie{
				Name:  cookie.Name,
				Value: cookie.Value,
			}
			s.client.Jar.SetCookies(&url.URL{Host: cookie.Host, Scheme: cookie.Scheme}, []*http.Cookie{c})
		}
	}

	s.deviceID = opts.DeviceID
	s.env = base64.StdEncoding.EncodeToString([]byte(`{"deviceId":"` + opts.DeviceID + `"}`))

	isLogin, authInfoList, err := s.authConfig(ctx, false, true)
	if err != nil {
		return LoginResult{}, err
	}
	if isLogin == 1 {
		log.Println("Already logged in")
		username, err := s.onlineInfo(ctx)
		return LoginResult{
			Username: username,
			SID:      sid,
			Cookies:  opts.Cookies,
			Reused:   true,
		}, err
	}

	if method == nil {
		return LoginResult{}, fmt.Errorf("login method is nil, but user is not logged in")
	}
	var foundAuthInfo *AuthInfo
	for _, authInfo := range authInfoList {
		if authInfo.AuthType == method.AuthType() && authInfo.LoginDomain == method.LoginDomain() {
			foundAuthInfo = &authInfo
			break
		}
	}
	if foundAuthInfo == nil {
		log.Printf("Available authentication methods: %+v", authInfoList)
		return LoginResult{}, fmt.Errorf("auth type/login domain combination not found: auth type: %s, login domain: %s", method.AuthType(), method.LoginDomain())
	}

	log.Printf("Starting login with auth type: %s, login domain: %s", method.AuthType(), method.LoginDomain())
	err = method.login(ctx, s, *foundAuthInfo)
	if err != nil {
		return LoginResult{}, err
	}

	err = s.reportEnv(ctx)
	if err != nil {
		return LoginResult{}, err
	}

	err = s.continueAuth(ctx, authStep{Service: "auth/authCheck"})
	if err != nil {
		return LoginResult{}, err
	}

	username, err := s.onlineInfo(ctx)
	if err != nil {
		return LoginResult{}, err
	}

	cookies := make([]Cookie, 0)
	for _, cookie := range s.client.Jar.Cookies(&url.URL{Host: s.baseHost, Scheme: "https"}) {
		if cookie.Name == "sid" {
			sid = cookie.Value
		}

		cookies = append(cookies, Cookie{
			Host:   s.baseHost,
			Scheme: "https",
			Name:   cookie.Name,
			Value:  cookie.Value,
		})
	}

	return LoginResult{
		Username: username,
		SID:      sid,
		Cookies:  cookies,
	}, nil
}
