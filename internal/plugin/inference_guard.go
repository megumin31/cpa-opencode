package plugin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"strings"
	"unicode"

	"github.com/megumin31/cpa-opencode-zen/internal/transport"
	"gopkg.in/yaml.v3"
)

// inferenceTestGuard is an opt-in restriction for isolated account testing.
// It validates identity, not price: callers must independently confirm current
// free eligibility for the exact model before enabling any live request.
// Omission disables it; a present block must explicitly say enabled: true.
type inferenceTestGuard struct {
	Enabled         bool                 `yaml:"enabled"`
	StrictTextOnly  bool                 `yaml:"strict_text_only" json:"strict_text_only"`
	AllowedRequests []inferenceGuardRule `yaml:"allowed_requests"`
}

type inferenceGuardRule struct {
	Model    string `yaml:"model"`
	Endpoint string `yaml:"endpoint"`
}

// Other plugin options retain their existing schema. The opt-in guard alone is
// strict, so a typo/null cannot silently disable the requested restriction.
func validateInferenceGuardYAML(raw []byte) error {
	var values map[string]any
	if yaml.Unmarshal(raw, &values) != nil {
		return fail(400, "invalid plugin YAML")
	}
	value, present := values["isolated_test_guard"]
	if !present {
		return nil
	}
	if value == nil {
		return fail(400, "isolated_test_guard must be a complete enabled guard object; omit it to disable")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return fail(400, "invalid isolated_test_guard object")
	}
	for name, value := range object {
		switch name {
		case "enabled", "allowed_requests":
		case "strict_text_only":
			if _, ok := value.(bool); !ok {
				return fail(400, "isolated_test_guard strict_text_only must be a boolean")
			}
		default:
			return fail(400, "isolated_test_guard contains invalid or unknown fields")
		}
	}
	guardJSON, err := json.Marshal(value)
	if err != nil {
		return fail(400, "invalid isolated_test_guard object")
	}
	var strict struct {
		Enabled         bool `json:"enabled"`
		StrictTextOnly  bool `json:"strict_text_only"`
		AllowedRequests []struct {
			Model    string `json:"model"`
			Endpoint string `json:"endpoint"`
		} `json:"allowed_requests"`
	}
	d := json.NewDecoder(bytes.NewReader(guardJSON))
	d.DisallowUnknownFields()
	if d.Decode(&strict) != nil {
		return fail(400, "isolated_test_guard contains invalid or unknown fields")
	}
	return nil
}

func validateInferenceGuardConfig(cfg pluginConfig) error {
	g := cfg.TestGuard
	if g == nil {
		return nil
	}
	if !g.Enabled || len(g.AllowedRequests) == 0 || len(g.AllowedRequests) > 128 {
		return fail(400, "isolated_test_guard requires enabled: true and 1-128 explicit model/endpoint pairs; omit the block to disable")
	}
	for _, rule := range g.AllowedRequests {
		if !guardModelValid(rule.Model) {
			return fail(400, "isolated_test_guard has an invalid model ID")
		}
		if transport.AllowedURL(cfg.transportPolicy(), rule.Endpoint) != nil {
			return fail(400, "isolated_test_guard endpoint must also satisfy allowed_destinations")
		}
		if !guardEndpointComplete(rule.Endpoint) {
			return fail(400, "isolated_test_guard requires a complete supported inference endpoint, not a URL prefix")
		}
		if g.StrictTextOnly && !strictTextEndpoint(rule.Endpoint) {
			return fail(400, "isolated_test_guard strict_text_only requires a Chat Completions endpoint")
		}
	}
	return nil
}

func guardEndpointComplete(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	switch {
	case strings.HasSuffix(u.Path, "/chat/completions"), strings.HasSuffix(u.Path, "/responses"):
		return u.RawQuery == ""
	case strings.HasSuffix(u.Path, "/messages"):
		return u.RawQuery == "" || u.RawQuery == "beta=true"
	case strings.Contains(u.Path, "/models/") && strings.HasSuffix(u.Path, ":streamGenerateContent"):
		return u.RawQuery == "alt=sse"
	}
	return false
}

func guardModelValid(model string) bool {
	return model != "" && len(model) <= 1024 && strings.IndexFunc(model, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) == -1
}

func (e *Engine) checkInferenceGuard(cfg pluginConfig, protocol, target string, payload []byte) (err error) {
	if cfg.TestGuard == nil {
		return nil
	}
	allowed := false
	defer func() {
		// Only booleans: no model, endpoint, body, auth, organization or prompt.
		e.logf("OpenCode isolated test guard: enabled=true allowed=%t", allowed)
	}()
	reject := func() error { return fail(403, "isolated test guard rejected the final upstream model or endpoint") }
	if validateInferenceGuardConfig(cfg) != nil || transport.AllowedURL(cfg.transportPolicy(), target) != nil {
		return reject()
	}
	body, valid := guardPayload(payload)
	if !valid {
		return reject()
	}
	if cfg.TestGuard.StrictTextOnly && !strictTextPayload(protocol, target, payload) {
		return reject()
	}
	for _, rule := range cfg.TestGuard.AllowedRequests {
		// Pairing is intentional: separate model and endpoint sets would admit
		// their unauthorized cross-product. Matching is exact, not a prefix.
		if target != rule.Endpoint {
			continue
		}
		switch protocol {
		case "openai", "openai-response", "claude":
			model, ok := body["model"].(string)
			allowed = ok && model == rule.Model
		case "gemini":
			// Gemini chooses the model in the final URL, not the ordinary body.
			// If a config overlay adds model back, reject conflicting semantics.
			u, parseErr := url.Parse(target)
			allowed = parseErr == nil && u.RawQuery == "alt=sse" && strings.HasSuffix(u.Path, "/models/"+rule.Model+":streamGenerateContent")
			if value, present := body["model"]; present {
				model, ok := value.(string)
				allowed = allowed && ok && model == rule.Model
			}
		default:
			return reject()
		}
		if allowed {
			return nil
		}
	}
	return reject()
}

func guardPayload(payload []byte) (map[string]any, bool) {
	var body map[string]any
	if json.Unmarshal(payload, &body) != nil || body == nil {
		return nil, false
	}
	// Final serialization normally removes duplicate keys. Still reject both
	// duplicates and case aliases instead of trusting upstream parser behavior.
	d := json.NewDecoder(bytes.NewReader(payload))
	if _, err := d.Token(); err != nil {
		return nil, false
	}
	seen := false
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, false
		}
		if strings.EqualFold(key, "model") {
			if seen || key != "model" {
				return nil, false
			}
			seen = true
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
	}
	if _, err := d.Token(); err != nil {
		return nil, false
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, false
	}
	return body, true
}
