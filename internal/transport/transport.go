package transport

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"github.com/megumin31/cpa-opencode-zen/internal/fault"
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

const MaxResponseBytes = 32 << 20
const UserAgent = "opencode/latest/2.0.29/cli"

type Policy struct {
	AllowedDestinations []string
	AllowLoopbackHTTP   bool
	ProxyURL            string
	WorkerEndpoint      string
	WorkerToken         string
}

func ValidatePolicy(cfg Policy) error {
	if err := validateWorker(cfg); err != nil {
		return err
	}
	if len(cfg.AllowedDestinations) == 0 {
		return fail(400, "allowed_destinations must not be empty")
	}
	for _, target := range cfg.AllowedDestinations {
		if _, err := SafeURL(target, cfg.AllowLoopbackHTTP); err != nil {
			return err
		}
	}
	if cfg.ProxyURL != "" {
		if _, err := ProxyURL(cfg.ProxyURL); err != nil {
			return err
		}
	}
	return nil
}

func SafeURL(raw string, allowLoopback bool) (*url.URL, error) {
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
func AllowedURL(cfg Policy, raw string) error {
	u, err := SafeURL(raw, cfg.AllowLoopbackHTTP)
	if err != nil {
		return err
	}
	for _, allowed := range cfg.AllowedDestinations {
		a, err := SafeURL(allowed, cfg.AllowLoopbackHTTP)
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
func ProxyURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") {
		return nil, fail(400, "invalid proxy URL")
	}
	return u, nil
}

// Pool owns clients for one configuration generation. Closing a pool is
// terminal: a late request cannot repopulate a retired generation.
type Pool struct {
	mu      sync.Mutex
	clients map[string]*http.Client
	closed  bool
	factory func(string) (*http.Client, error)
}

func NewPool(factory func(string) (*http.Client, error)) *Pool {
	if factory == nil {
		factory = NewClient
	}
	return &Pool{clients: map[string]*http.Client{}, factory: factory}
}
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	for _, client := range p.clients {
		client.CloseIdleConnections()
	}
	p.clients = map[string]*http.Client{}
}
func (p *Pool) Client(proxy string) (*http.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, fail(503, "HTTP pool is closed")
	}
	if client := p.clients[proxy]; client != nil {
		return client, nil
	}
	client, err := p.factory(proxy)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fail(500, "HTTP client factory returned nil")
	}
	p.clients[proxy] = client
	return client, nil
}
func NewClient(proxy string) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.ResponseHeaderTimeout = 0
	tr.TLSHandshakeTimeout = 15 * time.Second
	tr.DisableCompression = true
	if proxy != "" {
		u, err := ProxyURL(proxy)
		if err != nil {
			return nil, err
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

type Options struct {
	Total, Header, Chunk time.Duration
	Encoding             string
}
type transportOptionsKey struct{}

func ParseOptions(settings map[string]any) (Options, error) {
	opts := Options{Total: -1, Header: 5 * time.Minute, Chunk: 15 * time.Minute, Encoding: "none"}
	for key, dst := range map[string]*time.Duration{"timeout": &opts.Total, "headerTimeout": &opts.Header, "chunkTimeout": &opts.Chunk} {
		value, exists := settings[key]
		if !exists || value == nil {
			continue
		}
		if flag, ok := value.(bool); ok && !flag {
			*dst = -1
			continue
		}
		n, ok := number(value)
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
		opts.Encoding = encoding
	}
	if settings["transport"] != nil {
		return opts, fail(400, "unsupported native provider setting: transport")
	}
	return opts, nil
}

func WithOptions(ctx context.Context, settings map[string]any) (context.Context, context.CancelFunc, error) {
	opts, err := ParseOptions(settings)
	if err != nil {
		return nil, nil, err
	}
	var cancel context.CancelFunc
	if opts.Total >= 0 {
		ctx, cancel = context.WithTimeout(ctx, opts.Total)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	return context.WithValue(ctx, transportOptionsKey{}, opts), cancel, nil
}

func SupportedResponseEncoding(value string) bool {
	switch value {
	case "identity", "gzip", "deflate", "br", "zstd":
		return true
	}
	return false
}

func EncodeRequest(body []byte, encoding string) ([]byte, error) {
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

func (p *Pool) Do(ctx context.Context, cfg Policy, credentialProxy, hostProxy, method, target string, headers http.Header, body []byte, authenticated bool) (*http.Response, error) {
	if authenticated {
		if err := AllowedURL(cfg, target); err != nil {
			return nil, err
		}
	}
	// Validate the original destination before mapping; never fall back to direct.
	wireTarget, err := workerTarget(cfg, target)
	if err != nil {
		return nil, err
	}
	opts, ok := ctx.Value(transportOptionsKey{}).(Options)
	if !ok {
		opts, _ = ParseOptions(nil)
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
		if !SupportedResponseEncoding(strings.ToLower(strings.TrimSpace(coding))) {
			return nil, fail(400, "unsupported configured Accept-Encoding")
		}
	}
	if headers.Get("Accept-Encoding") == "" {
		headers.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	}
	if body != nil && opts.Encoding != "none" {
		var err error
		body, err = EncodeRequest(body, opts.Encoding)
		if err != nil {
			return nil, err
		}
		headers.Set("Content-Encoding", opts.Encoding)
	}
	// Caller-supplied gateway credentials must never escape on direct requests.
	headers.Del("X-CPA-Gateway-Token")
	if cfg.WorkerEndpoint != "" {
		headers.Del("Cookie")
		headers.Del("Proxy-Authorization")
		headers.Set("X-CPA-Gateway-Token", cfg.WorkerToken)
	}
	proxy := hostProxy
	if cfg.ProxyURL != "" {
		proxy = cfg.ProxyURL
	}
	if credentialProxy != "" {
		proxy = credentialProxy
	}
	client, err := p.Client(proxy)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithCancelCause(ctx)
	req, err := http.NewRequestWithContext(requestCtx, method, wireTarget, bytes.NewReader(body))
	if err != nil {
		cancel(nil)
		return nil, fail(400, "invalid upstream request")
	}
	req.Header = headers
	req.Header.Set("User-Agent", UserAgent)
	var timer *time.Timer
	if opts.Header >= 0 {
		timer = time.AfterFunc(opts.Header, func() { cancel(fail(504, "upstream response header deadline exceeded")) })
	}
	resp, err := client.Do(req)
	if timer != nil {
		timer.Stop()
	}
	if err != nil {
		cause := context.Cause(requestCtx)
		cancel(nil)
		return nil, Failure(cause)
	}
	raw := &timedBody{ReadCloser: resp.Body, ctx: requestCtx, cancel: cancel, idle: opts.Chunk}
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
		if encoding = strings.TrimSpace(encoding); encoding != "" && !SupportedResponseEncoding(encoding) {
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

func Failure(cause error) error {
	var status *fault.Error
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
		return 0, Failure(err)
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
		return n, Failure(cause)
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

func ReadBounded(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fail(502, "failed reading upstream response")
	}
	if int64(len(b)) > limit {
		return nil, fail(502, "upstream response exceeds size limit")
	}
	return b, nil
}
func UpstreamError(status int) error {
	if status < 400 || status > 599 {
		status = 502
	}
	return &fault.Error{Status: status, Message: "OpenCode upstream request failed (response body unavailable)"}
}

func fail(status int, message string) error { return fault.New(status, message) }
func number(value any) (float64, bool) {
	var n float64
	switch v := value.(type) {
	case float64:
		n = v
	case float32:
		n = float64(v)
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	case json.Number:
		var err error
		n, err = v.Float64()
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return n, !math.IsNaN(n) && !math.IsInf(n, 0)
}
