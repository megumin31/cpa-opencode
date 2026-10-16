package main

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"math"
	"sync"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 32 << 20
const pluginUserAgent = "opencode/latest/2.0.29/cli"

func validateConfig(cfg pluginConfig) error {
	if cfg.RequestTimeoutSeconds < 0 || cfg.RequestTimeoutSeconds > 3600 {
		return fail(400, "request_timeout_seconds must be between 0 (disabled) and 3600")
	}
	if len(cfg.AllowedDestinations) == 0 {
		return fail(400, "allowed_destinations must not be empty")
	}
	for _, target := range cfg.AllowedDestinations {
		if _, err := safeURL(target, cfg.AllowLoopbackHTTP); err != nil {
			return err
		}
	}
	if cfg.ProxyURL != "" {
		if _, err := proxyURL(cfg.ProxyURL); err != nil {
			return err
		}
	}
	return validateInferenceGuardConfig(cfg)
}
func safeURL(raw string, allowLoopback bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, fail(400, "invalid upstream URL")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if !(allowLoopback && u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
			return nil, fail(400, "upstream URLs must use HTTPS")
		}
	}
	if strings.Contains(u.EscapedPath(), "%") || strings.Contains(u.Path, "\\") {
		return nil, fail(400, "encoded upstream paths are not accepted")
	}
	for _, p := range strings.Split(u.Path, "/") {
		if p == ".." || p == "." {
			return nil, fail(400, "relative upstream path segments are not accepted")
		}
	}
	return u, nil
}
func allowedURL(cfg pluginConfig, raw string) error {
	u, err := safeURL(raw, cfg.AllowLoopbackHTTP)
	if err != nil {
		return err
	}
	for _, allowed := range cfg.AllowedDestinations {
		a, err := safeURL(allowed, cfg.AllowLoopbackHTTP)
		if err != nil {
			continue
		}
		if a.RawQuery != "" {
			continue
		}
		prefix := strings.TrimRight(a.Path, "/")
		if strings.EqualFold(u.Host, a.Host) && u.Scheme == a.Scheme && (u.Path == prefix || strings.HasPrefix(u.Path, prefix+"/")) {
			return nil
		}
	}
	return fail(403, "upstream credential destination is not allowlisted; review the discovered endpoint before adding it")
}
func proxyURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") {
		return nil, fail(400, "invalid proxy URL")
	}
	return u, nil
}

// Clients are shared only by explicit proxy policy, never process environment.
var httpClients = struct {
	sync.Mutex
	clients map[string]*http.Client
}{clients: map[string]*http.Client{}}

func closeHTTPClients() {
	httpClients.Lock()
	defer httpClients.Unlock()
	for _, client := range httpClients.clients {
		client.CloseIdleConnections()
	}
	httpClients.clients = map[string]*http.Client{}
}

