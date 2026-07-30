package core

import (
	"context"
	"fmt"
)

const MaxAuthChallengeImageSize = 4 * 1024 * 1024

type AuthChallengeKind string

const (
	AuthChallengePassword                   AuthChallengeKind = "password"
	AuthChallengeSMS                        AuthChallengeKind = "sms"
	AuthChallengeSecondarySMS               AuthChallengeKind = "secondary_sms"
	AuthChallengeTOTP                       AuthChallengeKind = "totp"
	AuthChallengeCASCallback                AuthChallengeKind = "cas_callback"
	AuthChallengeOAuthCallback              AuthChallengeKind = "oauth_callback"
	AuthChallengeGraphText                  AuthChallengeKind = "graph_text"
	AuthChallengeGraphClick                 AuthChallengeKind = "graph_click"
	AuthChallengeSelectAuthenticationMethod AuthChallengeKind = "select_authentication_method"
)

var validAuthChallengeKinds = map[AuthChallengeKind]struct{}{
	AuthChallengePassword:                   {},
	AuthChallengeSMS:                        {},
	AuthChallengeSecondarySMS:               {},
	AuthChallengeTOTP:                       {},
	AuthChallengeCASCallback:                {},
	AuthChallengeOAuthCallback:              {},
	AuthChallengeGraphText:                  {},
	AuthChallengeGraphClick:                 {},
	AuthChallengeSelectAuthenticationMethod: {},
}

type AuthChoice struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type AuthChallenge struct {
	ID        string            `json:"id"`
	Kind      AuthChallengeKind `json:"kind"`
	Prompt    string            `json:"prompt,omitempty"`
	URL       string            `json:"url,omitempty"`
	Image     []byte            `json:"image,omitempty"`
	AllowSkip bool              `json:"allow_skip,omitempty"`
	Choices   []AuthChoice      `json:"choices,omitempty"`
}

type AuthResponse struct {
	ChallengeID string `json:"challenge_id"`
	Value       string `json:"value,omitempty"`
	Skip        bool   `json:"skip,omitempty"`
	ChoiceID    string `json:"choice_id,omitempty"`
}

type AuthHandler interface {
	Handle(ctx context.Context, challenge AuthChallenge) (AuthResponse, error)
}

func (kind AuthChallengeKind) Valid() bool {
	_, ok := validAuthChallengeKinds[kind]
	return ok
}

func (challenge AuthChallenge) Validate() error {
	if challenge.ID == "" {
		return invalidAuthChallenge("authentication challenge ID is empty")
	}
	if !challenge.Kind.Valid() {
		return invalidAuthChallenge(fmt.Sprintf("authentication challenge kind %q is invalid", challenge.Kind))
	}
	if len(challenge.Image) > MaxAuthChallengeImageSize {
		return invalidAuthChallenge(fmt.Sprintf("authentication challenge image exceeds %d bytes", MaxAuthChallengeImageSize))
	}

	choiceIDs := make(map[string]struct{}, len(challenge.Choices))
	for _, choice := range challenge.Choices {
		if choice.ID == "" {
			return invalidAuthChallenge("authentication choice ID is empty")
		}
		if _, exists := choiceIDs[choice.ID]; exists {
			return invalidAuthChallenge(fmt.Sprintf("authentication choice ID %q is duplicated", choice.ID))
		}
		choiceIDs[choice.ID] = struct{}{}
	}
	if challenge.Kind == AuthChallengeSelectAuthenticationMethod && len(challenge.Choices) == 0 {
		return invalidAuthChallenge("authentication method challenge has no choices")
	}
	return nil
}

func (response AuthResponse) Validate(challenge AuthChallenge) error {
	if err := challenge.Validate(); err != nil {
		return err
	}
	if response.ChallengeID != challenge.ID {
		return invalidAuthResponse(fmt.Sprintf("authentication response challenge ID %q does not match %q", response.ChallengeID, challenge.ID))
	}
	if response.Skip {
		if !challenge.AllowSkip {
			return invalidAuthResponse("authentication challenge cannot be skipped")
		}
	}

	if challenge.Kind == AuthChallengeSelectAuthenticationMethod {
		if response.ChoiceID == "" {
			return invalidAuthResponse("authentication method choice is empty")
		}
		for _, choice := range challenge.Choices {
			if choice.ID == response.ChoiceID {
				return nil
			}
		}
		return invalidAuthResponse(fmt.Sprintf("authentication method choice %q is invalid", response.ChoiceID))
	}

	if response.Value == "" {
		return invalidAuthResponse("authentication response value is empty")
	}
	return nil
}

func invalidAuthChallenge(message string) error {
	return WrapError(ErrorCodeAuthChallengeInvalid, message, false, nil)
}

func invalidAuthResponse(message string) error {
	return WrapError(ErrorCodeAuthResponseInvalid, message, false, nil)
}
