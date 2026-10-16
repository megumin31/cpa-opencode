package protocol

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// These fixtures use only in-memory, synthetic upstream frames. Assertions are
// on the resulting Responses JSON rather than the aggregator's internal state.
func responsesRegressionFrame(t *testing.T, typ string, fields map[string]any) string {
	t.Helper()
	if fields == nil {
		fields = map[string]any{}
	}
	fields["type"] = typ
	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return "event: " + typ + "\ndata: " + string(body) + "\n\n"
}

func responsesRegressionTerminal(t *testing.T, status string, fields map[string]any) string {
	t.Helper()
	if fields == nil {
		fields = map[string]any{}
	}
	fields["id"] = "resp-regression"
	fields["status"] = status
	return responsesRegressionFrame(t, "response."+status, map[string]any{"response": fields})
}

func responsesRegressionAggregate(t *testing.T, input string) map[string]any {
	t.Helper()
	body, err := Aggregate(context.Background(), byteReader{strings.NewReader(input)}, "openai-response")
	if err != nil {
		t.Fatalf("aggregate failed: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("invalid result: %v: %s", err, body)
	}
	return result
}

func responsesRegressionOutput(t *testing.T, result map[string]any, count int) []any {
	t.Helper()
	output, ok := result["output"].([]any)
	if !ok || len(output) != count {
		t.Fatalf("want %d output items, got %#v", count, result)
	}
	return output
}

func responsesRegressionAssertJSON(t *testing.T, got, want any) {
	t.Helper()
	gotJSON, gotErr := json.Marshal(got)
	wantJSON, wantErr := json.Marshal(want)
	if gotErr != nil || wantErr != nil || string(gotJSON) != string(wantJSON) {
		t.Fatalf("JSON mismatch\n got: %s (%v)\nwant: %s (%v)", gotJSON, gotErr, wantJSON, wantErr)
	}
}

func TestResponsesAggregateSparseTerminalRetainsDeltas(t *testing.T) {
	for _, status := range []string{"completed", "incomplete"} {
		for _, emptyOutput := range []bool{false, true} {
			name := status + "/output-absent"
			if emptyOutput {
				name = status + "/output-empty"
			}
			t.Run(name, func(t *testing.T) {
				input := responsesRegressionFrame(t, "response.created", map[string]any{"response": map[string]any{
					"id": "resp-regression", "object": "response", "model": "synthetic-model", "created_at": 11,
					"metadata": map[string]any{"synthetic": "retained"}, "output": []any{},
				}})
				// No output_item or content_part events: item_id and the event's
				// output_index are sufficient to identify these text deltas.
				for _, delta := range []string{"hello ", "世界"} {
					input += responsesRegressionFrame(t, "response.output_text.delta", map[string]any{
						"item_id": "msg-regression", "output_index": 0, "content_index": 0, "delta": delta,
					})
				}
				terminal := map[string]any{"usage": map[string]any{"input_tokens": 3, "output_tokens": 5, "total_tokens": 8}}
				if status == "incomplete" {
					terminal["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
				}
				if emptyOutput {
					terminal["output"] = []any{}
				}
				result := responsesRegressionAggregate(t, input+responsesRegressionTerminal(t, status, terminal))
				output := responsesRegressionOutput(t, result, 1)
				message := object(output[0])
				if message["id"] != "msg-regression" || message["type"] != "message" {
					t.Fatalf("message identity lost: %#v", message)
				}
				parts := list(message["content"])
				if len(parts) != 1 || object(parts[0])["text"] != "hello 世界" || object(parts[0])["type"] != "output_text" {
					t.Fatalf("text lost/duplicated: %#v", message)
				}
				if result["status"] != status || result["model"] != "synthetic-model" || result["created_at"] != float64(11) {
					t.Fatalf("response metadata lost: %#v", result)
				}
				responsesRegressionAssertJSON(t, result["metadata"], map[string]any{"synthetic": "retained"})
				responsesRegressionAssertJSON(t, result["usage"], terminal["usage"])
				if status == "incomplete" {
					responsesRegressionAssertJSON(t, result["incomplete_details"], terminal["incomplete_details"])
				}
			})
		}
	}
}

func TestResponsesAggregateSnapshotsDoNotDuplicate(t *testing.T) {
	message := map[string]any{"id": "msg-final", "type": "message", "role": "assistant", "status": "completed", "content": []any{
		map[string]any{"type": "output_text", "text": "hello!", "annotations": []any{map[string]any{"type": "synthetic_annotation", "value": "opaque"}}},
	}}
	input := responsesRegressionFrame(t, "response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{
		"id": "msg-final", "type": "message", "role": "assistant", "status": "in_progress", "content": []any{},
	}})
	input += responsesRegressionFrame(t, "response.content_part.added", map[string]any{"item_id": "msg-final", "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
	input += responsesRegressionFrame(t, "response.output_text.delta", map[string]any{"item_id": "msg-final", "output_index": 0, "content_index": 0, "delta": "hello"})
	input += responsesRegressionFrame(t, "response.output_text.done", map[string]any{"item_id": "msg-final", "output_index": 0, "content_index": 0, "text": "hello!"})
	input += responsesRegressionFrame(t, "response.content_part.done", map[string]any{"item_id": "msg-final", "output_index": 0, "content_index": 0, "part": list(message["content"])[0]})
	input += responsesRegressionFrame(t, "response.output_item.done", map[string]any{"output_index": 0, "item": message})
	for _, fullTerminal := range []bool{false, true} {
		name := "sparse-terminal"
		fields := map[string]any{}
		if fullTerminal {
			name = "full-terminal"
			fields["output"] = []any{message}
		}
		t.Run(name, func(t *testing.T) {
			result := responsesRegressionAggregate(t, input+responsesRegressionTerminal(t, "completed", fields))
			output := responsesRegressionOutput(t, result, 1)
			responsesRegressionAssertJSON(t, output[0], message)
		})
	}
}

func TestResponsesAggregateToolIdentityAndSplitArguments(t *testing.T) {
	// Deliberately insert index 1 before index 0. call_id is a tool invocation
	// identity, not the streamed output item's id or its position.
	tool0 := map[string]any{"id": "item-zero", "type": "function_call", "call_id": "call-zero", "name": "first", "arguments": ""}
	tool1 := map[string]any{"id": "item-one", "type": "function_call", "call_id": "call-one", "name": "second", "arguments": ""}
	input := responsesRegressionFrame(t, "response.output_item.added", map[string]any{"output_index": 1, "item": tool1})
	input += responsesRegressionFrame(t, "response.output_item.added", map[string]any{"output_index": 0, "item": tool0})
	for _, step := range []struct {
		id    string
		index int
		delta string
	}{
		{"item-zero", 0, `{"city":`}, {"item-one", 1, `{"n":`},
		{"item-zero", 0, `"Paris"}`}, {"item-one", 1, `2}`},
	} {
		input += responsesRegressionFrame(t, "response.function_call_arguments.delta", map[string]any{"item_id": step.id, "output_index": step.index, "delta": step.delta})
	}
	input += responsesRegressionFrame(t, "response.function_call_arguments.done", map[string]any{"item_id": "item-zero", "output_index": 0, "arguments": `{"city":"Paris"}`})
	input += responsesRegressionFrame(t, "response.function_call_arguments.done", map[string]any{"item_id": "item-one", "output_index": 1, "arguments": `{"n":2}`})
	result := responsesRegressionAggregate(t, input+responsesRegressionTerminal(t, "completed", nil))
	output := responsesRegressionOutput(t, result, 2)
	for index, expected := range []struct{ id, callID, name, args string }{
		{"item-zero", "call-zero", "first", `{"city":"Paris"}`},
		{"item-one", "call-one", "second", `{"n":2}`},
	} {
		got := object(output[index])
		if got["id"] != expected.id || got["call_id"] != expected.callID || got["name"] != expected.name || got["arguments"] != expected.args {
			t.Fatalf("tool %d mis-associated or duplicated: %#v", index, got)
		}
	}
}

func TestResponsesAggregateUsesEventIndexNotEmbeddedItemIndex(t *testing.T) {
	input := responsesRegressionFrame(t, "response.output_item.added", map[string]any{"output_index": 1, "item": map[string]any{"id": "item-one", "type": "function_call", "call_id": "call-one", "name": "second", "arguments": "{}", "output_index": 0}})
	input += responsesRegressionFrame(t, "response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"id": "item-zero", "type": "function_call", "call_id": "call-zero", "name": "first", "arguments": "{}", "output_index": 1}})
	output := responsesRegressionOutput(t, responsesRegressionAggregate(t, input+responsesRegressionTerminal(t, "completed", nil)), 2)
	if object(output[0])["id"] != "item-zero" || object(output[1])["id"] != "item-one" {
		t.Fatalf("item field overrode event output_index: %#v", output)
	}
}

func TestResponsesAggregateDoneOnlyItemsAndParts(t *testing.T) {
	input := responsesRegressionFrame(t, "response.output_text.done", map[string]any{"item_id": "msg-done", "output_index": 0, "content_index": 0, "text": "done only"})
	input += responsesRegressionFrame(t, "response.content_part.done", map[string]any{"item_id": "msg-done", "output_index": 0, "content_index": 1, "part": map[string]any{"type": "refusal", "refusal": "synthetic refusal"}})
	tool := map[string]any{"id": "item-done", "type": "function_call", "call_id": "call-done", "name": "done_function", "arguments": `{"ok":true}`, "status": "completed"}
	input += responsesRegressionFrame(t, "response.output_item.done", map[string]any{"output_index": 1, "item": tool})
	output := responsesRegressionOutput(t, responsesRegressionAggregate(t, input+responsesRegressionTerminal(t, "completed", nil)), 2)
	content := list(object(output[0])["content"])
	if len(content) != 2 || object(content[0])["text"] != "done only" || object(content[1])["refusal"] != "synthetic refusal" {
		t.Fatalf("done-only message lost: %#v", output)
	}
	responsesRegressionAssertJSON(t, output[1], tool)
}

func TestResponsesAggregateArgumentsWithoutAdded(t *testing.T) {
	input := responsesRegressionFrame(t, "response.function_call_arguments.delta", map[string]any{"item_id": "item-late", "output_index": 0, "delta": `{"a":`})
	input += responsesRegressionFrame(t, "response.function_call_arguments.delta", map[string]any{"item_id": "item-late", "output_index": 0, "delta": `1}`})
	input += responsesRegressionFrame(t, "response.function_call_arguments.done", map[string]any{"item_id": "item-late", "output_index": 0, "arguments": `{"a":1}`})
	input += responsesRegressionFrame(t, "response.output_item.done", map[string]any{"output_index": 0, "item": map[string]any{"id": "item-late", "type": "function_call", "name": "late_metadata", "call_id": "call-late", "arguments": `{"a":1}`}})
	output := responsesRegressionOutput(t, responsesRegressionAggregate(t, input+responsesRegressionTerminal(t, "completed", nil)), 1)
	item := object(output[0])
	if item["id"] != "item-late" || item["call_id"] != "call-late" || item["arguments"] != `{"a":1}` || item["name"] != "late_metadata" {
		t.Fatalf("late tool metadata/deltas lost: %#v", item)
	}
}

func TestResponsesAggregateReasoningRefusalUsageAndOpaqueState(t *testing.T) {
	input := responsesRegressionFrame(t, "response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"id": "reasoning-item", "type": "reasoning", "summary": []any{}, "encrypted_content": "synthetic-opaque-ciphertext"}})
	input += responsesRegressionFrame(t, "response.reasoning_summary_part.added", map[string]any{"item_id": "reasoning-item", "output_index": 0, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}})
	for _, delta := range []string{"brief ", "summary"} {
		input += responsesRegressionFrame(t, "response.reasoning_summary_text.delta", map[string]any{"item_id": "reasoning-item", "output_index": 0, "summary_index": 0, "delta": delta})
	}
	input += responsesRegressionFrame(t, "response.reasoning_summary_text.done", map[string]any{"item_id": "reasoning-item", "output_index": 0, "summary_index": 0, "text": "brief summary"})
	input += responsesRegressionFrame(t, "response.reasoning_summary_part.done", map[string]any{"item_id": "reasoning-item", "output_index": 0, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": "brief summary"}})
	input += responsesRegressionFrame(t, "response.reasoning_text.delta", map[string]any{"item_id": "reasoning-item", "output_index": 0, "content_index": 0, "delta": "synthetic reasoning"})
	input += responsesRegressionFrame(t, "response.reasoning_text.done", map[string]any{"item_id": "reasoning-item", "output_index": 0, "content_index": 0, "text": "synthetic reasoning"})
	for _, delta := range []string{"cannot ", "comply"} {
		input += responsesRegressionFrame(t, "response.refusal.delta", map[string]any{"item_id": "refusal-message", "output_index": 1, "content_index": 0, "delta": delta})
	}
	input += responsesRegressionFrame(t, "response.refusal.done", map[string]any{"item_id": "refusal-message", "output_index": 1, "content_index": 0, "refusal": "cannot comply"})
	usage := map[string]any{"input_tokens": 20, "output_tokens": 12, "total_tokens": 32, "input_tokens_details": map[string]any{"cached_tokens": 7}, "output_tokens_details": map[string]any{"reasoning_tokens": 9}}
	result := responsesRegressionAggregate(t, input+responsesRegressionTerminal(t, "completed", map[string]any{"usage": usage, "opaque_state": map[string]any{"continuation": "synthetic-state"}}))
	output := responsesRegressionOutput(t, result, 2)
	reasoning := object(output[0])
	if reasoning["id"] != "reasoning-item" || reasoning["type"] != "reasoning" || reasoning["encrypted_content"] != "synthetic-opaque-ciphertext" {
		t.Fatalf("reasoning identity/state lost: %#v", reasoning)
	}
	responsesRegressionAssertJSON(t, reasoning["summary"], []any{map[string]any{"type": "summary_text", "text": "brief summary"}})
	responsesRegressionAssertJSON(t, reasoning["content"], []any{map[string]any{"type": "reasoning_text", "text": "synthetic reasoning"}})
	refusal := list(object(output[1])["content"])
	if len(refusal) != 1 || object(refusal[0])["type"] != "refusal" || object(refusal[0])["refusal"] != "cannot comply" {
		t.Fatalf("refusal lost/duplicated: %#v", output[1])
	}
	responsesRegressionAssertJSON(t, result["usage"], usage)
	responsesRegressionAssertJSON(t, result["opaque_state"], map[string]any{"continuation": "synthetic-state"})
}

func TestResponsesAggregateRejectsConflictingIdentities(t *testing.T) {
	first := responsesRegressionFrame(t, "response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"id": "item-first", "type": "function_call", "call_id": "call-first", "name": "first", "arguments": ""}})
	cases := map[string]string{
		"same-index-different-item": responsesRegressionFrame(t, "response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"id": "item-other", "type": "function_call", "call_id": "call-other", "name": "other", "arguments": ""}}),
		"same-item-different-index": responsesRegressionFrame(t, "response.function_call_arguments.delta", map[string]any{"output_index": 1, "item_id": "item-first", "delta": "{}"}),
		"delta-index-item-disagree": responsesRegressionFrame(t, "response.function_call_arguments.delta", map[string]any{"output_index": 0, "item_id": "item-other", "delta": "{}"}),
		"same-item-different-call":  responsesRegressionFrame(t, "response.output_item.done", map[string]any{"output_index": 0, "item": map[string]any{"id": "item-first", "type": "function_call", "call_id": "call-other", "name": "first", "arguments": "{}"}}),
		"terminal-reassigns-index":  responsesRegressionTerminal(t, "completed", map[string]any{"output": []any{map[string]any{"id": "item-other", "type": "function_call", "call_id": "call-other", "name": "other", "arguments": "{}"}}}),
	}
	for name, conflict := range cases {
		t.Run(name, func(t *testing.T) {
			body, err := Aggregate(context.Background(), strings.NewReader(first+conflict+responsesRegressionTerminal(t, "completed", nil)), "openai-response")
			if err == nil || len(body) != 0 {
				t.Fatalf("conflicting tools became a successful response: body=%s err=%v", body, err)
			}
		})
	}
}

func TestResponsesAggregateRequiresValidTerminalAfterPartialOutput(t *testing.T) {
	partial := responsesRegressionFrame(t, "response.output_text.delta", map[string]any{"output_index": 0, "item_id": "partial-item", "content_index": 0, "delta": "partial must not succeed"})
	terminal := responsesRegressionTerminal(t, "completed", nil)
	cases := map[string]io.Reader{
		"missing-terminal":          strings.NewReader(partial),
		"pending-terminal-at-EOF":   strings.NewReader(partial + strings.TrimSuffix(terminal, "\n")),
		"truncated-JSON":            strings.NewReader(partial + "data: {\"type\":\"response.completed\"\n\n"),
		"terminal-without-response": strings.NewReader(partial + responsesRegressionFrame(t, "response.completed", nil)),
		"terminal-null-response":    strings.NewReader(partial + responsesRegressionFrame(t, "response.completed", map[string]any{"response": nil})),
		"DONE-not-terminal":         strings.NewReader(partial + "data: [DONE]\n\n"),
		"error-after-partial":       strings.NewReader(partial + responsesRegressionFrame(t, "error", map[string]any{"error": map[string]any{"message": "synthetic failure"}})),
		"failed-after-partial":      strings.NewReader(partial + responsesRegressionFrame(t, "response.failed", map[string]any{"response": map[string]any{"status": "failed", "error": map[string]any{"message": "synthetic failure"}}})),
		"read-error-after-partial":  io.MultiReader(strings.NewReader(partial), streamFailureReader{io.ErrUnexpectedEOF}),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			body, err := Aggregate(context.Background(), input, "openai-response")
			if err == nil || len(body) != 0 {
				t.Fatalf("partial output became success: body=%s err=%v", body, err)
			}
		})
	}
}

func TestResponsesAggregateRetainsSizeBounds(t *testing.T) {
	t.Run("single-event", func(t *testing.T) {
		input := "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"item_id\":\"large\",\"content_index\":0,\"delta\":\"" + strings.Repeat("x", maxResponseBytes) + "\"}\n\n" + responsesRegressionTerminal(t, "completed", nil)
		body, err := Aggregate(context.Background(), strings.NewReader(input), "openai-response")
		if err == nil || len(body) != 0 {
			t.Fatalf("oversized event accepted: body bytes=%d err=%v", len(body), err)
		}
	})
	t.Run("cumulative-events", func(t *testing.T) {
		frame := responsesRegressionFrame(t, "response.output_text.delta", map[string]any{"output_index": 0, "item_id": "large", "content_index": 0, "delta": strings.Repeat("x", 1<<20)})
		readers := make([]io.Reader, 0, maxResponseBytes/(1<<20)+2)
		for size := 0; size <= maxResponseBytes; size += 1 << 20 {
			readers = append(readers, strings.NewReader(frame))
		}
		readers = append(readers, strings.NewReader(responsesRegressionTerminal(t, "completed", nil)))
		body, err := Aggregate(context.Background(), io.MultiReader(readers...), "openai-response")
		if err == nil || len(body) != 0 {
			t.Fatalf("oversized aggregate accepted: body bytes=%d err=%v", len(body), err)
		}
	})
}

func TestResponsesAggregateMissingIndexIsNotZero(t *testing.T) {
	// call_id can equal another item's id without linking those output items.
	input := responsesRegressionFrame(t, "response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"id": "item-zero", "type": "function_call", "call_id": "item-one", "name": "first", "arguments": ""}})
	input += responsesRegressionFrame(t, "response.output_item.added", map[string]any{"item": map[string]any{"id": "item-one", "type": "function_call", "call_id": "call-one", "name": "second", "arguments": ""}})
	input += responsesRegressionFrame(t, "response.function_call_arguments.done", map[string]any{"item_id": "item-one", "arguments": `{"second":true}`})
	input += responsesRegressionFrame(t, "response.function_call_arguments.done", map[string]any{"item_id": "item-zero", "arguments": `{"first":true}`})
	// Late index metadata binds the item already identified by item.id.
	input += responsesRegressionFrame(t, "response.output_item.done", map[string]any{"output_index": 1, "item": map[string]any{"id": "item-one", "type": "function_call", "call_id": "call-one", "name": "second", "arguments": `{"second":true}`}})
	output := responsesRegressionOutput(t, responsesRegressionAggregate(t, input+responsesRegressionTerminal(t, "completed", nil)), 2)
	if object(output[0])["id"] != "item-zero" || object(output[0])["arguments"] != `{"first":true}` || object(output[1])["id"] != "item-one" || object(output[1])["arguments"] != `{"second":true}` {
		t.Fatalf("missing index or call_id merged independent tools: %#v", output)
	}
}

func TestResponsesAggregateRejectsMalformedIndexesAndMissingIdentity(t *testing.T) {
	for _, key := range []string{"output_index", "content_index"} {
		for _, value := range []any{nil, -1, 0.5, "0", true, float64(maxResponseBytes) + 1} {
			encoded, _ := json.Marshal(value)
			t.Run(key+"="+string(encoded), func(t *testing.T) {
				event := map[string]any{"item_id": "item-invalid", "output_index": 0, "content_index": 0, "delta": "synthetic"}
				event[key] = value
				input := responsesRegressionFrame(t, "response.output_text.delta", event) + responsesRegressionTerminal(t, "completed", nil)
				body, err := Aggregate(context.Background(), strings.NewReader(input), "openai-response")
				if err == nil || len(body) != 0 {
					t.Fatalf("invalid %s accepted: body=%s err=%v", key, body, err)
				}
			})
		}
	}
	t.Run("missing-item-id-and-output-index", func(t *testing.T) {
		input := responsesRegressionFrame(t, "response.function_call_arguments.delta", map[string]any{"delta": "{}"}) + responsesRegressionTerminal(t, "completed", nil)
		body, err := Aggregate(context.Background(), strings.NewReader(input), "openai-response")
		if err == nil || len(body) != 0 {
			t.Fatalf("unidentified tool arguments accepted: body=%s err=%v", body, err)
		}
	})
}

func TestResponsesAggregateReasoningEventAliases(t *testing.T) {
	for _, variant := range []string{"delta-only", "delta-and-done", "done-only"} {
		t.Run(variant, func(t *testing.T) {
			var input string
			if variant != "done-only" {
				for _, delta := range []string{"alias ", "summary"} {
					input += responsesRegressionFrame(t, "response.reasoning.delta", map[string]any{"item_id": "reasoning-alias", "output_index": 0, "summary_index": 0, "delta": delta})
				}
			}
			if variant != "delta-only" {
				input += responsesRegressionFrame(t, "response.reasoning.done", map[string]any{"item_id": "reasoning-alias", "output_index": 0, "summary_index": 0, "text": "alias summary"})
			}
			output := responsesRegressionOutput(t, responsesRegressionAggregate(t, input+responsesRegressionTerminal(t, "completed", nil)), 1)
			reasoning := object(output[0])
			if reasoning["id"] != "reasoning-alias" || reasoning["type"] != "reasoning" {
				t.Fatalf("reasoning alias identity lost: %#v", reasoning)
			}
			responsesRegressionAssertJSON(t, reasoning["summary"], []any{map[string]any{"type": "summary_text", "text": "alias summary"}})
			if len(list(reasoning["content"])) != 0 {
				t.Fatalf("summary alias incorrectly became reasoning content: %#v", reasoning)
			}
		})
	}
}

func TestResponsesAggregateStandaloneContentPartDone(t *testing.T) {
	for _, tc := range []struct {
		partType, field, text, itemType string
	}{
		{"output_text", "text", "done-only text", "message"},
		{"refusal", "refusal", "done-only refusal", "message"},
		{"reasoning_text", "text", "done-only reasoning", "reasoning"},
	} {
		t.Run(tc.partType, func(t *testing.T) {
			part := map[string]any{"type": tc.partType, tc.field: tc.text}
			input := responsesRegressionFrame(t, "response.content_part.done", map[string]any{"item_id": "standalone-part", "output_index": 0, "content_index": 0, "part": part})
			output := responsesRegressionOutput(t, responsesRegressionAggregate(t, input+responsesRegressionTerminal(t, "completed", nil)), 1)
			item := object(output[0])
			if item["id"] != "standalone-part" || item["type"] != tc.itemType {
				t.Fatalf("standalone part lost item identity/type: %#v", item)
			}
			if tc.itemType == "message" && item["role"] != "assistant" {
				t.Fatalf("standalone message missing assistant role: %#v", item)
			}
			if tc.itemType == "reasoning" && item["role"] != nil {
				t.Fatalf("reasoning part gained a message role: %#v", item)
			}
			responsesRegressionAssertJSON(t, item["content"], []any{part})
		})
	}
}

func TestResponsesAggregateMergesPartialUsageSnapshots(t *testing.T) {
	for _, status := range []string{"completed", "incomplete"} {
		t.Run(status, func(t *testing.T) {
			input := responsesRegressionFrame(t, "response.created", map[string]any{"response": map[string]any{"id": "resp-regression", "usage": map[string]any{
				"input_tokens": 12, "output_tokens": 0,
				"input_tokens_details":  map[string]any{"cached_tokens": 7, "audio_tokens": 2},
				"output_tokens_details": map[string]any{"reasoning_tokens": 0},
			}}})
			input += responsesRegressionFrame(t, "response.in_progress", map[string]any{"response": map[string]any{"id": "resp-regression", "usage": map[string]any{
				"output_tokens": 4, "output_tokens_details": map[string]any{"reasoning_tokens": 2, "text_tokens": 2},
			}}})
			input += responsesRegressionTerminal(t, status, map[string]any{"usage": map[string]any{
				"output_tokens": 9, "total_tokens": 21,
				"input_tokens_details":  map[string]any{"cached_tokens": 0},
				"output_tokens_details": map[string]any{"reasoning_tokens": 5},
			}})
			result := responsesRegressionAggregate(t, input)
			responsesRegressionAssertJSON(t, result["usage"], map[string]any{
				"input_tokens": 12, "output_tokens": 9, "total_tokens": 21,
				"input_tokens_details":  map[string]any{"cached_tokens": 0, "audio_tokens": 2},
				"output_tokens_details": map[string]any{"reasoning_tokens": 5, "text_tokens": 2},
			})
		})
	}
}
