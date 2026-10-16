// wirecheck replays the first captured official OpenCode POST through CPA's
// pinned, unmodified native host and an actual C-shared plugin. All credentials
// are fixed fake values; every allowed destination is this process's loopback
// mock. It copies no official identity headers and performs no live inference.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

const (
	pluginID = "opencode"
	modelID  = "opencode/opencode/space-bunny-free"
	fakeKey  = "fake-official-wire-key"
)

type capturedRequest struct {
	Method    string              `json:"method"`
	Path      string              `json:"path"`
	Headers   map[string][]string `json:"headers"`
	Body      json.RawMessage     `json:"body,omitempty"`
	BodyRaw   string              `json:"body_raw,omitempty"`
	BodyBytes int                 `json:"body_bytes"`
}

type officialRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

type headerDifference struct {
	Name     string   `json:"name"`
	Official []string `json:"official"`
	Plugin   []string `json:"plugin"`
	Category string   `json:"category"`
}

type report struct {
	OriginalBodyEqual     bool               `json:"original_body_equal"`
	BodyNormalizations    []string           `json:"body_normalizations"`
	ReplayBodySHA256      string             `json:"replay_canonical_body_sha256"`
	Protocol              string             `json:"protocol"`
	RequireAlignedHeaders bool               `json:"require_aligned_headers"`
	HeadersAligned        bool               `json:"headers_aligned"`
	HostVersion           string             `json:"host_version"`
	PluginPath            string             `json:"plugin_path"`
	PluginSHA256          string             `json:"plugin_sha256"`
	PluginRevision        string             `json:"plugin_revision"`
	PluginModified        string             `json:"plugin_modified"`
	OfficialPath          string             `json:"official_path"`
	OfficialPOSTIndex     int                `json:"official_post_index"`
	MockURL               string             `json:"mock_url"`
	GuardEnabled          bool               `json:"guard_enabled"`
	OfficialPOST          officialRequest    `json:"official_post"`
	Requests              []capturedRequest  `json:"requests"`
	CanonicalBodyEqual    bool               `json:"canonical_body_equal"`
	OfficialBodySHA256    string             `json:"official_canonical_body_sha256"`
	PluginBodySHA256      string             `json:"plugin_canonical_body_sha256"`
	HeaderDifferences     []headerDifference `json:"post_header_differences"`
	MatchingPOSTHeaders   []string           `json:"matching_post_headers"`
	Response              json.RawMessage    `json:"response,omitempty"`
	ResponseOK            bool               `json:"response_ok"`
	Error                 string             `json:"error,omitempty"`
}

type executorRegistry struct{ executor coreauth.ProviderExecutor }

func (r *executorRegistry) Executor(provider string) (coreauth.ProviderExecutor, bool) {
	return r.executor, r.executor != nil && r.executor.Identifier() == provider
}
func (r *executorRegistry) RegisterExecutor(e coreauth.ProviderExecutor) { r.executor = e }
func (r *executorRegistry) UnregisterExecutor(string)                    { r.executor = nil }

func main() {
	watchdog := time.AfterFunc(30*time.Second, func() {
		fmt.Fprintln(os.Stderr, "FAIL: wirecheck exceeded 30 seconds")
		os.Exit(1)
	})
	defer watchdog.Stop()
	plugin := flag.String("plugin", "", "versioned C-shared plugin file (required)")
	official := flag.String("official", "", "official fake loopback capture JSON array")
	protocol := flag.String("protocol", "chat", "captured protocol: chat, responses, anthropic, gemini")
	post := flag.Int("post", 0, "zero-based POST ordinal to replay")
	output := flag.String("output", "", "output capture and comparison JSON file")
	requireAligned := flag.Bool("require-aligned-headers", false, "require POST Accept: */* and no credential x-org-id header")
	flag.Parse()
	if *plugin == "" || *official == "" || *output == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: wirecheck -plugin /path/plugin.so -official /path/official.json -output /path/result.json [-require-aligned-headers]")
		os.Exit(1)
	}
	if err := runProtocol(*plugin, *official, *output, *requireAligned, *post, *protocol); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func run(pluginPath, officialPath, outputPath string, requireAligned bool) error {
	return runSelected(pluginPath, officialPath, outputPath, requireAligned, 0)
}

