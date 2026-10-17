package transport_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/transport"
)

func workerPolicy() transport.Policy {
	return transport.Policy{AllowedDestinations: []string{"https://opencode.ai/console", "https://opencode.ai/inference"}, WorkerEndpoint: "https://gateway.example", WorkerToken: "fake-entry-token"}
}
func TestWorkerPolicyValidation(t *testing.T) {
	for _, endpoint := range []string{"http://gateway.example", "https://user:pass@gateway.example", "https://gateway.example/prefix", "https://gateway.example?key=x", "https://gateway.example?", "https://gateway.example/#x", "https://127.0.0.1", "https://localhost", "https://gateway.example:8443", "https://gateway.example/%2e"} {
		cfg := workerPolicy()
		cfg.WorkerEndpoint = endpoint
		if transport.ValidatePolicy(cfg) == nil {
			t.Errorf("accepted %q", endpoint)
		}
	}
	for _, token := range []string{"", "a\nb", " a", "a b"} {
		cfg := workerPolicy()
		cfg.WorkerToken = token
		if transport.ValidatePolicy(cfg) == nil {
			t.Errorf("accepted invalid token %q", token)
		}
	}
	cfg := workerPolicy()
	cfg.WorkerEndpoint = ""
	cfg.WorkerToken = ""
	if err := transport.ValidatePolicy(cfg); err != nil {
		t.Fatal(err)
	}
}
func TestWorkerMapsOnlyAfterOriginalPolicy(t *testing.T) {
	for _, tc := range []struct {
		method, target, path string
		auth                 bool
	}{
		{"GET", "https://opencode.ai/console/api/v2/config", "/opencode/console/api/v2/config", true},
		{"GET", "https://models.opencode.ai/api.json", "/models/api.json", false},
		{"POST", "https://opencode.ai/inference/openai/v1/chat/completions", "/opencode/inference/openai/v1/chat/completions", true},
		{"POST", "https://opencode.ai/inference/openai/v1/responses", "/opencode/inference/openai/v1/responses", true},
		{"POST", "https://opencode.ai/inference/go/v1/chat/completions", "/opencode/inference/go/v1/chat/completions", true},
		{"POST", "https://opencode.ai/inference/google/v1/models/fake-model:streamGenerateContent?alt=sse", "/opencode/inference/google/v1/models/fake-model:streamGenerateContent?alt=sse", true},
		{"POST", "https://opencode.ai/inference/anthropic/v1/messages?beta=true", "/opencode/inference/anthropic/v1/messages?beta=true", true},
		{"POST", "https://opencode.ai/inference/google/v1beta/models/fake-model:streamGenerateContent?alt=sse", "/opencode/inference/google/v1beta/models/fake-model:streamGenerateContent?alt=sse", true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			pool := transport.NewPool(func(string) (*http.Client, error) {
				return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.String() != "https://gateway.example"+tc.path || r.Host != "gateway.example" || r.Method != tc.method {
						t.Fatalf("wrong request: %s %s host=%s", r.Method, r.URL, r.Host)
					}
					if r.Header.Get("X-CPA-Gateway-Token") != "fake-entry-token" || r.Header.Get("Authorization") != "Bearer fake-key" || r.Header.Get("Anthropic-Version") != "2023-06-01" || r.Header.Get("X-Opencode-Session-Id") != "fake-session" {
						t.Fatal("required headers lost")
					}
					if r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" {
						t.Fatal("private headers forwarded")
					}
					b, _ := io.ReadAll(r.Body)
					if string(b) != "fake-body" {
						t.Fatalf("body changed %s", b)
					}
					return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"17"}}, Body: io.NopCloser(strings.NewReader("fake-error"))}, nil
				})}, nil
			})
			defer pool.Close()
			headers := http.Header{"Authorization": {"Bearer fake-key"}, "Anthropic-Version": {"2023-06-01"}, "X-Opencode-Session-Id": {"fake-session"}, "Cookie": {"private"}, "Proxy-Authorization": {"private"}}
			resp, err := pool.Do(context.Background(), workerPolicy(), "", "", tc.method, tc.target, headers, []byte("fake-body"), tc.auth)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "17" || string(b) != "fake-error" {
				t.Fatal("error response lost")
			}
			if headers.Get("Cookie") != "private" {
				t.Fatal("mutated caller headers")
			}
		})
	}
}
func TestWorkerRejectsWithoutFallback(t *testing.T) {
	calls := 0
	pool := transport.NewPool(func(string) (*http.Client, error) {
		return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { calls++; return nil, errors.New("synthetic failure") })}, nil
	})
	defer pool.Close()
	for _, tc := range []struct {
		target string
		auth   bool
	}{
		{"https://opencode.ai/not-allowed", true}, {"https://evil.example/inference/v1/messages", true},
		{"https://evil.example/api.json", false}, {"https://opencode.ai/inference/%2e/messages", true},
	} {
		if _, err := pool.Do(context.Background(), workerPolicy(), "", "", "GET", tc.target, nil, nil, tc.auth); err == nil {
			t.Fatal("unsafe URL accepted")
		}
	}
	if calls != 0 {
		t.Fatalf("rejected targets reached network: %d", calls)
	}
	if _, err := pool.Do(context.Background(), workerPolicy(), "", "", "POST", "https://opencode.ai/inference/v1/messages", nil, []byte("fake"), true); err == nil {
		t.Fatal("expected failure")
	}
	if calls != 1 {
		t.Fatalf("retried/fell back: %d", calls)
	}
}
func TestWorkerDirectDisabledAndCompressedStream(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{true: "worker", false: "direct"}[enabled], func(t *testing.T) {
			cfg := workerPolicy()
			if !enabled {
				cfg.WorkerEndpoint = ""
			}
			var zipped bytes.Buffer
			zw := gzip.NewWriter(&zipped)
			zw.Write([]byte("data: fake\n\n"))
			zw.Close()
			pool := transport.NewPool(func(string) (*http.Client, error) {
				return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if !enabled && (r.URL.Host != "opencode.ai" || r.Header.Get("X-CPA-Gateway-Token") != "") {
						t.Fatal("direct mode changed or leaked token")
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Encoding": {"gzip"}, "Content-Type": {"text/event-stream"}}, Body: io.NopCloser(bytes.NewReader(zipped.Bytes()))}, nil
				})}, nil
			})
			defer pool.Close()
			resp, err := pool.Do(context.Background(), cfg, "", "", "POST", "https://opencode.ai/inference/v1/messages", http.Header{"X-Cpa-Gateway-Token": {"caller-secret"}}, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || string(b) != "data: fake\n\n" {
				t.Fatalf("compressed stream: %q %v", b, err)
			}
		})
	}
}
func TestWorkerCancellation(t *testing.T) {
	started := make(chan struct{})
	pool := transport.NewPool(func(string) (*http.Client, error) {
		return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			close(started)
			<-r.Context().Done()
			return nil, r.Context().Err()
		})}, nil
	})
	defer pool.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := pool.Do(ctx, workerPolicy(), "", "", "POST", "https://opencode.ai/inference/v1/messages", nil, []byte("fake"), true)
		done <- err
	}()
	<-started
	cancel()
	requireStatus(t, <-done, 499, "")
}
