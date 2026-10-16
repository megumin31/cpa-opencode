// sdkrepro calls only the public CPA SDK. No plugin routing or normalization.
package main

import (
	"bytes"
	"context"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin"
	"os"
)

const combined = `data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}]}`
const tool = `data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":null}]}`
const finish = `data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`

func translate(records ...string) []byte {
	var state any
	var result []byte
	registry := builtin.Registry()
	for _, record := range records {
		for _, chunk := range registry.TranslateStream(context.Background(), translator.FormatOpenAI, translator.FormatGemini, "test", []byte(`{"contents":[{"role":"user","parts":[{"text":"weather"}]}]}`), []byte(`{"model":"test","messages":[{"role":"user","content":"weather"}],"stream":true}`), []byte(record), &state) {
			result = append(result, chunk...)
			result = append(result, '\n')
		}
	}
	return result
}
func main() {
	bad := translate(combined, "data: [DONE]")
	good := translate(tool, finish, "data: [DONE]")
	fmt.Printf("CPA v8.0.29 provider OpenAI Chat SSE -> Gemini client\ncombined frame output: %q\nsplit frame output: %s\n", bad, good)
	if bytes.Contains(bad, []byte(`"functionCall"`)) || !bytes.Contains(good, []byte(`"functionCall"`)) || !bytes.Contains(good, []byte(`"Paris"`)) {
		fmt.Fprintln(os.Stderr, "reproduction expectation changed")
		os.Exit(1)
	}
	fmt.Println("REPRODUCED: same-frame tool delta + finish_reason loses the tool; separate equivalent finish frame preserves it")
}
