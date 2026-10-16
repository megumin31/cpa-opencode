package plugin

func clientRequest(protocol string) string {
	switch protocol {
	case "openai":
		return `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]}`
	case "claude":
		return `{"model":"m","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]}`
	case "openai-response":
		return `{"model":"m","input":[{"role":"user","content":"hi"}],"tools":[{"type":"function","name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}`
	case "gemini":
		return `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}]}`
	}
	return `{}`
}
func nativeFixture(protocol string) string {
	switch protocol {
	case "openai":
		return `data: {"id":"chat-test","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hello","reasoning_content":"think"}}]}

data: {"id":"chat-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-test","type":"function","function":{"name":"weather","arguments":"{\"city\":"}}]}}]}

data: {"id":"chat-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Paris\"}"}}]},"finish_reason":"tool_calls"}]}

data: {"id":"chat-test","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":7,"total_tokens":19,"prompt_tokens_details":{"cached_tokens":3}}}

data: [DONE]

`
	case "claude":
		return `event: message_start
data: {"type":"message_start","message":{"id":"msg-test","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":12,"output_tokens":0,"cache_read_input_tokens":3}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"think"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"test-signature"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call-test","name":"weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Paris\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}

event: message_stop
data: {"type":"message_stop"}

`
	case "openai-response":
		return `event: response.created
data: {"type":"response.created","response":{"id":"resp-test","object":"response","model":"m","created_at":1,"output":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg-test","type":"message","role":"assistant","content":[]}}

event: response.content_part.added
data: {"type":"response.content_part.added","item_id":"msg-test","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg-test","output_index":0,"content_index":0,"delta":"hello"}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":1,"item":{"id":"fn-test","type":"function_call","call_id":"call-test","name":"weather","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fn-test","output_index":1,"delta":"{\"city\":\"Paris\"}"}

event: response.function_call_arguments.done
data: {"type":"response.function_call_arguments.done","item_id":"fn-test","output_index":1,"arguments":"{\"city\":\"Paris\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":1,"item":{"id":"fn-test","type":"function_call","call_id":"call-test","name":"weather","arguments":"{\"city\":\"Paris\"}"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp-test","object":"response","status":"completed","model":"m","output":[{"id":"msg-test","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},{"id":"fn-test","type":"function_call","call_id":"call-test","name":"weather","arguments":"{\"city\":\"Paris\"}"},{"type":"reasoning","id":"rs-test","summary":[{"type":"summary_text","text":"think"}],"encrypted_content":"test-encrypted"}],"usage":{"input_tokens":12,"output_tokens":7,"total_tokens":19,"input_tokens_details":{"cached_tokens":3}}}}

`
	case "gemini":
		return `data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"hello"},{"text":"think","thought":true,"thoughtSignature":"test-signature"}]}}],"modelVersion":"m","responseId":"gemini-test"}

data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"id":"call-test","name":"weather","args":{"city":"Paris"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":7,"totalTokenCount":19,"cachedContentTokenCount":3}}

`
	}
	return ""
}
