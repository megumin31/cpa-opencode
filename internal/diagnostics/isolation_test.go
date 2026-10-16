package diagnostics

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestCaptureHTTPDoesNotMutateCallerRedactionStorage(t *testing.T) {
	backing := []string{"private-current-key", "unused-slot-one", "unused-slot-two", "unused-slot-three"}
	secrets := backing[:1]
	resp := &http.Response{StatusCode: 403, Header: http.Header{"X-Request-Id": {"private-request-id"}}, Body: io.NopCloser(strings.NewReader(`{"account":"private-account","message":"private-current-key private-request-id private-account"}`))}
	d := CaptureHTTP(context.Background(), resp, secrets)
	if strings.Contains(d.ClientError().Error(), "private-") {
		t.Fatal("capture leaked execution or sibling secrets")
	}
	if fmt.Sprint(backing) != "[private-current-key unused-slot-one unused-slot-two unused-slot-three]" {
		t.Fatalf("capture mutated caller redaction storage: %v", backing)
	}
}

func TestSSECopiesExecutionContextAndInjectsPerRequestSink(t *testing.T) {
	var first, second bytes.Buffer
	secrets := []string{"private-first-key"}
	header := http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"private-first-id"}}
	ctx := WithSSE(context.Background(), &http.Response{StatusCode: 200, Header: header}, secrets, log.New(&first, "", 0).Printf)
	secrets[0] = "replaced-key"
	header["X-Request-Id"][0] = "replaced-id"
	other := WithSSE(context.Background(), &http.Response{StatusCode: 200}, []string{"private-second-key"}, log.New(&second, "", 0).Printf)
	firstErr := SSEError(ctx, []byte(`{"message":"first private-first-key private-first-id"}`))
	secondErr := SSEError(other, []byte(`{"message":"second private-second-key"}`))
	if strings.Contains(first.String()+firstErr.Error(), "private-first") || strings.Contains(second.String()+secondErr.Error(), "private-second") {
		t.Fatal("copied execution context failed to redact")
	}
	if !strings.Contains(first.String(), "first [redacted]") || strings.Contains(first.String(), "second") || !strings.Contains(second.String(), "second [redacted]") || strings.Contains(second.String(), "first") {
		t.Fatalf("request log sinks crossed: first=%q second=%q", first.String(), second.String())
	}
}

func TestConcurrentSSECapturesDoNotShareSiblingSecrets(t *testing.T) {
	resp := &http.Response{StatusCode: 200, Header: http.Header{"X-One": {"header-one"}, "X-Two": {"header-two"}, "X-Three": {"header-three"}, "X-Four": {"header-four"}, "X-Five": {"header-five"}}}
	ctx := WithSSE(context.Background(), resp, []string{"execution-key"}, nil)
	var workers sync.WaitGroup
	for i := 0; i < 64; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			for n := 0; n < 8; n++ {
				raw := []byte(fmt.Sprintf(`{"account":"private-%d","message":"generation execution-key private-%d"}`, i, i))
				d := captureSSEErrorDiagnostic(ctx, raw)
				if d.summary != "generation [redacted] [redacted]" {
					t.Errorf("capture shared or leaked sibling redaction state: %q", d.summary)
					return
				}
			}
		}(i)
	}
	workers.Wait()
}

func TestLogRejectsUntrustedSourceAndAllowsNilSink(t *testing.T) {
	d := Diagnostic{status: 403, parseCategory: "empty", summary: "[body unavailable]"}
	var output bytes.Buffer
	Log("upstream\nprivate-forged-source", d, log.New(&output, "", 0).Printf)
	if strings.Contains(output.String(), "private-forged-source") || !strings.Contains(output.String(), "source=upstream http_status=403") {
		t.Fatalf("untrusted source escaped log: %q", output.String())
	}
	Log("upstream", d, nil)
	if got := SSEError(context.Background(), []byte(`{"message":"private-opaque-secret"}`)).Error(); strings.Contains(got, "private-opaque-secret") {
		t.Fatalf("context-free error leaked prose: %q", got)
	}
}
