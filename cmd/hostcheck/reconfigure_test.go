package main

import (
	"errors"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestRetiredStreamRequiresCancellationAndClosure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
		wantErr bool
	}{
		{"explicit generation retirement", "plugin reconfigured", false},
		{"worker cancellation wins race", "OpenCode plugin: request canceled", false},
		{"context cancellation", "context canceled", false},
		{"unexpected upstream error", "bad upstream", true},
		{"no terminal notification", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chunks := make(chan coreexecutor.StreamChunk, 2)
			chunks <- coreexecutor.StreamChunk{Payload: []byte("buffered before retirement")}
			if tc.message != "" {
				chunks <- coreexecutor.StreamChunk{Err: errors.New(tc.message)}
			}
			close(chunks)
			err := verifyRetiredStream(&coreexecutor.StreamResult{Chunks: chunks})
			if (err != nil) != tc.wantErr {
				t.Fatalf("verifyRetiredStream() = %v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
