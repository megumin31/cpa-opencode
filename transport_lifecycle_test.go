package main

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func compressedFixture(t *testing.T, encoding string, plain []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	var w io.WriteCloser
	switch encoding {
	case "gzip":
		w = gzip.NewWriter(&out)
	case "deflate":
		w = zlib.NewWriter(&out)
	case "br":
		w = brotli.NewWriter(&out)
	case "zstd":
		var err error
		w, err = zstd.NewWriter(&out, zstd.WithEncoderConcurrency(1))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestTransportCompressionActualBody(t *testing.T) {
	plain := []byte("data: {\"text\":\"hello 世界\"}\n\ndata: [DONE]\n\n")
	for _, encoding := range []string{"gzip", "deflate", "br", "zstd", "gzip, br"} {
		for _, explicit := range []bool{false, true} {
			t.Run(encoding+"/explicit="+map[bool]string{true: "yes", false: "no"}[explicit], func(t *testing.T) {
				compressed := append([]byte(nil), plain...)
				for _, coding := range strings.Split(encoding, ", ") {
					compressed = compressedFixture(t, coding, compressed)
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Accept-Encoding") == "" {
						t.Error("no encoding offer")
					}
					if r.Header.Get("User-Agent") != "opencode/latest/2.0.29/cli" {
						t.Error("incorrect client identity")
					}
					w.Header().Set("Content-Encoding", encoding)
					w.Write(compressed)
				}))
				defer srv.Close()
				testConfig(t, srv.URL)
				headers := http.Header{}
				if explicit {
					headers.Set("Accept-Encoding", "gzip, deflate, br, zstd")
				}
				resp, err := outbound(context.Background(), credential{}, "", "GET", srv.URL, headers, nil, true)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				got, err := io.ReadAll(resp.Body)
				if err != nil || !bytes.Equal(got, plain) {
					t.Fatalf("decode got %q, %v", got, err)
				}
				if resp.Header.Get("Content-Encoding") != "" || !resp.Uncompressed {
					t.Fatal("decoded response metadata inconsistent")
				}
			})
		}
	}
}

func TestTransportRequestEncodingActualBody(t *testing.T) {
	plain := []byte(`{"model":"fake","messages":[{"role":"user","content":"hello"}]}`)
	for _, encoding := range []string{"none", "gzip", "zstd"} {
		t.Run(encoding, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body io.Reader = r.Body
				if r.Header.Get("Content-Encoding") != "" {
					switch encoding {
					case "gzip":
						reader, err := gzip.NewReader(r.Body)
						if err != nil {
							t.Error(err)
							return
						}
						defer reader.Close()
						body = reader
					case "zstd":
						reader, err := zstd.NewReader(r.Body)
						if err != nil {
							t.Error(err)
							return
						}
						defer reader.Close()
						body = reader
					default:
						t.Error("unexpected request encoding")
					}
				} else if encoding != "none" {
					t.Error("missing content encoding")
				}
				got, err := io.ReadAll(body)
				if err != nil || !bytes.Equal(got, plain) {
					t.Errorf("body mismatch: %q %v", got, err)
				}
				io.WriteString(w, "ok")
			}))
			defer srv.Close()
			testConfig(t, srv.URL)
			ctx, cancel, err := withTransportOptions(context.Background(), map[string]any{"encoding": encoding})
			if err != nil {
				t.Fatal(err)
			}
			defer cancel()
			resp, err := outbound(ctx, credential{}, "", "POST", srv.URL, http.Header{}, plain, true)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)
		})
	}
}

