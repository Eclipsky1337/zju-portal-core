package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/log"
)

type HTTPSOauth2Login struct {
	Domain   string
	Code     string
	Callback string
}

func (m HTTPSOauth2Login) AuthType() string {
	return "auth/httpsOauth2"
}

func (m HTTPSOauth2Login) LoginDomain() string {
	return m.Domain
}

func (m HTTPSOauth2Login) login(ctx context.Context, s *Session, authInfo AuthInfo) error {
	return s.loginAuthHttpsOauth2(ctx, authInfo.LoginURL, m.Domain, m.Code, m.Callback)
}

func (s *Session) loginAuthHttpsOauth2(ctx context.Context, loginURL, loginDomain, code, callback string) error {
	if callback == "" && code != "" {
		callback = s.httpsOauth2CallbackFromCode(loginDomain, code)
	}
	if callback == "" {
		var err error
		callback, err = s.interactiveHttpsOauth2(ctx, loginURL)
		if err != nil {
			return err
		}
	}

	if err := s.httpsOauth2(ctx, callback); err != nil {
		return err
	}

	_, _, err := s.authConfig(ctx, true, false)
	return err
}

func (s *Session) httpsOauth2CallbackFromCode(loginDomain, code string) string {
	params := url.Values{
		"sfDomain": {loginDomain},
		"code":     {code},
		"state":    {"null"},
	}
	return s.baseURL + "/passport/v1/auth/httpsOauth2?" + params.Encode()
}

func (s *Session) interactiveHttpsOauth2(ctx context.Context, loginURL string) (string, error) {
	response, err := s.handleChallenge(ctx, core.AuthChallenge{
		ID:     s.randSdpId(16),
		Kind:   core.AuthChallengeOAuthCallback,
		Prompt: "Enter OAuth2 callback URL: ",
		URL:    loginURL,
	})
	if err != nil {
		return "", err
	}
	callback := response.Value

	callbackURL, err := url.Parse(callback)
	if err != nil {
		return "", err
	}
	if err := validateHTTPSOauth2CallbackURL(callbackURL, s.baseHost); err != nil {
		return "", err
	}

	return callback, nil
}

func validateHTTPSOauth2CallbackURL(callbackURL *url.URL, baseHost string) error {
	if callbackURL.Scheme != "https" {
		return fmt.Errorf("invalid callback url: scheme not https")
	}
	if callbackURL.Host != baseHost {
		return fmt.Errorf("invalid callback url: host not match")
	}
	if callbackURL.Path != "/passport/v1/auth/httpsOauth2" {
		return fmt.Errorf("invalid callback url: path not match")
	}
	queries := callbackURL.Query()
	if queries.Get("code") == "" {
		return fmt.Errorf("invalid callback url: code not found")
	}
	return nil
}

func (s *Session) httpsOauth2(ctx context.Context, callback string) error {
	log.Println("Perform GET /passport/v1/auth/httpsOauth2")

	req, _ := http.NewRequestWithContext(ctx, "GET", callback, nil)
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("x-csrf-token", s.csrfToken)
	req.Header.Set("x-sdp-traceid", s.randSdpId())

	prevCheckRedirect := s.client.CheckRedirect
	s.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	defer func() { s.client.CheckRedirect = prevCheckRedirect }()

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	if resp.StatusCode != 302 {
		return fmt.Errorf("invalid status code: %d", resp.StatusCode)
	}

	ticket, err := parsePortalTicketFromRedirect(resp.Header.Get("Location"), s.baseHost)
	if err != nil {
		return err
	}

	body, _ := io.ReadAll(resp.Body)
	log.DebugPrintf("Received httpsOauth2 data: %s", string(body))

	s.ticket = ticket
	return nil
}
