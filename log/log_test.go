package log

import (
	"bytes"
	"strings"
	"testing"
)

func TestOutputPreservesMessages(t *testing.T) {
	var output bytes.Buffer
	SetOutput(&output)
	Printf("Token: %s", "secret-token")
	if !strings.Contains(output.String(), "secret-token") {
		t.Fatalf("output = %q", output.String())
	}
}
