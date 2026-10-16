package fault

import (
	"errors"
	"testing"
)

func TestNewPreservesStatusAndSource(t *testing.T) {
	err := New(418, "safe message")
	var status *Error
	if !errors.As(err, &status) || status.StatusCode() != 418 || err.Error() != "OpenCode plugin: safe message" {
		t.Fatalf("unexpected plugin error: %v", err)
	}
}

func TestErrorPreservesUpstreamMessage(t *testing.T) {
	err := &Error{Status: 403, Message: "OpenCode upstream HTTP 403"}
	if err.Error() != "OpenCode upstream HTTP 403" || err.StatusCode() != 403 {
		t.Fatalf("upstream source changed: %v", err)
	}
}
