package transport_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/megumin31/cpa-opencode-zen/internal/fault"
	"github.com/megumin31/cpa-opencode-zen/internal/transport"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type idleTrackingTransport struct {
	closes atomic.Int32
}

func (tr *idleTrackingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
}
func (tr *idleTrackingTransport) CloseIdleConnections() { tr.closes.Add(1) }

func requireStatus(t *testing.T, err error, want int, detail string) {
	t.Helper()
	var status *fault.Error
	if !errors.As(err, &status) || status.StatusCode() != want || (detail != "" && !strings.Contains(err.Error(), detail)) {
		t.Fatalf("error=%v; want status=%d detail=%q", err, want, detail)
	}
}

func loopbackPolicy(server *httptest.Server) transport.Policy {
	return transport.Policy{AllowedDestinations: []string{server.URL}, AllowLoopbackHTTP: true}
}

func TestPoolConcurrentClientConstructsOncePerProxy(t *testing.T) {
	var calls atomic.Int32
	pool := transport.NewPool(func(string) (*http.Client, error) {
		calls.Add(1)
		return &http.Client{Transport: &idleTrackingTransport{}}, nil
	})
	defer pool.Close()
	const workers = 64
	start := make(chan struct{})
	clients := make(chan *http.Client, workers)
	errors := make(chan error, workers)
	var done sync.WaitGroup
	for i := 0; i < workers; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			<-start
			client, err := pool.Client("http://127.0.0.1:18080")
			clients <- client
			errors <- err
		}()
	}
	close(start)
	done.Wait()
	close(clients)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first *http.Client
	for client := range clients {
		if first == nil {
			first = client
		}
		if client != first {
			t.Fatal("concurrent callers received distinct clients for one proxy")
		}
	}
	if first == nil || calls.Load() != 1 {
		t.Fatalf("factory calls=%d; expected one nonnil client", calls.Load())
	}
	other, err := pool.Client("http://127.0.0.1:18081")
	if err != nil || other == first || calls.Load() != 2 {
		t.Fatalf("different proxy did not own a distinct client: error=%v calls=%d", err, calls.Load())
	}
}

