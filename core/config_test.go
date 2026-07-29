package core

import "testing"

func TestInternetOutboundTypeValidation(t *testing.T) {
	for _, outboundType := range []InternetOutboundType{InternetOutboundDirect, InternetOutboundSOCKS5, InternetOutboundReject} {
		if !outboundType.Valid() {
			t.Fatalf("valid internet outbound type %q was rejected", outboundType)
		}
	}
	if InternetOutboundType("unknown").Valid() {
		t.Fatal("unknown internet outbound type was accepted")
	}
}