func newHTTPClient(proxy string) (*http.Client, error) {
	httpClients.Lock()
	defer httpClients.Unlock()
	if client := httpClients.clients[proxy]; client != nil {
		return client, nil
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	// Per-request header deadlines are independent of the pooled transport.
	tr.ResponseHeaderTimeout = 0
	tr.TLSHandshakeTimeout = 15 * time.Second
	tr.DisableCompression = true // Decode explicitly, including configured offers.
	if proxy != "" {
		u, err := proxyURL(proxy)
		if err != nil {
			return nil, err
		}
		tr.Proxy = http.ProxyURL(u)
	}
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	httpClients.clients[proxy] = client
	return client, nil
}

// Retained as an injection seam for offline tests.
var makeHTTPClient = newHTTPClient

type transportOptions struct {
	total, header, chunk time.Duration
	encoding             string
}
type transportOptionsKey struct{}

func parseTransportOptions(settings map[string]any) (transportOptions, error) {
	opts := transportOptions{total: -1, header: 5 * time.Minute, chunk: 15 * time.Minute, encoding: "none"}
	for key, dst := range map[string]*time.Duration{"timeout": &opts.total, "headerTimeout": &opts.header, "chunkTimeout": &opts.chunk} {
		value, exists := settings[key]
		if !exists || value == nil {
			continue
		}
		if flag, ok := value.(bool); ok && !flag {
			*dst = -1
			continue
		}
		n, ok := nativeNumber(value)
		if !ok || n < 0 || n >= float64(math.MaxInt64)/float64(time.Millisecond) {
			return opts, fail(400, "invalid native transport setting: "+key)
		}
		*dst = time.Duration(n * float64(time.Millisecond))
	}
	if value := settings["encoding"]; value != nil {
		encoding, ok := value.(string)
		if !ok || (encoding != "none" && encoding != "gzip" && encoding != "zstd") {
			return opts, fail(400, "unsupported native request encoding")
		}
		opts.encoding = encoding
	}
	if settings["transport"] != nil {
		return opts, fail(400, "unsupported native provider setting: transport")
	}
	return opts, nil
}

func withTransportOptions(ctx context.Context, settings map[string]any) (context.Context, context.CancelFunc, error) {
	opts, err := parseTransportOptions(settings)
	if err != nil {
		return nil, nil, err
	}
	var cancel context.CancelFunc
	if opts.total >= 0 {
		ctx, cancel = context.WithTimeout(ctx, opts.total)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	return context.WithValue(ctx, transportOptionsKey{}, opts), cancel, nil
}

func supportedResponseEncoding(value string) bool {
	switch value {
	case "identity", "gzip", "deflate", "br", "zstd":
		return true
	}
	return false
}

func encodeRequest(body []byte, encoding string) ([]byte, error) {
	if encoding == "none" {
		return body, nil
	}
	var buffer bytes.Buffer
	var writer io.WriteCloser
	if encoding == "gzip" {
		writer = gzip.NewWriter(&buffer)
	} else {
		z, err := zstd.NewWriter(&buffer, zstd.WithEncoderConcurrency(1))
		if err != nil {
			return nil, fail(500, "request compression unavailable")
		}
		writer = z
	}
	if _, err := writer.Write(body); err != nil {
		writer.Close()
		return nil, fail(500, "request compression failed")
	}
	if err := writer.Close(); err != nil {
		return nil, fail(500, "request compression failed")
	}
	return buffer.Bytes(), nil
}

func outbound(ctx context.Context, c credential, hostProxy, method, target string, headers http.Header, body []byte, authenticated bool) (*http.Response, error) {
	cfg := loadedConfig()
	if authenticated {
		if err := allowedURL(cfg, target); err != nil {
			return nil, err
		}
	}
	opts, ok := ctx.Value(transportOptionsKey{}).(transportOptions)
	if !ok {
		opts, _ = parseTransportOptions(nil)
	}
	headers = headers.Clone()
	if headers == nil {
		headers = http.Header{}
	}
	if headers.Get("Content-Encoding") != "" {
		return nil, fail(400, "configured Content-Encoding is unsupported; use the encoding provider setting")
	}
	for _, offer := range strings.Split(strings.Join(headers.Values("Accept-Encoding"), ","), ",") {
		if strings.TrimSpace(offer) == "" {
			continue
		}
		coding, _, _ := strings.Cut(strings.TrimSpace(offer), ";")
		if !supportedResponseEncoding(strings.ToLower(strings.TrimSpace(coding))) {
			return nil, fail(400, "unsupported configured Accept-Encoding")
		}
	}
	if headers.Get("Accept-Encoding") == "" {
		headers.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	}
	if body != nil && opts.encoding != "none" {
		var err error
		body, err = encodeRequest(body, opts.encoding)
		if err != nil {
			return nil, err
		}
		headers.Set("Content-Encoding", opts.encoding)
	}
	proxy := hostProxy
	if cfg.ProxyURL != "" {
		proxy = cfg.ProxyURL
	}
	if c.ProxyURL != "" {
		proxy = c.ProxyURL
	}
	client, err := makeHTTPClient(proxy)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithCancelCause(ctx)
	req, err := http.NewRequestWithContext(requestCtx, method, target, bytes.NewReader(body))
	if err != nil {
		cancel(nil)
		return nil, fail(400, "invalid upstream request")
	}
	req.Header = headers
	req.Header.Set("User-Agent", pluginUserAgent)
	var timer *time.Timer
	if opts.header >= 0 {
		timer = time.AfterFunc(opts.header, func() { cancel(fail(504, "upstream response header deadline exceeded")) })
	}
	resp, err := client.Do(req)
	if timer != nil {
		timer.Stop()
	}
	if err != nil {
		cause := context.Cause(requestCtx)
		cancel(nil)
		return nil, transportFailure(cause)
	}
	raw := &timedBody{ReadCloser: resp.Body, ctx: requestCtx, cancel: cancel, idle: opts.chunk}
	resp.Body = raw
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		resp.Body.Close()
		return nil, fail(502, "upstream redirect refused; update the trusted endpoint explicitly")
	}
	encodings := strings.Split(strings.ToLower(strings.Join(resp.Header.Values("Content-Encoding"), ",")), ",")
	if len(encodings) > 8 {
		resp.Body.Close()
		return nil, fail(502, "too many upstream content encodings")
	}
	for _, encoding := range encodings {
		if encoding = strings.TrimSpace(encoding); encoding != "" && !supportedResponseEncoding(encoding) {
			resp.Body.Close()
			return nil, fail(502, "unsupported upstream content encoding")
		}
	}
	if resp.Header.Get("Content-Encoding") != "" {
		resp.Body = &decodedBody{raw: raw, encodings: encodings}
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
		resp.Uncompressed = true
	}
	return resp, nil
}

func transportFailure(cause error) error {
	var status *statusError
	if errors.As(cause, &status) {
		return status
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return fail(504, "upstream request deadline exceeded")
	}
	if cause != nil {
		return fail(499, "request canceled")
	}
	return fail(502, "upstream connection failed")
}

// Only time waiting for network bytes, not downstream processing/backpressure.
// Wrapping before decompression measures incoming chunks rather than SSE events.
type timedBody struct {
	io.ReadCloser
	ctx    context.Context
	cancel context.CancelCauseFunc
	idle   time.Duration
	once   sync.Once
}

func (b *timedBody) Read(p []byte) (int, error) {
	if err := context.Cause(b.ctx); err != nil {
		return 0, transportFailure(err)
	}
	var timer *time.Timer
	if b.idle >= 0 {
		timer = time.AfterFunc(b.idle, func() { b.cancel(fail(504, "upstream response chunk deadline exceeded")) })
	}
	n, err := b.ReadCloser.Read(p)
	if timer != nil {
		timer.Stop()
	}
	if cause := context.Cause(b.ctx); cause != nil {
		return n, transportFailure(cause)
	}
	return n, err
}
func (b *timedBody) Close() error {
	var err error
	b.once.Do(func() { b.cancel(nil); err = b.ReadCloser.Close() })
	return err
}

// Lazy initialization keeps decoder header reads inside the caller's bounded
// response-reading window (especially the one-second error diagnostic window).
type decodedBody struct {
	raw       io.ReadCloser
	encodings []string
	mu        sync.Mutex
	reader    io.Reader
	closers   []io.Closer
	initErr   error
	closed    bool
}

func (b *decodedBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, io.ErrClosedPipe
	}
	if b.reader == nil && b.initErr == nil {
		b.reader = b.raw
		for i := len(b.encodings) - 1; i >= 0; i-- {
			var reader io.Reader
			var closer io.Closer
			var err error
			switch strings.TrimSpace(b.encodings[i]) {
			case "", "identity":
				continue
			case "gzip":
				r, e := gzip.NewReader(b.reader)
				reader, closer, err = r, r, e
			case "deflate":
				r, e := zlib.NewReader(b.reader)
				reader, closer, err = r, r, e
			case "br":
				reader = brotli.NewReader(b.reader)
			case "zstd":
				r, e := zstd.NewReader(b.reader, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20), zstd.WithDecoderMaxWindow(32<<20))
				err = e
				if e == nil {
					reader = r
					closer = r.IOReadCloser()
				}
			}
			if err != nil {
				b.initErr = err
				break
			}
			b.reader = reader
			if closer != nil {
				b.closers = append(b.closers, closer)
			}
		}
	}
	if b.initErr != nil {
		return 0, b.initErr
	}
	return b.reader.Read(p)
}
func (b *decodedBody) Close() error {
	err := b.raw.Close() // Interrupt Read before acquiring decoder lock.
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		for i := len(b.closers) - 1; i >= 0; i-- {
			b.closers[i].Close()
		}
	}
	return err
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fail(502, "failed reading upstream response")
	}
	if int64(len(b)) > limit {
		return nil, fail(502, "upstream response exceeds size limit")
	}
	return b, nil
}
func upstreamError(status int) error {
	if status < 400 || status > 599 {
		status = 502
	}
	return &statusError{status, "OpenCode upstream request failed (response body unavailable)"}
}