func TestPoolsHaveIndependentClientsAndCloseOwnership(t *testing.T) {
	var calls atomic.Int32
	factory := func(string) (*http.Client, error) {
		calls.Add(1)
		return &http.Client{Transport: &idleTrackingTransport{}}, nil
	}
	first, second := transport.NewPool(factory), transport.NewPool(factory)
	defer first.Close()
	defer second.Close()
	a, err := first.Client("")
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Client("")
	if err != nil {
		t.Fatal(err)
	}
	if a == b || calls.Load() != 2 {
		t.Fatal("separate pools shared an HTTP client")
	}
	first.Close()
	if a.Transport.(*idleTrackingTransport).closes.Load() != 1 || b.Transport.(*idleTrackingTransport).closes.Load() != 0 {
		t.Fatal("closing one pool changed another pool's transport")
	}
	stillOpen, err := second.Client("")
	if err != nil || stillOpen != b || calls.Load() != 2 {
		t.Fatalf("second pool cache changed after first closed: %v", err)
	}
	resp, err := second.Do(context.Background(), transport.Policy{AllowedDestinations: []string{"https://upstream.invalid"}}, "", "", http.MethodGet, "https://upstream.invalid/path", nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func TestPoolCloseIsConcurrentIdempotentAndTerminal(t *testing.T) {
	var calls atomic.Int32
	tr := &idleTrackingTransport{}
	pool := transport.NewPool(func(string) (*http.Client, error) {
		calls.Add(1)
		return &http.Client{Transport: tr}, nil
	})
	if _, err := pool.Client(""); err != nil {
		t.Fatal(err)
	}
	var done sync.WaitGroup
	for i := 0; i < 32; i++ {
		done.Add(1)
		go func() { defer done.Done(); pool.Close() }()
	}
	done.Wait()
	for _, proxy := range []string{"", "http://127.0.0.1:18080"} {
		client, err := pool.Client(proxy)
		requireStatus(t, err, http.StatusServiceUnavailable, "pool is closed")
		if client != nil {
			t.Fatal("closed pool returned a client")
		}
	}
	resp, err := pool.Do(context.Background(), transport.Policy{AllowedDestinations: []string{"https://upstream.invalid"}}, "", "", http.MethodGet, "https://upstream.invalid/path", nil, nil, true)
	requireStatus(t, err, http.StatusServiceUnavailable, "pool is closed")
	if resp != nil || calls.Load() != 1 || tr.closes.Load() != 1 {
		t.Fatalf("closed pool was rebuilt or closed repeatedly: response=%v factory=%d closes=%d", resp, calls.Load(), tr.closes.Load())
	}
	// Closing before the first request is terminal too.
	unused := transport.NewPool(func(string) (*http.Client, error) {
		t.Error("closed empty pool called its factory")
		return nil, nil
	})
	unused.Close()
	_, err = unused.Client("")
	requireStatus(t, err, http.StatusServiceUnavailable, "pool is closed")
}

func TestPoolFactoryFailureDoesNotCacheInvalidClient(t *testing.T) {
	for _, nilClient := range []bool{false, true} {
		t.Run(fmt.Sprint("nil=", nilClient), func(t *testing.T) {
			var calls int
			failure := errors.New("synthetic factory failure")
			pool := transport.NewPool(func(string) (*http.Client, error) {
				calls++
				if calls == 1 {
					if nilClient {
						return nil, nil
					}
					return nil, failure
				}
				return &http.Client{Transport: &idleTrackingTransport{}}, nil
			})
			defer pool.Close()
			client, err := pool.Client("")
			if client != nil || err == nil {
				t.Fatal("factory failure yielded a valid client")
			}
			if nilClient {
				requireStatus(t, err, http.StatusInternalServerError, "factory returned nil")
			} else if !errors.Is(err, failure) {
				t.Fatalf("factory failure was replaced: %v", err)
			}
			client, err = pool.Client("")
			if client == nil || err != nil || calls != 2 {
				t.Fatalf("factory failure was cached: client=%v err=%v calls=%d", client, err, calls)
			}
		})
	}
}

func TestNewClientHasExplicitProxyAndNeverInheritsEnvironment(t *testing.T) {
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(key, "http://127.0.0.1:18999")
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	req, err := http.NewRequest(http.MethodGet, "https://upstream.invalid/path", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, proxy := range []string{"", "http://127.0.0.1:18080", "socks5h://127.0.0.1:18081"} {
		t.Run(proxy, func(t *testing.T) {
			client, err := transport.NewClient(proxy)
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			tr, ok := client.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("unexpected client transport: %T", client.Transport)
			}
			if proxy == "" {
				if tr.Proxy != nil {
					t.Fatal("direct transport retained an environment proxy callback")
				}
			} else {
				if tr.Proxy == nil {
					t.Fatal("explicit proxy callback missing")
				}
				u, err := tr.Proxy(req)
				if err != nil || u == nil || u.String() != proxy {
					t.Fatalf("proxy=%v err=%v; want %q", u, err, proxy)
				}
			}
			if client.CheckRedirect == nil || !errors.Is(client.CheckRedirect(req, []*http.Request{req}), http.ErrUseLastResponse) {
				t.Fatal("client permits automatic redirects")
			}
			if !tr.DisableCompression || tr.ResponseHeaderTimeout != 0 {
				t.Fatal("client overrode request-scoped compression or header timeout ownership")
			}
		})
	}
}

func TestDoUsesExplicitProxyPrecedence(t *testing.T) {
	for _, tc := range []struct{ name, credential, configured, host, want string }{
		{"direct", "", "", "", ""},
		{"host", "", "", "http://127.0.0.1:18080", "http://127.0.0.1:18080"},
		{"configuration", "", "http://127.0.0.1:18081", "http://127.0.0.1:18080", "http://127.0.0.1:18081"},
		{"credential", "http://127.0.0.1:18082", "http://127.0.0.1:18081", "http://127.0.0.1:18080", "http://127.0.0.1:18082"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			pool := transport.NewPool(func(proxy string) (*http.Client, error) {
				got = proxy
				return &http.Client{Transport: &idleTrackingTransport{}}, nil
			})
			defer pool.Close()
			cfg := transport.Policy{AllowedDestinations: []string{"https://upstream.invalid"}, ProxyURL: tc.configured}
			resp, err := pool.Do(context.Background(), cfg, tc.credential, tc.host, http.MethodGet, "https://upstream.invalid/path", nil, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if got != tc.want {
				t.Fatalf("selected proxy=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestPolicyRejectsBeforeClientConstructionOrSend(t *testing.T) {
	var factories, sends atomic.Int32
	pool := transport.NewPool(func(string) (*http.Client, error) {
		factories.Add(1)
		return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			sends.Add(1)
			return nil, errors.New("unexpected send")
		})}, nil
	})
	defer pool.Close()
	cfg := transport.Policy{AllowedDestinations: []string{"https://upstream.invalid/allowed"}}
	if err := transport.ValidatePolicy(cfg); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{
		"https://unlisted.invalid/allowed", "https://upstream.invalid/allowed-suffix", "https://upstream.invalid:444/allowed", "http://upstream.invalid/allowed", "https://user:secret@upstream.invalid/allowed", "https://upstream.invalid/allowed/../private", "https://upstream.invalid/allowed/%2e%2e/private", "https://upstream.invalid/allowed#fragment",
	} {
		t.Run(target, func(t *testing.T) {
			resp, err := pool.Do(context.Background(), cfg, "", "", http.MethodPost, target, http.Header{"Authorization": {"Bearer fake-test-key"}}, []byte("synthetic payload"), true)
			if err == nil || resp != nil {
				t.Fatalf("policy allowed rejected target: %v %v", resp, err)
			}
			if factories.Load() != 0 || sends.Load() != 0 {
				t.Fatal("rejected credential destination reached the client factory or transport")
			}
		})
	}
	for _, bad := range []transport.Policy{{}, {AllowedDestinations: []string{"http://upstream.invalid"}}, {AllowedDestinations: cfg.AllowedDestinations, ProxyURL: "file:///tmp/proxy"}} {
		if err := transport.ValidatePolicy(bad); err == nil {
			t.Fatalf("invalid transport policy accepted: %+v", bad)
		}
	}
}

func TestDoRefusesRedirectWithoutContactingDestination(t *testing.T) {
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		_, _ = io.WriteString(w, "must not be contacted")
	}))
	defer destination.Close()
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", destination.URL+"/private")
				w.WriteHeader(status)
			}))
			defer origin.Close()
			pool := transport.NewPool(nil)
			defer pool.Close()
			resp, err := pool.Do(context.Background(), loopbackPolicy(origin), "", "", http.MethodPost, origin.URL, http.Header{"Authorization": {"Bearer fake-test-key"}}, []byte("synthetic payload"), true)
			requireStatus(t, err, http.StatusBadGateway, "redirect refused")
			if resp != nil || redirected.Load() != 0 {
				t.Fatalf("redirect escaped boundary: response=%v destination requests=%d", resp, redirected.Load())
			}
		})
	}
}

