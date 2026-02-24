package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/n0madic/go-chatmock/internal/models"
	responsesstate "github.com/n0madic/go-chatmock/internal/responses-state"
	"github.com/n0madic/go-chatmock/internal/sse"
	"github.com/n0madic/go-chatmock/internal/transform"
	"github.com/n0madic/go-chatmock/internal/types"
	"github.com/n0madic/go-chatmock/internal/upstream"
)

type universalRoute string

const (
	universalRouteChat      universalRoute = "chat"
	universalRouteResponses universalRoute = "responses"
)

type universalRequest struct {
	ResponseFormat          universalRoute // "chat" or "responses", derived from input format
	RequestedModel          string
	Model                   string
	Stream                  bool
	IncludeUsage            bool
	InputItems              []types.ResponsesInputItem
	Instructions            string
	Tools                   []types.ResponsesTool
	BaseTools               []types.ResponsesTool
	HadResponsesTools       bool
	ToolChoice              any
	ParallelToolCalls       bool
	Include                 []string
	StoreRequested          *bool
	StoreForUpstream        *bool
	StoreForced             bool
	PreviousResponseID      string
	ConversationID          string
	AutoPreviousResponseID  bool
	ReasoningParam          *types.ReasoningParam
	MessagesCount           int
	InputSource             string
	UsedPromptFallback      bool
	UsedInputFallback       bool
	DefaultWebSearchApplied bool
}

type requestNormalizeError struct {
	StatusCode int
	Message    string
}

func (s *Server) handleUnifiedCompletions(w http.ResponseWriter, r *http.Request, route universalRoute) {
	body, ok := readLimitedRequestBody(w, r, writeError, "Failed to read request body")
	if !ok {
		return
	}

	req, nerr := s.normalizeUniversalRequest(body, route)
	if nerr != nil {
		writeError(w, nerr.StatusCode, nerr.Message)
		return
	}

	if !s.validateModel(w, req.Model) {
		return
	}

	reasoningEffort, reasoningSummary := reasoningLogFields(req.ReasoningParam)
	if s.Config.Verbose {
		if route == universalRouteChat {
			slog.Info("openai.chat.request",
				"requested_model", req.RequestedModel,
				"upstream_model", req.Model,
				"stream", req.Stream,
				"include_usage", req.IncludeUsage,
				"messages", req.MessagesCount,
				"input_items", len(req.InputItems),
				"input_source", req.InputSource,
				"tools", len(req.Tools),
				"tool_choice", summarizeToolChoice(req.ToolChoice),
				"responses_tools", boolToInt(req.HadResponsesTools),
				"default_web_search", req.DefaultWebSearchApplied,
				"parallel_tool_calls", req.ParallelToolCalls,
				"include_count", len(req.Include),
				"store_requested", boolPtrState(req.StoreRequested),
				"store_upstream", boolPtrState(req.StoreForUpstream),
				"reasoning_effort", reasoningEffort,
				"reasoning_summary", reasoningSummary,
				"prompt_fallback", req.UsedPromptFallback,
				"input_fallback", req.UsedInputFallback,
				"previous_response_id", req.PreviousResponseID != "",
				"previous_response_id_auto", req.AutoPreviousResponseID,
				"conversation_id", req.ConversationID != "",
				"session_override", strings.TrimSpace(r.Header.Get("X-Session-Id")) != "",
			)
		} else {
			slog.Info("responses.request",
				"requested_model", req.RequestedModel,
				"upstream_model", req.Model,
				"stream", req.Stream,
				"input_items", len(req.InputItems),
				"input_source", req.InputSource,
				"tools", len(req.Tools),
				"tool_choice", summarizeToolChoice(req.ToolChoice),
				"parallel_tool_calls", req.ParallelToolCalls,
				"include_count", len(req.Include),
				"instructions_chars", len(req.Instructions),
				"previous_response_id", req.PreviousResponseID != "",
				"previous_response_id_auto", req.AutoPreviousResponseID,
				"conversation_id", req.ConversationID != "",
				"store_requested", boolPtrState(req.StoreRequested),
				"store_upstream", boolPtrState(req.StoreForUpstream),
				"default_web_search", req.DefaultWebSearchApplied,
				"reasoning_effort", reasoningEffort,
				"reasoning_summary", reasoningSummary,
				"session_override", strings.TrimSpace(r.Header.Get("X-Session-Id")) != "",
			)
		}
		if req.StoreForced {
			slog.Warn("client requested store=true; forcing store=false for upstream compatibility")
		}
	}

	upReq := &upstream.Request{
		Model:             req.Model,
		Instructions:      req.Instructions,
		InputItems:        req.InputItems,
		Tools:             req.Tools,
		ToolChoice:        req.ToolChoice,
		ParallelToolCalls: req.ParallelToolCalls,
		Include:           req.Include,
		Store:             req.StoreForUpstream,
		ReasoningParam:    req.ReasoningParam,
		SessionID:         r.Header.Get("X-Session-Id"),
	}

	resp, ok := s.doUpstreamWithResponsesToolsRetry(r.Context(), w, upReq, req.HadResponsesTools, req.BaseTools, writeError)
	if !ok {
		return
	}

	outputModel := req.RequestedModel
	if outputModel == "" {
		outputModel = req.Model
	}

	if req.ResponseFormat == universalRouteChat {
		created := time.Now().Unix()
		if req.Stream {
			writeSSEHeaders(w, resp.StatusCode)
			var rawSSE bytes.Buffer
			streamBody := newTeeReadCloser(resp.Body.Body, &rawSSE)
			sse.TranslateChat(w, streamBody, outputModel, created, sse.TranslateChatOptions{
				ReasoningCompat: s.Config.ReasoningCompat,
				IncludeUsage:    req.IncludeUsage,
			})
			s.storeChatStreamState(rawSSE.Bytes(), req.InputItems, req.Instructions, req.ConversationID)
			return
		}
		s.collectChatCompletion(w, resp, outputModel, created, req.InputItems, req.Instructions, req.ConversationID)
		return
	}

	if req.Stream {
		writeSSEHeaders(w, resp.StatusCode)
		s.streamResponsesWithState(w, resp, req.InputItems, req.Instructions, req.ConversationID)
		return
	}
	s.collectResponsesResponse(w, resp, outputModel, req.InputItems, req.Instructions, req.ConversationID)
}

