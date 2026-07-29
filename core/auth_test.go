package core

import "testing"

func TestAuthChallengeKindsAreValid(t *testing.T) {
	kinds := []AuthChallengeKind{
		AuthChallengePassword,
		AuthChallengeSMS,
		AuthChallengeSecondarySMS,
		AuthChallengeTOTP,
		AuthChallengeCASCallback,
		AuthChallengeOAuthCallback,
		AuthChallengeGraphText,
		AuthChallengeGraphClick,
		AuthChallengeSelectAuthenticationMethod,
	}
	for _, kind := range kinds {
		if !kind.Valid() {
			t.Fatalf("challenge kind %q is invalid", kind)
		}
	}
	if AuthChallengeKind("unknown").Valid() {
		t.Fatal("unknown challenge kind is valid")
	}
}

func TestAuthChallengeValidate(t *testing.T) {
	valid := AuthChallenge{
		ID:   "challenge-1",
		Kind: AuthChallengeSelectAuthenticationMethod,
		Choices: []AuthChoice{
			{ID: "password", Label: "Password"},
			{ID: "sms", Label: "SMS"},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	tests := []AuthChallenge{
		{Kind: AuthChallengePassword},
		{ID: "challenge-1", Kind: "unknown"},
		{ID: "challenge-1", Kind: AuthChallengeSelectAuthenticationMethod},
		{ID: "challenge-1", Kind: AuthChallengePassword, Choices: []AuthChoice{{}}},
		{ID: "challenge-1", Kind: AuthChallengePassword, Choices: []AuthChoice{{ID: "same"}, {ID: "same"}}},
	}
	for _, challenge := range tests {
		err := challenge.Validate()
		if err == nil {
			t.Fatalf("Validate() succeeded for %#v", challenge)
		}
		if code := ErrorCodeOf(err); code != ErrorCodeAuthChallengeInvalid {
			t.Fatalf("Validate() error code = %q", code)
		}
	}
}

func TestAuthChallengeRejectsOversizedImage(t *testing.T) {
	challenge := AuthChallenge{ID: "graph-1", Kind: AuthChallengeGraphClick, Image: make([]byte, MaxAuthChallengeImageSize+1)}
	if err := challenge.Validate(); ErrorCodeOf(err) != ErrorCodeAuthChallengeInvalid {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestAuthResponseValidate(t *testing.T) {
	passwordChallenge := AuthChallenge{ID: "password-1", Kind: AuthChallengePassword}
	if err := (AuthResponse{ChallengeID: "password-1", Value: "secret"}).Validate(passwordChallenge); err != nil {
		t.Fatalf("password response error = %v", err)
	}

	secondarySMS := AuthChallenge{ID: "sms-1", Kind: AuthChallengeSecondarySMS, AllowSkip: true}
	if err := (AuthResponse{ChallengeID: "sms-1", Skip: true}).Validate(secondarySMS); err != nil {
		t.Fatalf("skip response error = %v", err)
	}

	methodChallenge := AuthChallenge{
		ID:      "method-1",
		Kind:    AuthChallengeSelectAuthenticationMethod,
		Choices: []AuthChoice{{ID: "password", Label: "Password"}},
	}
	if err := (AuthResponse{ChallengeID: "method-1", ChoiceID: "password"}).Validate(methodChallenge); err != nil {
		t.Fatalf("choice response error = %v", err)
	}
}

func TestAuthResponseRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		challenge AuthChallenge
		response  AuthResponse
	}{
		{
			challenge: AuthChallenge{ID: "password-1", Kind: AuthChallengePassword},
			response:  AuthResponse{ChallengeID: "other", Value: "secret"},
		},
		{
			challenge: AuthChallenge{ID: "password-1", Kind: AuthChallengePassword},
			response:  AuthResponse{ChallengeID: "password-1"},
		},
		{
			challenge: AuthChallenge{ID: "password-1", Kind: AuthChallengePassword},
			response:  AuthResponse{ChallengeID: "password-1", Skip: true},
		},
		{
			challenge: AuthChallenge{ID: "method-1", Kind: AuthChallengeSelectAuthenticationMethod, Choices: []AuthChoice{{ID: "password"}}},
			response:  AuthResponse{ChallengeID: "method-1", ChoiceID: "sms"},
		},
	}

	for _, test := range tests {
		err := test.response.Validate(test.challenge)
		if err == nil {
			t.Fatalf("Validate() succeeded for %#v", test.response)
		}
		if code := ErrorCodeOf(err); code != ErrorCodeAuthResponseInvalid {
			t.Fatalf("Validate() error code = %q", code)
		}
	}
}
