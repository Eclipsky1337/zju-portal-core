package auth

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestAuthStepFromData(t *testing.T) {
	tests := []struct {
		name string
		data authStepData
		want authStep
	}{
		{
			name: "selects matching service",
			data: authStepData{
				NextService: "auth/sms",
				NextServiceList: []authServiceInfo{
					{AuthID: "totp-id", AuthType: "auth/totp"},
					{AuthID: "sms-id", AuthType: "auth/sms"},
				},
			},
			want: authStep{Service: "auth/sms", AuthID: "sms-id", SMSMode: smsWithAuthID},
		},
		{
			name: "falls back to first listed service",
			data: authStepData{
				NextServiceList: []authServiceInfo{{AuthID: "totp-id", AuthType: "auth/totp"}},
			},
			want: authStep{Service: "auth/totp", AuthID: "totp-id"},
		},
		{
			name: "supports legacy sms auth id",
			data: authStepData{
				NextServiceList: []authServiceInfo{{AuthID: "legacy-sms-id"}},
			},
			want: authStep{Service: "auth/sms", AuthID: "legacy-sms-id", SMSMode: smsWithAuthID},
		},
		{
			name: "supports sms without auth id",
			data: authStepData{NextService: "auth/sms"},
			want: authStep{Service: "auth/sms", SMSMode: smsWithoutAuthID},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := authStepFromData(test.data); got != test.want {
				t.Fatalf("authStepFromData() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParsePhoneNumbers(t *testing.T) {
	tests := []struct {
		name    string
		raw     json.RawMessage
		want    []string
		wantErr bool
	}{
		{name: "missing", raw: nil},
		{name: "null", raw: json.RawMessage(`null`)},
		{name: "empty string", raw: json.RawMessage(`""`)},
		{name: "single", raw: json.RawMessage(`"138****0000"`), want: []string{"138****0000"}},
		{name: "list", raw: json.RawMessage(`["138****0000","139****0000"]`), want: []string{"138****0000", "139****0000"}},
		{name: "invalid scalar", raw: json.RawMessage(`123`), wantErr: true},
		{name: "invalid list", raw: json.RawMessage(`[123]`), wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parsePhoneNumbers(test.raw)
			if test.wantErr {
				if err == nil {
					t.Fatal("parsePhoneNumbers() error = nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePhoneNumbers() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parsePhoneNumbers() = %#v, want %#v", got, test.want)
			}
		})
	}
}