func (s *Server) normalizeUniversalRequest(body []byte, route universalRoute) (*universalRequest, *requestNormalizeError) {
	raw, chatReq, responsesReq, err := decodeUniversalBody(body)
	if err != nil {
		return nil, &requestNormalizeError{StatusCode: http.StatusBadRequest, Message: "Invalid JSON body"}
	}

	requestedModel := strings.TrimSpace(chatReq.Model)
	if requestedModel == "" {
		requestedModel = strings.TrimSpace(responsesReq.Model)
	}
	if requestedModel == "" {
		requestedModel = stringFromAny(raw["model"])
	}
	model := models.NormalizeModelName(requestedModel, s.Config.DebugModel)

	inputItems, inputSystemInstructions, messagesCount, inputSource, usedPromptFallback, usedInputFallback, ierr := normalizeUniversalInput(raw, route, chatReq.Prompt)
	if ierr != nil {
		return nil, ierr
	}

	conversationID := extractConversationID(raw)
	previousResponseID := strings.TrimSpace(responsesReq.PreviousResponseID)
	autoPreviousResponseID := false
	if previousResponseID == "" && conversationID != "" {
		if mappedID, ok := s.responsesState.GetConversationLatest(conversationID); ok {
			previousResponseID = mappedID
			autoPreviousResponseID = true
		}
	}
	if route == universalRouteResponses || previousResponseID != "" {
		prependPreviousContext := route == universalRouteResponses
		var err error
		inputItems, err = s.restoreFunctionCallContext(inputItems, previousResponseID, prependPreviousContext)
		if err != nil {
			if autoPreviousResponseID {
				// Best-effort continuity for clients that omit previous_response_id.
				previousResponseID = ""
				autoPreviousResponseID = false
			} else {
				return nil, &requestNormalizeError{StatusCode: http.StatusBadRequest, Message: err.Error()}
			}
		}
	}

	reasoningOverrides := chatReq.Reasoning
	if route == universalRouteResponses && responsesReq.Reasoning != nil {
		reasoningOverrides = responsesReq.Reasoning
	}
	if reasoningOverrides == nil {
		reasoningOverrides = responsesReq.Reasoning
	}
	reasoningParam := buildReasoningWithModelFallback(s.Config, requestedModel, model, reasoningOverrides)

	responseFormat := universalRouteChat
	if inputSource == "input" {
		responseFormat = universalRouteResponses
	}

	toolChoice := pickToolChoice(route, chatReq, responsesReq)
	parallelToolCalls := false
	if v, ok := raw["parallel_tool_calls"].(bool); ok {
		parallelToolCalls = v
	} else if responsesReq.ParallelToolCalls != nil {
		parallelToolCalls = *responsesReq.ParallelToolCalls
	}

	tools, baseTools, hadResponsesTools, defaultWebSearchApplied, terr := s.normalizeUniversalTools(raw, responseFormat, chatReq, responsesReq, toolChoice)
	if terr != nil {
		return nil, terr
	}

	instructions := composeInstructionsForRoute(s, route, model, strings.TrimSpace(responsesReq.Instructions), inputSystemInstructions, previousResponseID)

	storeForUpstream, storeForced := normalizeStoreForUpstream(responsesReq.Store)

	stream := false
	if v, ok := raw["stream"].(bool); ok {
		stream = v
	} else {
		stream = chatReq.Stream || responsesReq.Stream
	}
	includeUsage := chatReq.StreamOptions != nil && chatReq.StreamOptions.IncludeUsage

	return &universalRequest{
		ResponseFormat:          responseFormat,
		RequestedModel:          requestedModel,
		Model:                   model,
		Stream:                  stream,
		IncludeUsage:            includeUsage,
		InputItems:              inputItems,
		Instructions:            instructions,
		Tools:                   tools,
		BaseTools:               baseTools,
		HadResponsesTools:       hadResponsesTools,
		ToolChoice:              toolChoice,
		ParallelToolCalls:       parallelToolCalls,
		Include:                 responsesReq.Include,
		StoreRequested:          responsesReq.Store,
		StoreForUpstream:        storeForUpstream,
		StoreForced:             storeForced,
		PreviousResponseID:      previousResponseID,
		ConversationID:          conversationID,
		AutoPreviousResponseID:  autoPreviousResponseID,
		ReasoningParam:          reasoningParam,
		MessagesCount:           messagesCount,
		InputSource:             inputSource,
		UsedPromptFallback:      usedPromptFallback,
		UsedInputFallback:       usedInputFallback,
		DefaultWebSearchApplied: defaultWebSearchApplied,
	}, nil
}

