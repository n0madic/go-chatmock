package normalize

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/n0madic/go-chatmock/internal/config"
	"github.com/n0madic/go-chatmock/internal/models"
	"github.com/n0madic/go-chatmock/internal/reasoning"
	"github.com/n0madic/go-chatmock/internal/state"
	"github.com/n0madic/go-chatmock/internal/types"
)

// Enrich normalizes a raw request body into a CanonicalRequest.
func Enrich(body []byte, route string, cfg *config.ServerConfig, store *state.Store) (*types.CanonicalRequest, *NormalizeError) {
	raw, chatReq, responsesReq, err := decodeUniversalBody(body)
	if err != nil {
		return nil, &NormalizeError{StatusCode: http.StatusBadRequest, Message: "Invalid JSON body"}
	}

	requestedModel := strings.TrimSpace(chatReq.Model)
	if requestedModel == "" {
		requestedModel = strings.TrimSpace(responsesReq.Model)
	}
	if requestedModel == "" {
		requestedModel = stringFromAny(raw["model"])
	}
	model := models.NormalizeModelName(requestedModel, cfg.DebugModel)

	inputItems, inputSystemInstructions, messagesCount, inputSource, usedPromptFallback, usedInputFallback, ierr := NormalizeInput(raw, route, chatReq.Prompt)
	if ierr != nil {
		return nil, ierr
	}

	conversationID := ExtractConversationID(raw)
	previousResponseID := strings.TrimSpace(responsesReq.PreviousResponseID)
	autoPreviousResponseID := false
	if previousResponseID == "" && conversationID != "" {
		if mappedID, ok := store.GetConversationLatest(conversationID); ok {
			previousResponseID = mappedID
			autoPreviousResponseID = true
		}
	}
	if route == "responses" || previousResponseID != "" {
		prependPreviousContext := route == "responses"
		var restoreErr error
		inputItems, restoreErr = store.RestoreFunctionCallContext(inputItems, previousResponseID, prependPreviousContext)
		if restoreErr != nil {
			if autoPreviousResponseID {
				previousResponseID = ""
				autoPreviousResponseID = false
			} else {
				return nil, &NormalizeError{StatusCode: http.StatusBadRequest, Message: restoreErr.Error()}
			}
		}
	}

	reasoningOverrides := chatReq.Reasoning
	if route == "responses" && responsesReq.Reasoning != nil {
		reasoningOverrides = responsesReq.Reasoning
	}
	if reasoningOverrides == nil {
		reasoningOverrides = responsesReq.Reasoning
	}
	reasoningParam := buildReasoningWithModelFallback(cfg, requestedModel, model, reasoningOverrides)

	responseFormat := "chat"
	if inputSource == "input" {
		responseFormat = "responses"
	}

	toolChoice := pickToolChoice(route, chatReq, responsesReq)
	parallelToolCalls := false
	if v, ok := raw["parallel_tool_calls"].(bool); ok {
		parallelToolCalls = v
	} else if responsesReq.ParallelToolCalls != nil {
		parallelToolCalls = *responsesReq.ParallelToolCalls
	}

	tools, baseTools, hadResponsesTools, defaultWebSearchApplied, terr := NormalizeTools(raw, responseFormat, chatReq, responsesReq, toolChoice, cfg.DefaultWebSearch)
	if terr != nil {
		return nil, terr
	}

	instructions := ComposeInstructions(cfg, store, route, model, strings.TrimSpace(responsesReq.Instructions), inputSystemInstructions, previousResponseID)

	storeForUpstream, storeForced := state.NormalizeStoreForUpstream(responsesReq.Store)

	stream := false
	if v, ok := raw["stream"].(bool); ok {
		stream = v
	} else {
		stream = chatReq.Stream || responsesReq.Stream
	}
	includeUsage := chatReq.StreamOptions != nil && chatReq.StreamOptions.IncludeUsage

	return &types.CanonicalRequest{
		ResponseFormat:          responseFormat,
		RequestedModel:          requestedModel,
		Model:                   model,
		Stream:                  stream,
		IncludeUsage:            includeUsage,
		InputItems:              inputItems,
		Instructions:            instructions,
		InputSource:             inputSource,
		MessagesCount:           messagesCount,
		Tools:                   tools,
		BaseTools:               baseTools,
		HadResponsesTools:       hadResponsesTools,
		ToolChoice:              toolChoice,
		ParallelToolCalls:       parallelToolCalls,
		PreviousResponseID:      previousResponseID,
		ConversationID:          conversationID,
		AutoPreviousResponseID:  autoPreviousResponseID,
		Include:                 responsesReq.Include,
		ReasoningParam:          reasoningParam,
		StoreRequested:          responsesReq.Store,
		StoreForUpstream:        storeForUpstream,
		StoreForced:             storeForced,
		UsedPromptFallback:      usedPromptFallback,
		UsedInputFallback:       usedInputFallback,
		DefaultWebSearchApplied: defaultWebSearchApplied,
	}, nil
}

func decodeUniversalBody(body []byte) (map[string]any, types.ChatCompletionRequest, types.ResponsesRequest, error) {
	decoded := body
	var raw map[string]any
	if err := json.Unmarshal(decoded, &raw); err != nil {
		cleaned := strings.ReplaceAll(strings.ReplaceAll(string(body), "\r", ""), "\n", "")
		if err := json.Unmarshal([]byte(cleaned), &raw); err != nil {
			return nil, types.ChatCompletionRequest{}, types.ResponsesRequest{}, err
		}
		decoded = []byte(cleaned)
	}
	var chatReq types.ChatCompletionRequest
	_ = json.Unmarshal(decoded, &chatReq)
	var responsesReq types.ResponsesRequest
	_ = json.Unmarshal(decoded, &responsesReq)
	return raw, chatReq, responsesReq, nil
}

func pickToolChoice(route string, chatReq types.ChatCompletionRequest, responsesReq types.ResponsesRequest) any {
	var toolChoice any
	if route == "chat" {
		toolChoice = chatReq.ToolChoice
		if toolChoice == nil {
			toolChoice = responsesReq.ToolChoice
		}
	} else {
		toolChoice = responsesReq.ToolChoice
		if toolChoice == nil {
			toolChoice = chatReq.ToolChoice
		}
	}
	if toolChoice == nil {
		toolChoice = "auto"
	}
	if rtc := strings.TrimSpace(chatReq.ResponsesToolChoice); rtc == "auto" || rtc == "none" {
		toolChoice = rtc
	}
	return toolChoice
}

func buildReasoningWithModelFallback(
	cfg *config.ServerConfig,
	requestedModel string,
	normalizedModel string,
	reasoningOverrides *types.ReasoningParam,
) *types.ReasoningParam {
	if reasoningOverrides == nil {
		reasoningOverrides = reasoning.ExtractFromModelName(requestedModel)
	}
	return reasoning.BuildReasoningParam(
		cfg.ReasoningEffort,
		cfg.ReasoningSummary,
		reasoningOverrides,
		normalizedModel,
	)
}