func TestDoCallerCancellationInterruptsHeaderWait(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	pool := transport.NewPool(nil)
	defer pool.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		resp, err := pool.Do(ctx, loopbackPolicy(server), "", "", http.MethodGet, server.URL, nil, nil, true)
		if resp != nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("loopback request did not start")
	}
	cancel()
	select {
	case err := <-done:
		requireStatus(t, err, 499, "request canceled")
	case <-time.After(2 * time.Second):
		t.Fatal("request did not honor caller cancellation")
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not reach the HTTP transport")
	}
}

func TestDoHeaderAndTotalTimeoutsAreRequestScoped(t *testing.T) {
	for _, tc := range []struct {
		name, detail string
		settings     map[string]any
	}{
		{"header", "header deadline exceeded", map[string]any{"timeout": false, "headerTimeout": 40, "chunkTimeout": false}},
		{"total", "request deadline exceeded", map[string]any{"timeout": 40, "headerTimeout": false, "chunkTimeout": false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/fast" {
					_, _ = io.WriteString(w, "recovered")
					return
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			pool := transport.NewPool(nil)
			defer pool.Close()
			parent, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			ctx, cancel, err := transport.WithOptions(parent, tc.settings)
			if err != nil {
				t.Fatal(err)
			}
			defer cancel()
			resp, err := pool.Do(ctx, loopbackPolicy(server), "", "", http.MethodGet, server.URL, nil, nil, true)
			requireStatus(t, err, http.StatusGatewayTimeout, tc.detail)
			if resp != nil || parent.Err() != nil {
				t.Fatalf("request relied on the outer test deadline: response=%v parent=%v", resp, parent.Err())
			}
			// A request deadline must not alter the cached client's next request.
			next, finish, err := transport.WithOptions(parent, map[string]any{"timeout": false, "headerTimeout": false, "chunkTimeout": false})
			if err != nil {
				t.Fatal(err)
			}
			defer finish()
			resp, err = pool.Do(next, loopbackPolicy(server), "", "", http.MethodGet, server.URL+"/fast", nil, nil, true)
			if err != nil {
				t.Fatalf("request deadline poisoned cached client: %v", err)
			}
			defer resp.Body.Close()
			raw, err := io.ReadAll(resp.Body)
			if err != nil || string(raw) != "recovered" {
				t.Fatalf("recovery response=%q err=%v", raw, err)
			}
		})
	}
}