// decodeUniversalBody decodes the request body into both ChatCompletionRequest
// and ResponsesRequest simultaneously. The same body is decoded twice so that
// normalizeUniversalRequest can accept either API format on any endpoint without
// requiring separate routes per format.
//
// The CR/LF stripping fallback handles malformed payloads sent by some SDK
// versions that embed literal newlines inside JSON strings.
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

type parsedInputCandidate struct {
	Present      bool
	Valid        bool
	Usable       bool
	Items        []types.ResponsesInputItem
	Instructions string
	Messages     int
}

func normalizeUniversalInput(raw map[string]any, route universalRoute, prompt string) ([]types.ResponsesInputItem, string, int, string, bool, bool, *requestNormalizeError) {
	msgCand := parseMessagesCandidate(raw, route)
	inputCand := parseResponsesInputCandidate(raw)
	prompt = strings.TrimSpace(prompt)

	preferInput := route == universalRouteResponses
	preferred := msgCand
	alternate := inputCand
	preferredName := "messages"
	alternateName := "input"
	if preferInput {
		preferred = inputCand
		alternate = msgCand
		preferredName = "input"
		alternateName = "messages"
	}

	if preferred.Usable {
		return preferred.Items, preferred.Instructions, preferred.Messages, preferredName, false, false, nil
	}
	if alternate.Usable {
		usedInputFallback := !preferInput && alternateName == "input"
		return alternate.Items, alternate.Instructions, alternate.Messages, alternateName, false, usedInputFallback, nil
	}
	if prompt != "" {
		return []types.ResponsesInputItem{
			{Type: "message", Role: "user", Content: []types.ResponsesContent{{Type: "input_text", Text: prompt}}},
		}, "", 0, "prompt", true, false, nil
	}

	anyInvalid := (preferred.Present && !preferred.Valid) || (alternate.Present && !alternate.Valid)
	if anyInvalid {
		msg := "Request must include valid messages or input"
		if route == universalRouteResponses {
			msg = "Request must include valid input or messages"
		}
		return nil, "", 0, "", false, false, &requestNormalizeError{StatusCode: http.StatusBadRequest, Message: msg}
	}

	msg := "Request must include messages or input"
	if route == universalRouteResponses {
		msg = "Request must include input or messages"
	}
	return nil, "", 0, "", false, false, &requestNormalizeError{StatusCode: http.StatusBadRequest, Message: msg}
}

