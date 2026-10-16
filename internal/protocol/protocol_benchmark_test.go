package protocol

import (
	"context"
	"strings"
	"testing"

	"github.com/megumin31/cpa-opencode-zen/internal/catalog"
)

func BenchmarkProtocolPrepareChat(b *testing.B) {
	r := catalog.Route{ProviderID: "custom", ModelID: "m", Protocol: "openai", BaseURL: "https://provider.example/v1", Settings: map[string]any{}, Compatibility: map[string]any{}}
	raw := []byte(clientRequest("openai"))
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, _, _, err := Prepare(ctx, r, FormatName("openai"), raw, Options{}); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkProtocolAggregateResponses(b *testing.B) {
	wire := nativeFixture("openai-response")
	ctx := context.Background()
	b.ReportAllocs()
	b.SetBytes(int64(len(wire)))
	b.ResetTimer()
	for b.Loop() {
		if _, err := Aggregate(ctx, strings.NewReader(wire), "openai-response"); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkProtocolForwardChatResponses(b *testing.B) {
	wire := nativeFixture("openai")
	ctx := context.Background()
	r := catalog.Route{Protocol: "openai", Alias: "opencode/p/m"}
	original, request := []byte(clientRequest("openai-response")), []byte(clientRequest("openai"))
	emit := func([]byte) error { return nil }
	output := FormatName("openai-response")
	b.ReportAllocs()
	b.SetBytes(int64(len(wire)))
	b.ResetTimer()
	for b.Loop() {
		if err := Forward(ctx, strings.NewReader(wire), r, output, original, request, emit); err != nil {
			b.Fatal(err)
		}
	}
}
