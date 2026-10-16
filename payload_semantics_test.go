package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func prepareSemanticFixture(t *testing.T, source, target, raw string, mutate func(*route)) (map[string]any, error) {
	t.Helper()
	r := route{ProviderID: "fixture", ModelID: "generic-fixture", Protocol: target, BaseURL: "http://127.0.0.1:18080/v1", Package: protocolPackage(target)}
	if mutate != nil {
		mutate(&r)
	}
	data, _, _, err := prepareRequest(context.Background(), credential{Key: "fake-key"}, r, formatName(source), []byte(raw))
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	return body, nil
}

func TestPreparedPayloadSamplingParity(t *testing.T) {
	testConfig(t, "http://127.0.0.1:18080")
	cases := []struct {
		target, extra, section string
		fields                 map[string]any
	}{
		{"claude", `,"temperature":0.37,"top_p":0.83,"top_k":23,"stop":"END","max_tokens":123`, "", map[string]any{"temperature": 0.37, "top_p": 0.83, "top_k": float64(23), "max_tokens": float64(123)}},
		{"gemini", `,"temperature":0.37,"top_p":0.83,"presence_penalty":0.3,"frequency_penalty":0.4,"seed":42,"stop":["END"],"max_tokens":123`, "generationConfig", map[string]any{"temperature": 0.37, "topP": 0.83, "presencePenalty": 0.3, "frequencyPenalty": 0.4, "seed": float64(42), "maxOutputTokens": float64(123)}},
		{"openai-response", `,"temperature":0.37,"presence_penalty":0.3,"frequency_penalty":0.4,"max_tokens":123`, "", map[string]any{"temperature": 0.37, "presence_penalty": 0.3, "frequency_penalty": 0.4, "max_output_tokens": float64(123)}},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			body, err := prepareSemanticFixture(t, "openai", tc.target, `{"messages":[{"role":"user","content":"hello"}]`+tc.extra+`}`, nil)
			if err != nil {
				t.Fatal(err)
			}
			values := body
			if tc.section != "" {
				values = object(body[tc.section])
			}
			for key, want := range tc.fields {
				if values[key] != want {
					t.Fatalf("%s = %#v, want %#v; body=%#v", key, values[key], want, body)
				}
			}
			if tc.target == "gemini" && len(list(values["stopSequences"])) != 1 {
				t.Fatal("lost stop", body)
			}
			if tc.target == "claude" && len(list(values["stop_sequences"])) != 1 {
				t.Fatal("lost stop", body)
			}
			if body["metadata"] != nil || body["safetySettings"] != nil {
				t.Fatal("unsolicited metadata/safety", body)
			}
		})
	}
}

func TestPreparedPayloadReverseSamplingAndUnsupported(t *testing.T) {
	testConfig(t, "http://127.0.0.1:18080")
	for _, target := range []string{"openai", "openai-response"} {
		body, err := prepareSemanticFixture(t, "gemini", target, `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"temperature":0.2,"presencePenalty":0.3,"frequencyPenalty":0.4,"maxOutputTokens":55}}`, nil)
		if err != nil {
			t.Fatal(target, err)
		}
		if body["presence_penalty"] != 0.3 || body["frequency_penalty"] != 0.4 {
			t.Fatal(target, body)
		}
	}
	for _, tc := range []struct{ target, extra string }{{"claude", `,"presence_penalty":0.1`}, {"claude", `,"seed":0`}, {"openai-response", `,"seed":42`}, {"openai-response", `,"stop":"END"`}, {"openai-response", `,"top_k":12`}} {
		if _, err := prepareSemanticFixture(t, "openai", tc.target, `{"messages":[{"role":"user","content":"hello"}]`+tc.extra+`}`, nil); err == nil {
			t.Fatal("silently dropped", tc)
		}
	}
}