func selectPOST(requests []officialRequest, ordinal int) (int, error) {
	if ordinal < 0 {
		return -1, errors.New("POST ordinal must be nonnegative")
	}
	for i, request := range requests {
		if request.Method == http.MethodPost {
			if ordinal == 0 {
				return i, nil
			}
			ordinal--
		}
	}
	return -1, errors.New("official capture has no selected POST")
}

func runSelected(pluginPath, officialPath, outputPath string, requireAligned bool, ordinal int) error {
	return runProtocol(pluginPath, officialPath, outputPath, requireAligned, ordinal, "chat")
}

func runProtocol(pluginPath, officialPath, outputPath string, requireAligned bool, ordinal int, protocol string) (runErr error) {
	spec, err := protocolSpecFor(protocol)
	if err != nil {
		return err
	}
	pluginPath, err = filepath.Abs(pluginPath)
	if err != nil {
		return err
	}
	officialPath, err = filepath.Abs(officialPath)
	if err != nil {
		return err
	}
	outputPath, err = filepath.Abs(outputPath)
	if err != nil {
		return err
	}
	if outputPath == officialPath || outputPath == pluginPath {
		return errors.New("output must not overwrite either input")
	}
	capture, err := os.ReadFile(officialPath)
	if err != nil {
		return err
	}
	var officialRequests []officialRequest
	if err := json.Unmarshal(capture, &officialRequests); err != nil {
		return fmt.Errorf("official capture: %w", err)
	}
	postIndex, err := selectPOST(officialRequests, ordinal)
	if err != nil {
		return err
	}

	official := officialRequests[postIndex]
	canonicalOfficial, err := canonicalJSON(official.Body)
	if err != nil {
		return fmt.Errorf("official POST body: %w", err)
	}
	if official.Path != spec.path {
		return fmt.Errorf("unsupported official POST path %q", official.Path)
	}
	if !reflect.DeepEqual(foldOfficialHeaders(official.Headers)[spec.authHeader], []string{spec.authValue}) {
		return errors.New("official capture must use the fixed fake credential")
	}
	replayBody, normalizations, err := replayPayload(protocol, official.Body)
	if err != nil {
		return err
	}
	canonicalReplay, err := canonicalJSON(replayBody)
	if err != nil {
		return err
	}
	binary, err := os.ReadFile(pluginPath)
	if err != nil {
		return err
	}
	result := report{
		Protocol: protocol, RequireAlignedHeaders: requireAligned, HostVersion: "v8.0.29", PluginPath: pluginPath, PluginSHA256: digest(binary),
		PluginRevision: "unknown", PluginModified: "unknown", OfficialPath: officialPath,
		OfficialPOSTIndex: postIndex, OfficialPOST: official, Requests: []capturedRequest{},
		BodyNormalizations: normalizations, ReplayBodySHA256: digest(canonicalReplay), OfficialBodySHA256: digest(canonicalOfficial), HeaderDifferences: []headerDifference{},
		MatchingPOSTHeaders: []string{},
	}
	if info, err := buildinfo.Read(bytes.NewReader(binary)); err == nil {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				result.PluginRevision = setting.Value
			case "vcs.modified":
				result.PluginModified = setting.Value
			}
		}
	}
	defer func() {
		if runErr != nil {
			result.Error = runErr.Error()
		}
		data, err := json.MarshalIndent(result, "", "  ")
		if err == nil {
			err = os.WriteFile(outputPath, append(data, '\n'), 0600)
		}
		if err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("save capture: %w", err))
		}
	}()

	tmp, err := os.MkdirTemp("", "opencode-wirecheck-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	pluginDir := filepath.Join(tmp, "plugins")
	if err := os.Mkdir(pluginDir, 0700); err != nil {
		return err
	}
	// Isolate native-host cleanup from the actual build directory.
	if err := os.WriteFile(filepath.Join(pluginDir, filepath.Base(pluginPath)), binary, 0700); err != nil {
		return err
	}
	catalog := filepath.Join(tmp, "catalog.json")
	if err := os.WriteFile(catalog, []byte(`{}`), 0600); err != nil {
		return err
	}

	var mu sync.Mutex
	var requests []capturedRequest
	var mockErrors []error
	var mock *httptest.Server
	mock = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		headers := r.Header.Clone()
		headers.Set("Host", r.Host) // net/http represents Host separately.
		mu.Lock()
		requests = append(requests, capturedRequest{Method: r.Method, Path: r.URL.RequestURI(), Headers: headers, Body: body, BodyRaw: string(body), BodyBytes: len(body)})
		mu.Unlock()
		reject := func(problem string) {
			mu.Lock()
			mockErrors = append(mockErrors, errors.New(problem))
			mu.Unlock()
			http.Error(w, problem, http.StatusBadRequest)
		}
		if err != nil {
			reject("mock body read failed")
			return
		}
		// Also a fail-closed outbound proxy: never forward or CONNECT.
		if r.Host != strings.TrimPrefix(mock.URL, "http://") || r.Method == http.MethodConnect {
			reject("non-mock destination prohibited")
			return
		}
		authHeader, authValue := spec.authHeader, spec.authValue
		if r.Method == http.MethodGet {
			authHeader, authValue = "Authorization", "Bearer "+fakeKey
		}
		if r.Header.Get(authHeader) != authValue {
			reject("wrong fake service key")
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/console/api/v2/config":
			if r.Header.Get("x-org-id") != "fake-org" {
				reject("wrong fake Console org")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"providers":{"opencode":{"package":%q,"settings":{"baseURL":%q,"headers":{"x-opencode-org-id":"fake-config-org"}},"models":{"space-bunny-free":{}}}}}`, "aisdk:"+spec.packageName, mock.URL+spec.basePath)
		case r.Method == http.MethodPost && r.URL.RequestURI() == official.Path:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, spec.stream)

			w.(http.Flusher).Flush()
		default:
			reject("unexpected method or path")
		}
	}))
	defer mock.Close()
	result.MockURL = mock.URL
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		result.Requests = append(result.Requests, requests...)
	}()

	// The host runtime cannot dial outside the mock. The C-shared runtime has
	// its own transport, isolated by explicit proxy_url and allowed_destinations.
	oldTransport := http.DefaultTransport
	safeTransport := oldTransport.(*http.Transport).Clone()
	safeTransport.Proxy = nil
	safeTransport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != strings.TrimPrefix(mock.URL, "http://") {
			return nil, errors.New("non-mock dial prohibited")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	http.DefaultTransport = safeTransport
	defer func() { safeTransport.CloseIdleConnections(); http.DefaultTransport = oldTransport }()
	var node yaml.Node
	// The guard is off only when its optional block is omitted entirely.
	pluginYAML := fmt.Sprintf("enabled: true\nallow_loopback_http: true\nrequest_timeout_seconds: 10\ncatalog_file: %q\nproxy_url: %q\nallowed_destinations:\n  - %q\n", catalog, mock.URL, mock.URL)
	if err := yaml.Unmarshal([]byte(pluginYAML), &node); err != nil {
		return err
	}
	enabled := true
	host := pluginhost.New()
	defer host.ShutdownAll()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	host.ApplyConfig(ctx, &config.Config{AuthDir: tmp, Plugins: config.PluginsConfig{
		Enabled: true, Dir: pluginDir,
		Configs: map[string]config.PluginInstanceConfig{pluginID: {Enabled: &enabled, Raw: *node.Content[0]}},
	}})
	plugins := host.RegisteredPlugins()
	if len(plugins) != 1 || plugins[0].ID != pluginID || !host.HasAuthProvider(pluginID) {
		return fmt.Errorf("native plugin load/registration failed: %+v", plugins)
	}
	authJSON, _ := json.Marshal(map[string]string{"type": pluginID, "key": fakeKey, "server": mock.URL + "/console", "org_id": "fake-org"})
	auth, handled, err := host.ParseAuth(ctx, pluginapi.AuthParseRequest{Provider: pluginID, FileName: "wirecheck-fake.json", RawJSON: authJSON})
	if err != nil || !handled || auth == nil {
		return fmt.Errorf("auth parse: handled=%v error=%v", handled, err)
	}
	models := host.ModelsForAuth(ctx, auth)
	if models.Err != nil || !models.Handled || len(models.Models) != 1 || models.Models[0].ID != modelID {
		return fmt.Errorf("model discovery: handled=%v models=%+v error=%v", models.Handled, models.Models, models.Err)
	}
	registry := &executorRegistry{}
	host.RegisterExecutors(registry, nil)
	executor, ok := registry.Executor(pluginID)
	if !ok {
		return errors.New("native executor was not registered")
	}
	// Only the payload is replayed. Official identity/trace/session headers are
	// comparison data, never input to the native executor or plugin.
	response, err := executor.Execute(ctx, auth,
		coreexecutor.Request{Model: modelID, Payload: replayBody, Format: spec.format},
		coreexecutor.Options{SourceFormat: spec.format, ResponseFormat: spec.format, OriginalRequest: replayBody})
	if err != nil {
		return fmt.Errorf("native execute: %w", err)
	}
	result.Response = response.Payload
	if !json.Valid(response.Payload) {
		return errors.New("native response is not JSON")
	}
	result.ResponseOK = responseOK(protocol, response.Payload)

	mu.Lock()
	observed := append([]capturedRequest(nil), requests...)
	failures := append([]error(nil), mockErrors...)
	mu.Unlock()
	if len(failures) != 0 {
		return errors.Join(failures...)
	}
	if len(observed) != 2 || observed[0].Method != http.MethodGet || observed[1].Method != http.MethodPost {
		return fmt.Errorf("expected exactly one Console GET and one inference POST, got %d requests", len(observed))
	}
	post := observed[1]
	canonicalPlugin, err := canonicalJSON(post.Body)
	if err != nil {
		return fmt.Errorf("plugin POST body: %w", err)
	}
	result.PluginBodySHA256 = digest(canonicalPlugin)
	result.OriginalBodyEqual = bytes.Equal(canonicalOfficial, canonicalPlugin)
	result.CanonicalBodyEqual = bytes.Equal(canonicalReplay, canonicalPlugin)
	result.HeaderDifferences, result.MatchingPOSTHeaders = compareHeaders(official.Headers, post.Headers)
	alignmentErr := checkAlignedHeaders(post.Headers)
	result.HeadersAligned = alignmentErr == nil
	if !result.CanonicalBodyEqual {
		return errors.New("canonical POST body differs from the official capture; inspect output JSON")
	}
	if !result.ResponseOK {
		return errors.New("native response lost OK, stop reason, or 10/1/11 usage")
	}
	if err := checkProtocolHeaders(protocol, official.Headers, post.Headers); err != nil {
		return err
	}
	pluginHeaders := foldHeaders(post.Headers)
	for key, expected := range map[string]string{spec.authHeader: spec.authValue, "content-type": "application/json", "x-opencode-org-id": "fake-config-org", "user-agent": "opencode/latest/2.0.29/cli"} {
		if !reflect.DeepEqual(pluginHeaders[key], []string{expected}) {
			return fmt.Errorf("incorrect plugin %s header", key)
		}
	}
	for _, key := range []string{"x-opencode-session", "x-session-affinity", "x-session-id"} {
		if len(pluginHeaders[key]) != 1 || !strings.HasPrefix(pluginHeaders[key][0], "ses_") {
			return fmt.Errorf("missing plugin-owned %s", key)
		}
	}
	for _, key := range []string{"b3", "traceparent", "x-opencode-client", "x-opencode-project", "x-opencode-session-id"} {
		if len(pluginHeaders[key]) != 0 {
			return fmt.Errorf("unexpected official identity/trace header %s", key)
		}
	}
	if requireAligned && alignmentErr != nil {
		return alignmentErr
	}
	fmt.Printf("PASS: native CPA %s; plugin revision=%s modified=%s sha256=%s\n", result.HostVersion, result.PluginRevision, result.PluginModified, result.PluginSHA256)
	fmt.Printf("PASS: canonical replay POST body parity (see body_normalizations) sha256=%s; response OK; usage 10/1/11; one mock GET and POST; guard off\n", result.PluginBodySHA256)
	for _, difference := range result.HeaderDifferences {
		fmt.Printf("HEADER [%s] %s: official=%q plugin=%q\n", difference.Category, difference.Name, difference.Official, difference.Plugin)
	}
	fmt.Printf("Capture: %s\n", outputPath)
	return nil
}

func digest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func canonicalJSON(data []byte) ([]byte, error) {
	if !json.Valid(data) {
		return nil, errors.New("invalid JSON")
	}
	// Preserve number tokens rather than silently rounding large integers.
	// Normalize object ordering, whitespace, and string escape spellings only.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("expected a JSON object")
	}
	return json.Marshal(object)
}

func foldOfficialHeaders(headers map[string]string) map[string][]string {
	multi := make(map[string][]string, len(headers))
	for key, value := range headers {
		multi[key] = []string{value}
	}
	return foldHeaders(multi)
}

func foldHeaders(headers map[string][]string) map[string][]string {
	folded := make(map[string][]string, len(headers))
	for key, values := range headers {
		name := strings.ToLower(key)
		folded[name] = append(folded[name], values...)
	}
	for key := range folded {
		sort.Strings(folded[key])
	}
	return folded
}

func compareHeaders(official map[string]string, plugin map[string][]string) ([]headerDifference, []string) {
	left, right := foldOfficialHeaders(official), foldHeaders(plugin)
	keys := make(map[string]bool)
	for key := range left {
		keys[key] = true
	}
	for key := range right {
		keys[key] = true
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	differences, matching := []headerDifference{}, []string{}
	for _, key := range ordered {
		if reflect.DeepEqual(left[key], right[key]) {
			matching = append(matching, key)
			continue
		}
		category := "other"
		switch key {
		case "user-agent", "b3", "traceparent", "x-opencode-client", "x-opencode-project", "x-opencode-session", "x-opencode-session-id", "x-session-affinity", "x-session-id":
			category = "identity/session/trace"
		case "accept", "accept-encoding", "connection", "host", "content-length", "transfer-encoding":
			category = "transport/serialization"
		case "x-org-id":
			category = "credential-org"
		}
		differences = append(differences, headerDifference{Name: key, Official: left[key], Plugin: right[key], Category: category})
	}
	return differences, matching
}

// The official mock proves these two functional headers, independently of
// intentionally different client identities, affinity IDs, and HTTP transports.
func checkAlignedHeaders(headers map[string][]string) error {
	folded := foldHeaders(headers)
	if !reflect.DeepEqual(folded["accept"], []string{"*/*"}) {
		return errors.New("aligned headers require exactly Accept: */*")
	}
	if _, present := folded["x-org-id"]; present {
		return errors.New("aligned headers prohibit credential x-org-id on inference POST")
	}
	return nil
}

// Protocol metadata is a closed allowlist, never a URL supplied by a capture.
type protocolSpec struct {
	path, basePath, packageName, authHeader, authValue, stream string
	format                                                     translator.Format
}

func protocolSpecFor(protocol string) (protocolSpec, error) {
	s := protocolSpec{authHeader: "authorization", authValue: "Bearer " + fakeKey}
	switch protocol {
	case "chat":
		s.path, s.basePath, s.packageName, s.format = "/inference/openai/v1/chat/completions", "/inference/openai/v1", "@ai-sdk/openai-compatible", translator.FormatOpenAI
		s.stream = `data: {"id":"fake-wire-response","object":"chat.completion.chunk","model":"space-bunny-free","choices":[{"index":0,"delta":{"role":"assistant","content":"OK"},"finish_reason":null}]}

data: {"id":"fake-wire-response","object":"chat.completion.chunk","model":"space-bunny-free","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}

data: [DONE]

`
	case "responses":
		s.path, s.basePath, s.packageName, s.format = "/inference/openai/v1/responses", "/inference/openai/v1", "@ai-sdk/openai", translator.FormatOpenAIResponse
		s.stream = `event: response.completed
data: {"type":"response.completed","response":{"id":"fake-wire-response","object":"response","status":"completed","model":"space-bunny-free","output":[{"id":"fake-message","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK","annotations":[]}]}],"usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}}

`
	case "anthropic":
		s.path, s.basePath, s.packageName, s.format = "/inference/anthropic/v1/messages", "/inference/anthropic/v1", "@ai-sdk/anthropic", translator.FormatClaude
		s.authHeader, s.authValue = "x-api-key", fakeKey
		s.stream = `event: message_start
data: {"type":"message_start","message":{"id":"fake-message","type":"message","role":"assistant","model":"space-bunny-free","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`
	case "gemini":
		s.path, s.basePath, s.packageName, s.format = "/inference/google/v1beta/models/space-bunny-free:streamGenerateContent?alt=sse", "/inference/google/v1beta", "@ai-sdk/google", translator.FormatGemini
		s.authHeader, s.authValue = "x-goog-api-key", fakeKey
		s.stream = `data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1,"totalTokenCount":11},"modelVersion":"space-bunny-free","responseId":"fake-wire-response"}

`
	default:
		return protocolSpec{}, fmt.Errorf("unsupported protocol %q", protocol)
	}
	return s, nil
}

func responseOK(protocol string, payload []byte) bool {
	var want map[string]any
	switch protocol {
	case "chat":
		want = map[string]any{"choices.#": int64(1), "choices.0.message.content": "OK", "choices.0.finish_reason": "stop", "usage.prompt_tokens": int64(10), "usage.completion_tokens": int64(1), "usage.total_tokens": int64(11)}
	case "responses":
		want = map[string]any{"status": "completed", "output.#": int64(1), "output.0.content.0.text": "OK", "usage.input_tokens": int64(10), "usage.output_tokens": int64(1), "usage.total_tokens": int64(11)}
	case "anthropic":
		want = map[string]any{"stop_reason": "end_turn", "content.#": int64(1), "content.0.text": "OK", "usage.input_tokens": int64(10), "usage.output_tokens": int64(1)}
	case "gemini":
		want = map[string]any{"candidates.#": int64(1), "candidates.0.content.parts.0.text": "OK", "candidates.0.finishReason": "STOP", "usageMetadata.promptTokenCount": int64(10), "usageMetadata.candidatesTokenCount": int64(1), "usageMetadata.totalTokenCount": int64(11)}
	default:
		return false
	}
	for path, expected := range want {
		got := gjson.GetBytes(payload, path)
		if !got.Exists() {
			return false
		}
		switch v := expected.(type) {
		case string:
			if got.Type != gjson.String || got.String() != v {
				return false
			}
		case int64:
			if got.Type != gjson.Number || got.Float() != float64(v) {
				return false
			}
		}
	}
	return true
}

// Only the official orchestrator's session-derived cache identity is replaced.
// Semantic caller options and the original evidence remain untouched.
func replayPayload(protocol string, body []byte) ([]byte, []string, error) {
	normalizations := []string{}
	if protocol != "responses" {
		return body, normalizations, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, nil, err
	}
	var key string
	if json.Unmarshal(object["prompt_cache_key"], &key) == nil && strings.HasPrefix(key, "ses_") {
		object["prompt_cache_key"] = json.RawMessage(`"cpa_wirecheck_synthetic_cache"`)
		normalized, err := json.Marshal(object)
		return normalized, []string{"prompt_cache_key: official ses_ identity replaced with cpa_wirecheck_synthetic_cache"}, err
	}
	return body, normalizations, nil
}

func checkProtocolHeaders(protocol string, official map[string]string, plugin map[string][]string) error {
	spec, err := protocolSpecFor(protocol)
	if err != nil {
		return err
	}
	expected := map[string][]string{spec.authHeader: {spec.authValue}}
	if protocol == "anthropic" {
		expected["anthropic-version"] = []string{"2023-06-01"}
		expected["anthropic-beta"] = []string{"interleaved-thinking-2025-05-14"}
	}
	for _, headers := range []map[string][]string{foldOfficialHeaders(official), foldHeaders(plugin)} {
		for key, want := range expected {
			if !reflect.DeepEqual(headers[key], want) {
				return fmt.Errorf("incorrect %s protocol header %s", protocol, key)
			}
		}
		for _, key := range []string{"authorization", "x-api-key", "x-goog-api-key"} {
			if key != spec.authHeader && len(headers[key]) != 0 {
				return fmt.Errorf("unexpected %s credential header %s", protocol, key)
			}
		}
	}
	return nil
}
