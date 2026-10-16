package diagnostics

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxDiagnosticSummaryBytes = 1024

var (
	diagnosticURL   = regexp.MustCompile(`(?i)(?:https?://|www\.)[^\s<>"']+`)
	diagnosticEmail = regexp.MustCompile(`(?i)[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-z0-9.-]+\.[a-z]{2,}`)
	diagnosticToken = regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_-]+|eyJ[a-z0-9_-]+\.[a-z0-9_-]+\.[a-z0-9_-]+)\b`)
	// Drop the entire remaining value, not just one whitespace-delimited token.
	// This deliberately sacrifices detail if an upstream embeds credentials or
	// personal account fields inside a prose message or stringified JSON.
	diagnosticSensitive = regexp.MustCompile(`(?i)\b(?:authorization|proxy[ _.-]?authorization|bearer|basic|set[ _.-]?cookie|cookie|[a-z_-]*token|api[ _-]?key|secret|password|passwd|credentials?|(?:full[ _-]?|first[ _-]?|last[ _-]?|display[ _-]?)?name|request[ _-]?id|email|e-mail|account(?:[ _-]?id)?|user(?:[ _-]?(?:id|name))?|org(?:anization)?(?:[ _-]?id)?|project[ _-]?id|phone|address)\b\s*["']?\s*(?:[:=]|\bis\b|\bfor\b)\s*.+`)
	diagnosticAuth      = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+\S+`)
	diagnosticPercent   = regexp.MustCompile(`%[0-9a-fA-F]{2}`)
	diagnosticUnicode   = regexp.MustCompile(`\\u[0-9a-fA-F]{4}`)
	diagnosticHTML      = regexp.MustCompile(`(?i)<\s*[!/a-z][^>]*>`)
)

func redactDiagnosticText(value string, secrets []string) string {
	value = canonicalDiagnosticText(value)
	variants := make([]string, 0, len(secrets)*8)
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		variants = append(variants, canonicalDiagnosticText(secret), canonicalDiagnosticText(url.QueryEscape(secret)), secret, base64.StdEncoding.EncodeToString([]byte(secret)), base64.RawStdEncoding.EncodeToString([]byte(secret)), base64.URLEncoding.EncodeToString([]byte(secret)), base64.RawURLEncoding.EncodeToString([]byte(secret)), hex.EncodeToString([]byte(secret)), strings.ToUpper(hex.EncodeToString([]byte(secret))))
		escaped, _ := json.Marshal(secret)
		if len(escaped) > 2 {
			variants = append(variants, string(escaped[1:len(escaped)-1]))
		}
	}
	sort.Slice(variants, func(i, j int) bool { return len(variants[i]) > len(variants[j]) })
	for _, secret := range variants {
		value = strings.ReplaceAll(value, secret, "[redacted]")
	}
	if diagnosticHTML.MatchString(value) {
		return "[HTML omitted]"
	}
	// Normalize before pattern redaction, so newlines cannot split a sensitive
	// assignment and terminal/bidi controls cannot forge additional log records.
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, value)
	value = diagnosticURL.ReplaceAllString(value, "[URL redacted]")
	value = diagnosticEmail.ReplaceAllString(value, "[email redacted]")
	value = diagnosticToken.ReplaceAllString(value, "[credential redacted]")
	value = diagnosticAuth.ReplaceAllString(value, "[authorization redacted]")
	value = diagnosticSensitive.ReplaceAllString(value, "[sensitive details redacted]")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	return value
}

// Only diagnostic containers and message/detail/title strings are visited.
// Metadata, prompts, request payloads, unknown keys and scalar codes are not
// dumped. Sensitive sibling fields are also used to mask reflected values.
func diagnosticSummary(raw []byte, secrets []string, responseError ...bool) string {
	// Sibling fields extend this capture's redaction set. Never mutate the
	// caller's backing array or another concurrent event's context values.
	secrets = append([]string(nil), secrets...)
	var root any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&root) != nil {
		return "[body unavailable: invalid JSON]"
	}
	var sensitiveScalars func(any, int)
	sensitiveScalars = func(v any, depth int) {
		if depth > 16 {
			return
		}
		switch v := v.(type) {
		case string:
			secrets = append(secrets, v)
		case json.Number:
			secrets = append(secrets, v.String())
		case map[string]any:
			for _, child := range v {
				sensitiveScalars(child, depth+1)
			}
		case []any:
			for _, child := range v {
				sensitiveScalars(child, depth+1)
			}
		}
	}
	var collect func(any, int)
	collect = func(v any, depth int) {
		if depth > 8 {
			return
		}
		switch v := v.(type) {
		case map[string]any:
			for k, child := range v {
				if diagnosticSensitiveKey(k) {
					sensitiveScalars(child, 0)
				}
				collect(child, depth+1)
			}
		case []any:
			for _, child := range v {
				collect(child, depth+1)
			}
		}
	}
	collect(root, 0)
	// SSE response.failed stores its diagnostic in response.error. Expose only
	// that known position to the normal message walker after collecting all
	// sensitive siblings. Avoid reserializing/duplicating a bounded raw body.
	if len(responseError) > 0 && responseError[0] {
		if envelope, ok := root.(map[string]any); ok && envelope["error"] == nil {
			if response, ok := envelope["response"].(map[string]any); ok {
				envelope["error"] = response["error"]
			}
		}
	}
	var parts []string
	var walk func(any, int)
	walk = func(v any, depth int) {
		if depth > 5 || len(parts) >= 8 {
			return
		}
		switch v := v.(type) {
		case string:
			if s := redactDiagnosticText(v, secrets); s != "" {
				parts = append(parts, s)
			}
		case map[string]any:
			for _, key := range []string{"error", "errors", "message", "detail", "details", "title", "innererror"} {
				if child, ok := v[key]; ok {
					walk(child, depth+1)
				}
			}
		case []any:
			for i, child := range v {
				if i >= 8 {
					break
				}
				walk(child, depth+1)
			}
		}
	}
	// Top-level JSON scalars/arrays are not known diagnostic envelopes.
	if _, ok := root.(map[string]any); ok {
		walk(root, 0)
	}
	if len(parts) == 0 {
		return "[body omitted: no supported diagnostic message]"
	}
	result := strings.Join(parts, "; ")
	if len(result) > maxDiagnosticSummaryBytes {
		result = result[:maxDiagnosticSummaryBytes-len(" [truncated]")]
		for !utf8.ValidString(result) {
			result = result[:len(result)-1]
		}
		result += " [truncated]"
	}
	return result
}

// Decode valid escapes even when surrounding prose contains a literal percent.
// Never URL-decode the whole string atomically: an unrelated "95% quota" must
// not disable redaction of a percent-encoded credential elsewhere in the text.
func canonicalDiagnosticText(value string) string {
	for i := 0; i < 2; i++ {
		value = html.UnescapeString(value)
		value = diagnosticPercent.ReplaceAllStringFunc(value, func(s string) string { b, _ := strconv.ParseUint(s[1:], 16, 8); return string([]byte{byte(b)}) })
		value = diagnosticUnicode.ReplaceAllStringFunc(value, func(s string) string {
			decoded, err := strconv.Unquote(`"` + s + `"`)
			if err != nil {
				return s
			}
			return decoded
		})
	}
	return value
}

func diagnosticSensitiveKey(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(strings.ToLower(key))
	switch normalized {
	case "key", "name", "firstname", "lastname", "fullname", "displayname", "requestid", "organization", "organisation", "project", "account", "user":
		return true
	}
	return diagnosticSensitive.MatchString(normalized + "=value")
}
