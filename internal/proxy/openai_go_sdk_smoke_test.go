package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
)

func newSDKSmokeHTTPServer(t *testing.T, up *queuedUpstreamClient) *httptest.Server {
	t.Helper()

	s := newCompatTestServer(up)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("POST /v1/responses", s.handleResponses)
	return httptest.NewServer(mux)
}

func newOpenAISDKClient(baseURL string) openai.Client {
	return openai.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey("test-key"),
	)
}

func TestOpenAIGoSDKSmokeChatCompletions(t *testing.T) {
	up := &queuedUpstreamClient{
		results: []queuedUpstreamResult{
			{
				body: `data: {"type":"response.created","response":{"id":"resp_sdk_chat_1"}}

data: {"type":"response.output_text.delta","delta":"SDK chat works"}

data: {"type":"response.completed","response":{"id":"resp_sdk_chat_1","usage":{"input_tokens":4,"output_tokens":3,"total_tokens":7}}}
`,
			},
		},
	}

	httpSrv := newSDKSmokeHTTPServer(t, up)
	defer httpSrv.Close()

	client := newOpenAISDKClient(httpSrv.URL + "/v1")

	out, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model: shared.ChatModel("gpt-5"),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("hello from sdk"),
		},
	})
	if err != nil {
		t.Fatalf("sdk chat completion failed: %v", err)
	}

	if len(out.Choices) == 0 {
		t.Fatalf("expected non-empty choices, got: %+v", out)
	}
	if got := out.Choices[0].Message.Content; !strings.Contains(got, "SDK chat works") {
		t.Fatalf("unexpected content: %q", got)
	}
	if len(up.calls) != 1 {
		t.Fatalf("upstream call count: got %d want %d", len(up.calls), 1)
	}
}

func TestOpenAIGoSDKSmokeChatCompletionsStreamingWithTools(t *testing.T) {
	up := &queuedUpstreamClient{
		results: []queuedUpstreamResult{
			{
				body: `data: {"type":"response.created","response":{"id":"resp_sdk_chat_stream"}}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}

data: {"type":"response.completed","response":{"id":"resp_sdk_chat_stream"}}
`,
			},
		},
	}

	httpSrv := newSDKSmokeHTTPServer(t, up)
	defer httpSrv.Close()

	client := newOpenAISDKClient(httpSrv.URL + "/v1")

	stream := client.Chat.Completions.NewStreaming(context.Background(), openai.ChatCompletionNewParams{
		Model: shared.ChatModel("gpt-5"),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("weather in Paris"),
		},
		Tools: []openai.ChatCompletionToolParam{
			{
				Function: shared.FunctionDefinitionParam{
					Name: "get_weather",
					Parameters: shared.FunctionParameters{
						"type": "object",
						"properties": map[string]any{
							"city": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
	})

	var sawToolCall bool
	var sawToolFinish bool
	for stream.Next() {
		chunk := stream.Current()
		for _, choice := range chunk.Choices {
			if choice.FinishReason == "tool_calls" {
				sawToolFinish = true
			}
			for _, tc := range choice.Delta.ToolCalls {
				if tc.Function.Name == "get_weather" && strings.Contains(tc.Function.Arguments, `"city":"Paris"`) {
					sawToolCall = true
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("chat stream failed: %v", err)
	}
	if !sawToolCall {
		t.Fatal("expected tool call delta in sdk stream")
	}
	if !sawToolFinish {
		t.Fatal("expected tool_calls finish_reason in sdk stream")
	}
}

func TestOpenAIGoSDKSmokeResponsesStreamingAndToolLoop(t *testing.T) {
	up := &queuedUpstreamClient{
		results: []queuedUpstreamResult{
			{
				body: `data: {"type":"response.created","response":{"id":"resp_sdk_loop_1"}}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}

data: {"type":"response.completed","response":{"id":"resp_sdk_loop_1"}}
`,
			},
			{
				body: `data: {"type":"response.created","response":{"id":"resp_sdk_loop_2"}}

data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"21C"}]}}

data: {"type":"response.completed","response":{"id":"resp_sdk_loop_2"}}
`,
			},
		},
	}

	httpSrv := newSDKSmokeHTTPServer(t, up)
	defer httpSrv.Close()

	client := newOpenAISDKClient(httpSrv.URL + "/v1")

	stream := client.Responses.NewStreaming(context.Background(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("gpt-5"),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: openai.String("Weather in Paris?"),
		},
	})

	var firstResponseID string
	var sawFunctionCall bool
	for stream.Next() {
		evt := stream.Current()
		if evt.Type == "response.created" && evt.Response.ID != "" {
			firstResponseID = evt.Response.ID
		}
		if evt.Type == "response.output_item.done" && evt.Item.Type == "function_call" && evt.Item.Name == "get_weather" {
			sawFunctionCall = true
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("responses stream failed: %v", err)
	}
	if !sawFunctionCall {
		t.Fatal("expected function_call output item in responses stream")
	}
	if firstResponseID != "resp_sdk_loop_1" {
		t.Fatalf("unexpected first response id: %q", firstResponseID)
	}

	second, err := client.Responses.New(context.Background(), responses.ResponseNewParams{
		Model:              shared.ResponsesModel("gpt-5"),
		PreviousResponseID: openai.String(firstResponseID),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: responses.ResponseInputParam{
				responses.ResponseInputItemParamOfFunctionCallOutput("call_1", `{"temp_c":21}`),
			},
		},
	})
	if err != nil {
		t.Fatalf("second responses call failed: %v", err)
	}
	if second.ID != "resp_sdk_loop_2" {
		t.Fatalf("unexpected second response id: %q", second.ID)
	}
	if len(up.calls) != 2 {
		t.Fatalf("upstream call count: got %d want %d", len(up.calls), 2)
	}
}