func TestPreparedPayloadNoSilentContentLoss(t *testing.T) {
	testConfig(t, "http://127.0.0.1:18080")
	for _, tc := range []struct{ name, target, raw string }{
		{"remote image", "gemini", `{"messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"https://example.invalid/image.png"}}]}]}`},
		{"audio", "claude", `{"messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"input_audio","input_audio":{"data":"YXVkaW8=","format":"wav"}}]}]}`},
		{"reasoning", "claude", `{"messages":[{"role":"assistant","content":"answer","reasoning_content":"earlier reasoning"},{"role":"user","content":"continue"}]}`},
		{"opaque reasoning", "gemini", `{"messages":[{"role":"assistant","content":"answer","reasoning_details":[{"type":"reasoning.encrypted","data":"opaque"}]},{"role":"user","content":"continue"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := prepareSemanticFixture(t, "openai", tc.target, tc.raw, nil); err == nil {
				t.Fatal("silent loss accepted")
			}
		})
	}
	for _, target := range []string{"claude", "gemini", "openai-response"} {
		body, err := prepareSemanticFixture(t, "openai", target, `{"messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}}]}]}`, nil)
		if err != nil {
			t.Fatal("supported inline image", target, err)
		}
		encoded, _ := json.Marshal(body)
		if !strings.Contains(string(encoded), "aW1hZ2U=") {
			t.Fatal("lost inline media", body)
		}
	}
	body, err := prepareSemanticFixture(t, "openai", "gemini", `{"messages":[{"role":"assistant","content":"answer","reasoning_content":"earlier reasoning"},{"role":"user","content":"continue"}]}`, nil)
	if err != nil {
		t.Fatal("supported reasoning", err)
	}
	encoded, _ := json.Marshal(body)
	if !strings.Contains(string(encoded), "earlier reasoning") {
		t.Fatal(body)
	}
}

func TestPreparedPayloadExplicitSafetyMetadataOverlay(t *testing.T) {
	testConfig(t, "http://127.0.0.1:18080")
	for _, target := range []string{"claude", "gemini"} {
		body, err := prepareSemanticFixture(t, "openai", target, `{"messages":[{"role":"user","content":"hello"}]}`, func(r *route) {
			if target == "claude" {
				r.Settings = map[string]any{"metadata": map[string]any{"user_id": "explicit-user"}}
				r.Body = map[string]any{"metadata": map[string]any{"user_id": "final-user"}}
			} else {
				r.Settings = map[string]any{"safetySettings": []any{map[string]any{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_ONLY_HIGH"}}}
				r.Body = map[string]any{"safetySettings": []any{map[string]any{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_LOW_AND_ABOVE"}}}
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		if target == "claude" && object(body["metadata"])["user_id"] != "final-user" {
			t.Fatal(body)
		}
		if target == "gemini" && object(list(body["safetySettings"])[0])["threshold"] != "BLOCK_LOW_AND_ABOVE" {
			t.Fatal(body)
		}
	}
}

func TestPreparedPayloadCanonicalReasoningAndSignature(t *testing.T) {
	testConfig(t, "http://127.0.0.1:18080")
	body, err := prepareSemanticFixture(t, "openai", "openai", `{"messages":[{"role":"assistant","content":"answer"},{"role":"user","content":"continue"}]}`, func(r *route) { r.Canonical = "deepseek"; r.Settings = map[string]any{"provider": "other"} })
	if err != nil {
		t.Fatal(err)
	}
	if value, exists := object(list(body["messages"])[0])["reasoning_content"]; !exists || value != "" {
		t.Fatal("canonical DeepSeek empty reasoning missing", body)
	}
	raw := `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reasoning","signature":""},{"type":"text","text":"answer"}]},{"role":"user","content":"continue"}]}`
	if _, err := prepareSemanticFixture(t, "claude", "claude", raw, func(r *route) { r.Canonical = "moonshotai"; r.Settings = map[string]any{"provider": "anthropic"} }); err != nil {
		t.Fatal("canonical exemption ignored", err)
	}
	if _, err := prepareSemanticFixture(t, "claude", "claude", raw, func(r *route) { r.Canonical = "anthropic"; r.Settings = map[string]any{"provider": "moonshotai"} }); err == nil {
		t.Fatal("settings incorrectly bypassed signature requirement")
	}
	for _, flag := range []bool{true, false} {
		if _, err := prepareSemanticFixture(t, "openai", "openai", `{"messages":[{"role":"user","content":"hi"}]}`, func(r *route) { r.Compatibility = map[string]any{"requireFinishReason": flag} }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPreparedPayloadNativeOpaqueHistoryAndMedia(t *testing.T) {
	testConfig(t, "http://127.0.0.1:18080")
	cases := []struct{ protocol, raw, marker string }{
		{"claude", `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"private thought","signature":"original-synthetic-signature"},{"type":"text","text":"answer"}]},{"role":"user","content":"continue"}],"metadata":{"user_id":"explicit-user"}}`, "original-synthetic-signature"},
		{"gemini", `{"contents":[{"role":"user","parts":[{"text":"hello"},{"fileData":{"mimeType":"image/png","fileUri":"https://example.invalid/picture.png"}}]}],"safetySettings":[{"category":"HARM_CATEGORY_HARASSMENT","threshold":"BLOCK_ONLY_HIGH"}]}`, "https://example.invalid/picture.png"},
		{"openai-response", `{"input":[{"type":"reasoning","encrypted_content":"opaque-original","summary":[{"type":"summary_text","text":"earlier reasoning"}]},{"role":"user","content":"continue"}]}`, "opaque-original"},
	}
	for _, tc := range cases {
		body, err := prepareSemanticFixture(t, tc.protocol, tc.protocol, tc.raw, nil)
		if err != nil {
			t.Fatal(tc.protocol, err)
		}
		raw, _ := json.Marshal(body)
		if !strings.Contains(string(raw), tc.marker) {
			t.Fatal("native content lost", string(raw))
		}
	}
}

func TestPreparedPayloadUnknownAttachmentFailsClosed(t *testing.T) {
	testConfig(t, "http://127.0.0.1:18080")
	raw := `{"messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"video_url","video_url":{"url":"https://example.invalid/video.mp4"}}]}]}`
	if _, err := prepareSemanticFixture(t, "openai", "gemini", raw, nil); err == nil {
		t.Fatal("unknown mixed attachment silently lost")
	}
}

func TestPreparedPayloadPreservesExplicitUserOnly(t *testing.T) {
	testConfig(t, "http://127.0.0.1:18080")
	body, err := prepareSemanticFixture(t, "openai", "claude", `{"messages":[{"role":"user","content":"hello"}],"user":"explicit-user"}`, nil)
	if err != nil || object(body["metadata"])["user_id"] != "explicit-user" {
		t.Fatal(body, err)
	}
}

func TestPreparedPayloadCrossProtocolSafetyCannotDisappear(t *testing.T) {
	testConfig(t, "http://127.0.0.1:18080")
	for _, target := range []string{"openai", "openai-response", "claude"} {
		for _, safety := range []string{`[]`, `[{"category":"HARM_CATEGORY_HARASSMENT","threshold":"BLOCK_LOW_AND_ABOVE"}]`} {
			raw := `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"safetySettings":` + safety + `}`
			if _, err := prepareSemanticFixture(t, "gemini", target, raw, nil); err == nil {
				t.Fatal("explicit safety settings disappeared", target, safety)
			}
		}
	}
}

func TestPreparedPayloadThinkingAliasesCannotBypassGuard(t *testing.T) {
	testConfig(t, "http://127.0.0.1:18080")
	for _, config := range []string{`{"thinking_budget":2345}`, `{"include_thoughts":false}`, `{"thinkingBudget":2345,"thinking_budget":3456}`, `{"thinkingLevel":"high","thinking_level":"low"}`} {
		raw := `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"thinkingConfig":` + config + `}}`
		if _, err := prepareSemanticFixture(t, "gemini", "openai-response", raw, nil); err == nil {
			t.Fatal("reasoning control lost or approximated", config)
		}
	}
	for _, target := range []string{"openai", "openai-response"} {
		raw := `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"thinkingConfig":{"thinking_level":"high"}}}`
		body, err := prepareSemanticFixture(t, "gemini", target, raw, nil)
		if err != nil {
			t.Fatal("exact reasoning level should survive", target, err)
		}
		effort := body["reasoning_effort"]
		if target == "openai-response" {
			effort = object(body["reasoning"])["effort"]
		}
		if effort != "high" {
			t.Fatal("lost exact reasoning level", body)
		}
	}
}