func TestTransportRejectsFalseEncodingPromises(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	testConfig(t, srv.URL)
	for _, headers := range []http.Header{{"Accept-Encoding": {"snappy"}}, {"Accept-Encoding": {"*"}}, {"Content-Encoding": {"gzip"}}} {
		_, err := outbound(context.Background(), credential{}, "", "POST", srv.URL, headers, []byte(`{}`), true)
		if err == nil {
			t.Fatal("invalid configured encoding accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid encoding sent upstream")
	}
}

func TestTransportPoolReuseAndLifecycle(t *testing.T) {
	var addresses []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addresses = append(addresses, r.RemoteAddr)
		io.WriteString(w, "ok")
	}))
	defer srv.Close()
	testConfig(t, srv.URL)
	fetch := func() {
		t.Helper()
		resp, err := outbound(context.Background(), credential{}, "", "GET", srv.URL, nil, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.ReadAll(resp.Body); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	fetch()
	fetch()
	if addresses[0] != addresses[1] {
		t.Fatal("consecutive requests did not reuse connection")
	}
	client, _ := newHTTPClient("")
	same, _ := newHTTPClient("")
	other, _ := newHTTPClient("http://127.0.0.1:9")
	if client != same || client == other {
		t.Fatal("proxy-keyed client reuse failed")
	}
	testConfig(t, srv.URL)
	fetch()
	if addresses[2] == addresses[1] {
		t.Fatal("reconfigure retained old connection")
	}
	replacement, _ := newHTTPClient("")
	if replacement == client {
		t.Fatal("reconfigure retained old client")
	}
	shutdown()
	httpClients.Lock()
	count := len(httpClients.clients)
	httpClients.Unlock()
	if count != 0 {
		t.Fatal("shutdown retained pool")
	}
}

func TestTransportTimeoutDefaultsAndValidation(t *testing.T) {
	opts, err := parseTransportOptions(nil)
	if err != nil || opts.total != -1 || opts.header != 5*time.Minute || opts.chunk != 15*time.Minute {
		t.Fatalf("defaults: %+v %v", opts, err)
	}
	if err := configure(rawJSON(t, map[string]any{"schema_version": 6})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(shutdown)
	ctx, cancel := executionContext(pluginapi.ExecutorRequest{})
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("default execution has total deadline")
	}
	for _, key := range []string{"timeout", "headerTimeout", "chunkTimeout"} {
		for _, value := range []any{true, -1, "100", math.Inf(1), float64(math.MaxInt64)} {
			if _, err := parseTransportOptions(map[string]any{key: value}); err == nil {
				t.Fatalf("accepted invalid %s=%v", key, value)
			}
		}
		for _, value := range []any{false, 0, 0.5, 100} {
			if _, err := parseTransportOptions(map[string]any{key: value}); err != nil {
				t.Fatalf("rejected %s=%v", key, value)
			}
		}
	}
}

func requireStatus(t *testing.T, err error, status int) {
	t.Helper()
	var e *statusError
	if !errors.As(err, &e) || e.status != status {
		t.Fatalf("want %d, got %v", status, err)
	}
}

func TestTransportHeaderIdleTotalAndCancel(t *testing.T) {
	for _, mode := range []string{"header", "idle", "total", "cancel", "continuing"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "header" {
					<-r.Context().Done()
					return
				}
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				if mode == "continuing" {
					for i := 0; i < 8; i++ {
						io.WriteString(w, "x")
						w.(http.Flusher).Flush()
						time.Sleep(15 * time.Millisecond)
					}
					return
				}
				<-r.Context().Done()
			}))
			defer srv.Close()
			testConfig(t, srv.URL)
			settings := map[string]any{"headerTimeout": 1000, "chunkTimeout": 1000}
			switch mode {
			case "header":
				settings["headerTimeout"] = 40
			case "idle":
				settings["chunkTimeout"] = 40
			case "total":
				settings["timeout"] = 40
			case "continuing":
				settings["chunkTimeout"] = 80
			}
			ctx, cancel, err := withTransportOptions(context.Background(), settings)
			if err != nil {
				t.Fatal(err)
			}
			defer cancel()
			started := time.Now()
			resp, err := outbound(ctx, credential{}, "", "GET", srv.URL, nil, nil, true)
			if mode == "header" {
				requireStatus(t, err, 504)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if mode == "cancel" {
				time.AfterFunc(20*time.Millisecond, cancel)
			}
			body, err := io.ReadAll(resp.Body)
			if mode == "continuing" {
				if err != nil || string(body) != "xxxxxxxx" || time.Since(started) < 80*time.Millisecond {
					t.Fatalf("continuing stream failed %q %v", body, err)
				}
				return
			}
			status := 504
			if mode == "cancel" {
				status = 499
			}
			requireStatus(t, err, status)
			if time.Since(started) > time.Second {
				t.Fatal("timeout did not promptly stop network read")
			}
		})
	}
}

func TestTransportCompressedDiagnosticStillBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(403)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	testConfig(t, srv.URL)
	started := time.Now()
	resp, err := outbound(context.Background(), credential{}, "", "GET", srv.URL, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	d := captureUpstreamErrorDiagnostic(context.Background(), resp)
	if d.status != 403 || time.Since(started) > 2*time.Second {
		t.Fatalf("compressed error diagnostic not bounded: %+v", d)
	}
}