func parseMessagesCandidate(raw map[string]any, route universalRoute) parsedInputCandidate {
	rawMessages, present := raw["messages"]
	if !present {
		return parsedInputCandidate{}
	}

	msgs, ok := parseMessagesFromRaw(rawMessages)
	if !ok {
		return parsedInputCandidate{Present: true, Valid: false}
	}

	switch route {
	case universalRouteResponses:
		items, instructions := chatMessagesToResponsesInputWithSystem(msgs)
		usable := len(items) > 0 || strings.TrimSpace(instructions) != ""
		return parsedInputCandidate{
			Present:      true,
			Valid:        true,
			Usable:       usable,
			Items:        items,
			Instructions: instructions,
			Messages:     len(msgs),
		}
	default:
		normalized := append([]types.ChatMessage(nil), msgs...)
		convertSystemToUser(normalized)
		items := transform.ChatMessagesToResponsesInput(normalized)
		return parsedInputCandidate{
			Present:  true,
			Valid:    true,
			Usable:   len(items) > 0,
			Items:    items,
			Messages: len(normalized),
		}
	}
}

func parseResponsesInputCandidate(raw map[string]any) parsedInputCandidate {
	rawInput, present := raw["input"]
	if !present {
		return parsedInputCandidate{}
	}

	items, instructions, ok := parseResponsesInputFromRaw(rawInput)
	if !ok {
		return parsedInputCandidate{Present: true, Valid: false}
	}
	usable := len(items) > 0 || strings.TrimSpace(instructions) != ""
	return parsedInputCandidate{
		Present:      true,
		Valid:        true,
		Usable:       usable,
		Items:        items,
		Instructions: instructions,
	}
}

func parseMessagesFromRaw(rawMessages any) ([]types.ChatMessage, bool) {
	if rawMessages == nil {
		return nil, true
	}
	b, err := json.Marshal(rawMessages)
	if err != nil {
		return nil, false
	}
	var msgs []types.ChatMessage
	if err := json.Unmarshal(b, &msgs); err != nil {
		return nil, false
	}
	return msgs, true
}

func chatMessagesToResponsesInputWithSystem(messages []types.ChatMessage) ([]types.ResponsesInputItem, string) {
	if len(messages) == 0 {
		return nil, ""
	}

	normalized := make([]types.ChatMessage, 0, len(messages))
	var instructions []string
	for _, m := range messages {
		if m.Role != "system" {
			normalized = append(normalized, m)
			continue
		}
		if txt, ok := extractSystemTextFromChatContent(m.Content); ok {
			instructions = append(instructions, txt)
			continue
		}
		m.Role = "user"
		normalized = append(normalized, m)
	}

	items := transform.ChatMessagesToResponsesInput(normalized)
	return items, strings.Join(instructions, "\n\n")
}

func extractSystemTextFromChatContent(content any) (string, bool) {
	switch c := content.(type) {
	case string:
		text := strings.TrimSpace(c)
		if text == "" {
			return "", false
		}
		return text, true
	case []any:
		var parts []string
		for _, rawPart := range c {
			part, ok := rawPart.(map[string]any)
			if !ok {
				return "", false
			}
			typ := strings.TrimSpace(stringFromAny(part["type"]))
			if typ != "" && typ != "text" && typ != "input_text" {
				return "", false
			}
			text := strings.TrimSpace(stringFromAny(part["text"]))
			if text == "" {
				text = strings.TrimSpace(stringFromAny(part["content"]))
			}
			if text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) == 0 {
			return "", false
		}
		return strings.Join(parts, "\n"), true
	default:
		return "", false
	}
}

