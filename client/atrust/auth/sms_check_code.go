package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/Eclipsky1337/zju-portal-core/core"
	"github.com/Eclipsky1337/zju-portal-core/log"
)

type SMSLogin struct {
	Phone         string
	Domain        string
	GraphCodeFile string
}

func (m SMSLogin) AuthType() string {
	return "auth/smsCheckCode"
}

func (m SMSLogin) LoginDomain() string {
	return m.Domain
}

func (m SMSLogin) login(ctx context.Context, s *Session, _ AuthInfo) error {
	return s.loginAuthSmsCheckCode(ctx, m.Phone, m.Domain, m.GraphCodeFile)
}

func (s *Session) loginAuthSmsCheckCode(ctx context.Context, phone, loginDomain, graphCodeFile string) error {
	sendSmsProcess := func(graphCheckCode string) (int, error) {
		return s.sendSms(ctx, phone, loginDomain, graphCheckCode)
	}
	err := s.withGraphCheckCode(ctx, sendSmsProcess, graphCodeFile)
	if err != nil {
		return err
	}

	response, err := s.handleChallenge(ctx, core.AuthChallenge{
		ID:     s.randSdpId(16),
		Kind:   core.AuthChallengeSMS,
		Prompt: "Enter SMS verification code: ",
	})
	if err != nil {
		return err
	}
	code := response.Value

	smsCheckCodeProcess := func(graphCheckCode string) (int, error) {
		return s.smsCheckCodeImpl(ctx, code, phone, loginDomain, graphCheckCode)
	}
	return s.withGraphCheckCode(ctx, smsCheckCodeProcess, graphCodeFile)
}

func (s *Session) sendSms(ctx context.Context, phone, loginDomain, graphCheckCode string) (int, error) {
	log.Println("Perform POST /passport/v1/public/sendSms")

	data := map[string]interface{}{
		"phone":          phone + "@" + loginDomain,
		"graphCheckCode": graphCheckCode,
	}

	postBody, _ := json.Marshal(data)

	u := s.baseURL + "/passport/v1/public/sendSms"
	req, err := newRequest(ctx, "POST", u+"?"+WithSharedParams(nil).Encode(), bytes.NewReader(postBody))
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Content-Type", "application/json;charset=utf-8")
	req.Header.Set("x-csrf-token", s.csrfToken)
	req.Header.Set("x-sdp-env", s.env)
	req.Header.Set("x-sdp-traceid", s.randSdpId())

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)
	body, err := readAuthResponse(resp)
	if err != nil {
		return 0, err
	}
	log.DebugPrintf("Received sendSms: %s", string(body))

	var re struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Tips                 string `json:"tips"`
			Interval             string `json:"interval"`
			GraphCheckCodeEnable int    `json:"graphCheckCodeEnable"`
		} `json:"data"`
	}
	err = json.Unmarshal(body, &re)
	if err != nil {
		return 0, err
	}
	log.DebugPrintf("Parsed sendSms: %+v", re)
	if re.Code != 0 || re.Message != "" {
		log.Printf("Code: %d, Message: %s", re.Code, re.Message)
	}

	return re.Data.GraphCheckCodeEnable, nil
}

func (s *Session) smsCheckCodeImpl(ctx context.Context, code, phone, loginDomain, graphCheckCode string) (int, error) {
	log.Println("Perform POST /passport/v1/auth/smsCheckCode")

	data := map[string]interface{}{
		"code":  code,
		"phone": phone + "@" + loginDomain,
	}

	if graphCheckCode != "" {
		data["graphCheckCode"] = graphCheckCode
	}
	postBody, _ := json.Marshal(data)

	u := s.baseURL + "/passport/v1/auth/smsCheckCode"
	req, err := newRequest(ctx, "POST", u+"?"+WithSharedParams(nil).Encode(), bytes.NewReader(postBody))
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Content-Type", "application/json;charset=utf-8")
	req.Header.Set("x-csrf-token", s.csrfToken)
	req.Header.Set("x-sdp-env", s.env)
	req.Header.Set("x-sdp-traceid", s.randSdpId())

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)
	body, err := readAuthResponse(resp)
	if err != nil {
		return 0, err
	}
	log.DebugPrintf("Received smsCheckCode: %s", string(body))

	var re struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Ticket               string `json:"ticket"`
			GraphCheckCodeEnable int    `json:"graphCheckCodeEnable"`
		} `json:"data"`
	}
	err = json.Unmarshal(body, &re)
	if err != nil {
		return 0, err
	}
	if re.Code != 0 || re.Message != "" {
		log.Printf("Code: %d, Message: %s", re.Code, re.Message)
	}
	log.DebugPrintf("Parsed smsCheckCode: %+v", re)

	s.ticket = re.Data.Ticket

	return re.Data.GraphCheckCodeEnable, nil
}