func TestDoBodyReadHonorsChunkTimeoutAndCallerCancellation(t *testing.T) {
	for _, mode := range []string{"chunk", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			stopped := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, "prefix")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				close(stopped)
			}))
			defer server.Close()
			pool := transport.NewPool(nil)
			defer pool.Close()
			parent, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			settings := map[string]any{"timeout": false, "headerTimeout": false, "chunkTimeout": false}
			if mode == "chunk" {
				settings["chunkTimeout"] = 40
			}
			ctx, cancel, err := transport.WithOptions(parent, settings)
			if err != nil {
				t.Fatal(err)
			}
			defer cancel()
			resp, err := pool.Do(ctx, loopbackPolicy(server), "", "", http.MethodGet, server.URL, nil, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			prefix := make([]byte, len("prefix"))
			if _, err := io.ReadFull(resp.Body, prefix); err != nil || string(prefix) != "prefix" {
				t.Fatalf("body prefix=%q err=%v", prefix, err)
			}
			done := make(chan error, 1)
			go func() { _, err := resp.Body.Read(make([]byte, 1)); done <- err }()
			if mode == "cancel" {
				cancel()
			}
			select {
			case err := <-done:
				if mode == "chunk" {
					requireStatus(t, err, http.StatusGatewayTimeout, "chunk deadline exceeded")
				} else {
					requireStatus(t, err, 499, "request canceled")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("body read did not stop")
			}
			if parent.Err() != nil {
				t.Fatal("body timeout relied on the outer test deadline")
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("body cancellation did not reach the transport")
			}
		})
	}
}

func TestDoChunkDeadlineDoesNotIncludeDownstreamPause(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "first")
		w.(http.Flusher).Flush()
		select {
		case <-release:
			_, _ = io.WriteString(w, "second")
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	pool := transport.NewPool(nil)
	defer pool.Close()
	parent, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	ctx, cancel, err := transport.WithOptions(parent, map[string]any{"timeout": false, "headerTimeout": false, "chunkTimeout": 40})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	resp, err := pool.Do(ctx, loopbackPolicy(server), "", "", http.MethodGet, server.URL, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first := make([]byte, len("first"))
	if _, err := io.ReadFull(resp.Body, first); err != nil || string(first) != "first" {
		t.Fatalf("first chunk=%q err=%v", first, err)
	}
	// The consumer deliberately does not call Read during this pause.
	<-time.After(100 * time.Millisecond)
	close(release)
	second, err := io.ReadAll(resp.Body)
	if err != nil || string(second) != "second" {
		t.Fatalf("downstream pause incorrectly consumed chunk deadline: second=%q err=%v", second, err)
	}
}