func pickToolChoice(route universalRoute, chatReq types.ChatCompletionRequest, responsesReq types.ResponsesRequest) any {
	var toolChoice any
	if route == universalRouteChat {
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

func (s *Server) normalizeUniversalTools(
	raw map[string]any,
	responseFormat universalRoute,
	chatReq types.ChatCompletionRequest,
	responsesReq types.ResponsesRequest,
	toolChoice any,
) ([]types.ResponsesTool, []types.ResponsesTool, bool, bool, *requestNormalizeError) {
	chatTools := transform.ToolsChatToResponses(chatReq.Tools)
	responsesTools := sanitizeResponsesTools(responsesReq.Tools)
	responsesStyleTools := parseResponsesStyleToolsFromRaw(raw["tools"])

	var primary []types.ResponsesTool
	if responseFormat == universalRouteChat {
		primary = chatTools
		if len(primary) == 0 {
			if len(responsesTools) > 0 {
				primary = responsesTools
			} else {
				primary = responsesStyleTools
			}
		}
	} else {
		primary = responsesTools
		if len(primary) == 0 {
			if len(chatTools) > 0 {
				primary = chatTools
			} else {
				primary = responsesStyleTools
			}
		}
	}

	extraTools, err := parseExplicitResponsesTools(chatReq.ResponsesTools)
	if err != nil {
		return nil, nil, false, false, &requestNormalizeError{
			StatusCode: http.StatusBadRequest,
			Message:    "Only web_search/web_search_preview are supported in responses_tools",
		}
	}
	baseTools := cloneResponsesTools(primary)
	tools := cloneResponsesTools(primary)
	if len(extraTools) > 0 {
		tools = append(tools, extraTools...)
	}
	hadResponsesTools := len(extraTools) > 0

	defaultWebSearchApplied := false
	if len(tools) == 0 && s.Config.DefaultWebSearch {
		tc, _ := toolChoice.(string)
		if strings.TrimSpace(tc) != "none" {
			tools = []types.ResponsesTool{{Type: "web_search"}}
			defaultWebSearchApplied = true
		}
	}

	return tools, baseTools, hadResponsesTools, defaultWebSearchApplied, nil
}

func parseExplicitResponsesTools(responsesTools []any) ([]types.ResponsesTool, error) {
	if responsesTools == nil {
		return nil, nil
	}
	var out []types.ResponsesTool
	for _, t := range responsesTools {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		ttype := strings.TrimSpace(stringFromAny(tm["type"]))
		if ttype != "web_search" && ttype != "web_search_preview" {
			return nil, errInvalidResponsesTool
		}
		out = append(out, types.ResponsesTool{Type: ttype})
	}
	return out, nil
}

var errInvalidResponsesTool = errors.New("invalid responses_tool")

func sanitizeResponsesTools(in []types.ResponsesTool) []types.ResponsesTool {
	if len(in) == 0 {
		return nil
	}
	out := make([]types.ResponsesTool, 0, len(in))
	for _, t := range in {
		switch t.Type {
		case "function":
			if strings.TrimSpace(t.Name) == "" {
				continue
			}
			if t.Parameters == nil {
				t.Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			if t.Strict == nil {
				t.Strict = types.BoolPtr(false)
			}
		case "custom":
			if strings.TrimSpace(t.Name) == "" {
				continue
			}
		case "web_search", "web_search_preview":
			// pass through
		default:
			continue
		}
		out = append(out, t)
	}
	return out
}

func composeInstructionsForRoute(
	s *Server,
	route universalRoute,
	model string,
	clientInstructions string,
	inputSystemInstructions string,
	previousResponseID string,
) string {
	switch route {
	case universalRouteResponses:
		instructions := joinNonEmpty("\n\n", strings.TrimSpace(clientInstructions), strings.TrimSpace(inputSystemInstructions))
		if previousResponseID != "" && instructions == "" {
			if prevInstructions, ok := s.responsesState.GetInstructions(previousResponseID); ok {
				instructions = prevInstructions
			}
		}
		return instructions
	default:
		client := joinNonEmpty("\n\n", strings.TrimSpace(clientInstructions), strings.TrimSpace(inputSystemInstructions))
		// The proxy ships a built-in Codex system prompt, but IDE agents (e.g.
		// Cursor) already include their own orchestration instructions. Mixing
		// both would conflict and confuse the model, so we only use the built-in
		// prompt when the client sends no instructions of its own.
		if client != "" {
			return client
		}
		return strings.TrimSpace(s.Config.InstructionsForModel(model))
	}
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		out = append(out, strings.TrimSpace(p))
	}
	return strings.Join(out, sep)
}

func cloneResponsesTools(tools []types.ResponsesTool) []types.ResponsesTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]types.ResponsesTool, len(tools))
	copy(out, tools)
	return out
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// extractConversationID reads a stable conversation identifier from the request
// payload. Multiple key names are checked because different clients use different
// conventions: Cursor IDE sends "cursorConversationId" inside "metadata", while
// other tools may use "conversation_id" or "conversationId" at the top level.
// The conversation ID is not forwarded upstream; it is used only for our local
// previous_response_id auto-linking.
func extractConversationID(raw map[string]any) string {
	if raw == nil {
		return ""
	}
	if md, ok := raw["metadata"].(map[string]any); ok {
		for _, key := range []string{"cursorConversationId", "conversation_id", "conversationId"} {
			if id := strings.TrimSpace(stringFromAny(md[key])); id != "" {
				return id
			}
		}
	}
	for _, key := range []string{"cursorConversationId", "conversation_id", "conversationId"} {
		if id := strings.TrimSpace(stringFromAny(raw[key])); id != "" {
			return id
		}
	}
	return ""
}

type teeReadCloser struct {
	reader io.Reader
	closer io.Closer
}

// newTeeReadCloser wraps an SSE response body so that bytes are written to dst
// while the body is being streamed to the client. This allows storeChatStreamState
// to parse the completed SSE payload for state storage after streaming ends —
// without buffering the entire body in memory before starting to stream.
func newTeeReadCloser(src io.ReadCloser, dst io.Writer) io.ReadCloser {
	return &teeReadCloser{
		reader: io.TeeReader(src, dst),
		closer: src,
	}
}

func (t *teeReadCloser) Read(p []byte) (int, error) {
	return t.reader.Read(p)
}

func (t *teeReadCloser) Close() error {
	if t.closer == nil {
		return nil
	}
	return t.closer.Close()
}

func (s *Server) storeChatStreamState(raw []byte, requestInput []types.ResponsesInputItem, instructions string, conversationID string) {
	if s == nil || len(raw) == 0 {
		return
	}

	reader := sse.NewReader(io.NopCloser(bytes.NewReader(raw)))
	var responseID string
	var outputItems []types.ResponsesOutputItem

	for {
		evt, err := reader.Next()
		if err != nil {
			break
		}

		if id := responseIDFromEvent(evt.Data); id != "" {
			responseID = id
		}
		if evt.Type != "response.output_item.done" {
			continue
		}
		item, _ := evt.Data["item"].(map[string]any)
		if item != nil {
			outputItems = append(outputItems, unmarshalOutputItem(item))
		}
	}

	if responseID == "" {
		return
	}

	var calls []types.ResponsesInputItem
	for _, it := range outputItemsToInputItems(outputItems) {
		if it.Type == "function_call" {
			calls = append(calls, it)
		}
	}
	s.responsesState.PutSnapshot(responseID, appendContextHistory(requestInput, outputItemsToInputItems(outputItems)), responseStateCallsFromInputItems(calls))
	s.responsesState.PutInstructions(responseID, instructions)
	s.responsesState.PutConversationLatest(conversationID, responseID)
}

func responseStateCallsFromInputItems(items []types.ResponsesInputItem) []responsesstate.FunctionCall {
	if len(items) == 0 {
		return nil
	}
	var calls []responsesstate.FunctionCall
	for _, it := range items {
		if it.Type != "function_call" || strings.TrimSpace(it.CallID) == "" || strings.TrimSpace(it.Name) == "" {
			continue
		}
		calls = append(calls, responsesstate.FunctionCall{
			CallID:    strings.TrimSpace(it.CallID),
			Name:      strings.TrimSpace(it.Name),
			Arguments: it.Arguments,
		})
	}
	return calls
}
