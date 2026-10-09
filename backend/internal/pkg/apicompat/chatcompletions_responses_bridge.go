package apicompat

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ResponsesToChatCompletionsRequest converts a Responses API request into a
// Chat Completions request for upstreams that only implement
// /v1/chat/completions.
func ResponsesToChatCompletionsRequest(req *ResponsesRequest) (*ChatCompletionsRequest, error) {
	if req == nil {
		return nil, fmt.Errorf("responses request is nil")
	}

	messages, err := responsesInputToChatMessages(req.Instructions, req.Input)
	if err != nil {
		return nil, err
	}

	out := &ChatCompletionsRequest{
		Model:               req.Model,
		Messages:            messages,
		MaxCompletionTokens: req.MaxOutputTokens,
		Temperature:         req.Temperature,
		TopP:                req.TopP,
		Stream:              req.Stream,
		ServiceTier:         req.ServiceTier,
		ParallelToolCalls:   req.ParallelToolCalls,
	}
	if req.Reasoning != nil {
		out.ReasoningEffort = req.Reasoning.Effort
	}
	if len(req.Tools) > 0 {
		out.Tools, out.CustomToolNames = responsesToolsToChatTools(req.Tools)
	}
	if len(req.ToolChoice) > 0 {
		out.ToolChoice = responsesToolChoiceToChatToolChoice(req.ToolChoice)
	}

	return out, nil
}

func responsesInputToChatMessages(instructions string, inputRaw json.RawMessage) ([]ChatMessage, error) {
	var messages []ChatMessage
	if strings.TrimSpace(instructions) != "" {
		content, _ := json.Marshal(instructions)
		messages = append(messages, ChatMessage{
			Role:    "system",
			Content: content,
		})
	}

	inputRaw = bytesTrimSpace(inputRaw)
	if len(inputRaw) == 0 || string(inputRaw) == "null" {
		return messages, nil
	}

	var inputText string
	if err := json.Unmarshal(inputRaw, &inputText); err == nil {
		content, _ := json.Marshal(inputText)
		messages = append(messages, ChatMessage{
			Role:    "user",
			Content: content,
		})
		return messages, nil
	}

	var rawItems []json.RawMessage
	if err := json.Unmarshal(inputRaw, &rawItems); err != nil {
		return nil, fmt.Errorf("parse responses input: %w", err)
	}

	for _, raw := range rawItems {
		raw = bytesTrimSpace(raw)
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}

		var item map[string]json.RawMessage
		if err := json.Unmarshal(raw, &item); err != nil {
			var text string
			if textErr := json.Unmarshal(raw, &text); textErr == nil {
				content, _ := json.Marshal(text)
				messages = append(messages, ChatMessage{Role: "user", Content: content})
				continue
			}
			return nil, fmt.Errorf("parse responses input item: %w", err)
		}

		role := chatCompletionsBridgeRole(rawString(item["role"]))
		itemType := rawString(item["type"])
		switch itemType {
		case "function_call":
			arguments := rawString(item["arguments"])
			if strings.TrimSpace(arguments) == "" {
				arguments = "{}"
			}
			messages = append(messages, ChatMessage{
				Role: "assistant",
				ToolCalls: []ChatToolCall{{
					ID:   rawString(item["call_id"]),
					Type: "function",
					Function: ChatFunctionCall{
						Name:      rawString(item["name"]),
						Arguments: arguments,
					},
				}},
			})
			continue
		case "custom_tool_call":
			arguments := wrapCustomToolInput(rawString(item["input"]))
			messages = append(messages, ChatMessage{Role: "assistant", ToolCalls: []ChatToolCall{{ID: rawString(item["call_id"]), Type: "function", Function: ChatFunctionCall{Name: rawString(item["name"]), Arguments: arguments}}}})
			continue
		case "custom_tool_call_output":
			content, _ := json.Marshal(rawString(item["output"]))
			messages = append(messages, ChatMessage{Role: "tool", ToolCallID: rawString(item["call_id"]), Content: content})
			continue
		case "function_call_output":
			content, _ := json.Marshal(rawString(item["output"]))
			messages = append(messages, ChatMessage{
				Role:       "tool",
				ToolCallID: rawString(item["call_id"]),
				Content:    content,
			})
			continue
		case "input_text", "text":
			content, _ := json.Marshal(rawString(item["text"]))
			messages = append(messages, ChatMessage{Role: "user", Content: content})
			continue
		case "input_image":
			content, err := chatContentFromSingleResponsesPart(itemType, item)
			if err != nil {
				return nil, err
			}
			messages = append(messages, ChatMessage{Role: "user", Content: content})
			continue
		}

		content := item["content"]
		if len(bytesTrimSpace(content)) == 0 {
			if text := rawString(item["text"]); text != "" {
				content, _ = json.Marshal(text)
			}
		}
		chatContent, err := responsesContentToChatContent(content, role)
		if err != nil {
			return nil, err
		}
		messages = append(messages, ChatMessage{
			Role:    role,
			Content: chatContent,
		})
	}

	return messages, nil
}

func chatCompletionsBridgeRole(role string) string {
	trimmed := strings.TrimSpace(role)
	if trimmed == "" {
		return "user"
	}
	if strings.EqualFold(trimmed, "developer") {
		return "system"
	}
	return role
}

func responsesContentToChatContent(raw json.RawMessage, role string) (json.RawMessage, error) {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		empty, _ := json.Marshal("")
		return empty, nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return raw, nil
	}

	var rawParts []json.RawMessage
	if err := json.Unmarshal(raw, &rawParts); err == nil {
		return responsesContentPartsToChatContent(rawParts, role)
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		return chatContentFromSingleResponsesPart(rawString(obj["type"]), obj)
	}

	return raw, nil
}

func responsesContentPartsToChatContent(rawParts []json.RawMessage, role string) (json.RawMessage, error) {
	var textParts []string
	var chatParts []ChatContentPart
	hasNonText := false

	for _, rawPart := range rawParts {
		var part map[string]json.RawMessage
		if err := json.Unmarshal(rawPart, &part); err != nil {
			continue
		}
		partType := rawString(part["type"])
		switch partType {
		case "input_text", "output_text", "text", "":
			text := rawString(part["text"])
			if text == "" {
				continue
			}
			textParts = append(textParts, text)
			chatParts = append(chatParts, ChatContentPart{Type: "text", Text: text})
		case "input_image", "image_url":
			imageURL := rawString(part["image_url"])
			if imageURL == "" {
				imageURL = rawNestedString(part["image_url"], "url")
			}
			if imageURL == "" {
				continue
			}
			hasNonText = true
			chatParts = append(chatParts, ChatContentPart{
				Type:     "image_url",
				ImageURL: &ChatImageURL{URL: imageURL},
			})
		}
	}

	if !hasNonText {
		joined, _ := json.Marshal(strings.Join(textParts, "\n\n"))
		return joined, nil
	}
	if role != "user" {
		joined, _ := json.Marshal(strings.Join(textParts, "\n\n"))
		return joined, nil
	}
	if len(chatParts) == 0 {
		empty, _ := json.Marshal("")
		return empty, nil
	}
	return json.Marshal(chatParts)
}

func chatContentFromSingleResponsesPart(partType string, part map[string]json.RawMessage) (json.RawMessage, error) {
	switch partType {
	case "input_image", "image_url":
		imageURL := rawString(part["image_url"])
		if imageURL == "" {
			imageURL = rawNestedString(part["image_url"], "url")
		}
		return json.Marshal([]ChatContentPart{{
			Type:     "image_url",
			ImageURL: &ChatImageURL{URL: imageURL},
		}})
	default:
		return json.Marshal(rawString(part["text"]))
	}
}

func responsesToolsToChatTools(tools []ResponsesTool) ([]ChatTool, map[string]bool) {
	out := make([]ChatTool, 0, len(tools))
	customNames := make(map[string]bool)
	for _, tool := range tools {
		if tool.Type != "function" && tool.Type != "custom" {
			continue
		}
		if tool.Type == "custom" {
			customNames[tool.Name] = true
		}
		parameters := tool.Parameters
		if tool.Type == "custom" {
			parameters = json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"],"additionalProperties":false}`)
		}
		out = append(out, ChatTool{
			Type: "function",
			Function: &ChatFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  parameters,
				Strict:      tool.Strict,
			},
		})
	}
	return out, customNames
}

func responsesToolChoiceToChatToolChoice(raw json.RawMessage) json.RawMessage {
	var choice map[string]json.RawMessage
	if err := json.Unmarshal(raw, &choice); err != nil {
		return raw
	}
	choiceType := rawString(choice["type"])
	if choiceType != "function" && choiceType != "custom" {
		return raw
	}
	name := rawString(choice["name"])
	if name == "" {
		name = rawNestedString(choice["function"], "name")
	}
	if name == "" {
		return raw
	}
	out, err := json.Marshal(map[string]any{
		"type": "function",
		"function": map[string]string{
			"name": name,
		},
	})
	if err != nil {
		return raw
	}
	return out
}

// ChatCompletionsResponseToResponses converts a non-streaming Chat Completions
// response into a Responses API response.
func ChatCompletionsResponseToResponses(resp *ChatCompletionsResponse, model string, customToolNames map[string]bool) *ResponsesResponse {
	id := ""
	if resp != nil {
		id = resp.ID
	}
	if id == "" {
		id = generateResponsesID()
	}

	out := &ResponsesResponse{
		ID:     id,
		Object: "response",
		Model:  model,
		Status: "completed",
	}
	if resp == nil {
		out.Output = []ResponsesOutput{emptyResponsesMessageOutput()}
		return out
	}
	if out.Model == "" {
		out.Model = resp.Model
	}

	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		out.Output = chatMessageToResponsesOutput(choice.Message, customToolNames)
		if choice.FinishReason == "length" {
			out.Status = "incomplete"
			out.IncompleteDetails = &ResponsesIncompleteDetails{Reason: "max_output_tokens"}
		}
	}
	if len(out.Output) == 0 {
		out.Output = []ResponsesOutput{emptyResponsesMessageOutput()}
	}
	if resp.Usage != nil {
		out.Usage = ChatUsageToResponsesUsage(resp.Usage)
	}
	return out
}

func chatMessageToResponsesOutput(message ChatMessage, customToolNames map[string]bool) []ResponsesOutput {
	var outputs []ResponsesOutput
	if message.ReasoningContent != "" {
		outputs = append(outputs, ResponsesOutput{
			Type: "reasoning",
			ID:   generateItemID(),
			Summary: []ResponsesSummary{{
				Type: "summary_text",
				Text: message.ReasoningContent,
			}},
		})
	}

	text := chatMessageContentText(message.Content)
	if text != "" || len(message.ToolCalls) == 0 {
		outputs = append(outputs, ResponsesOutput{
			Type: "message",
			ID:   generateItemID(),
			Role: "assistant",
			Content: []ResponsesContentPart{{
				Type: "output_text",
				Text: text,
			}},
			Status: "completed",
		})
	}

	for _, toolCall := range message.ToolCalls {
		arguments := toolCall.Function.Arguments
		if strings.TrimSpace(arguments) == "" {
			arguments = "{}"
		}
		outputType := "function_call"
		if customToolNames[toolCall.Function.Name] {
			outputType = "custom_tool_call"
		}
		customInput := ""
		if outputType == "custom_tool_call" {
			customInput = unwrapCustomToolInput(arguments)
		}
		outputs = append(outputs, ResponsesOutput{
			Type:      outputType,
			ID:        generateItemID(),
			CallID:    toolCall.ID,
			Name:      toolCall.Function.Name,
			Arguments: arguments,
			Input:     customInput,
			Status:    "completed",
		})
	}

	return outputs
}

func emptyResponsesMessageOutput() ResponsesOutput {
	return ResponsesOutput{
		Type:    "message",
		ID:      generateItemID(),
		Role:    "assistant",
		Content: []ResponsesContentPart{{Type: "output_text", Text: ""}},
		Status:  "completed",
	}
}

func chatMessageContentText(raw json.RawMessage) string {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var parts []ChatContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		var texts []string
		for _, part := range parts {
			if part.Type == "text" && part.Text != "" {
				texts = append(texts, part.Text)
			}
		}
		return strings.Join(texts, "\n\n")
	}
	return ""
}

// ChatUsageToResponsesUsage converts Chat Completions token usage to Responses
// usage shape.
func ChatUsageToResponsesUsage(usage *ChatUsage) *ResponsesUsage {
	if usage == nil {
		return nil
	}
	out := &ResponsesUsage{
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		TotalTokens:  usage.TotalTokens,
	}
	if out.TotalTokens == 0 {
		out.TotalTokens = out.InputTokens + out.OutputTokens
	}
	if usage.PromptTokensDetails != nil {
		details := usage.PromptTokensDetails
		cacheCreationTokens := details.CacheWriteTokens
		if cacheCreationTokens == 0 {
			cacheCreationTokens = details.CacheCreationTokens
		}
		if cacheCreationTokens > 0 {
			out.CacheCreationInputTokens = cacheCreationTokens
		}
		if details.CachedTokens > 0 || details.AudioTokens > 0 || details.CacheCreationTokens > 0 || details.CacheWriteTokens > 0 {
			out.InputTokensDetails = &ResponsesInputTokensDetails{
				CachedTokens:        details.CachedTokens,
				AudioTokens:         details.AudioTokens,
				CacheCreationTokens: details.CacheCreationTokens,
				CacheWriteTokens:    details.CacheWriteTokens,
			}
		}
	}
	return out
}

// ChatCompletionsToResponsesStreamState tracks state while converting Chat
// Completions SSE chunks into Responses SSE events.
type ChatCompletionsToResponsesStreamState struct {
	ResponseID      string
	Model           string
	Created         int64
	SequenceNumber  int
	CreatedSent     bool
	InProgressSent  bool
	ContentPartSent bool
	CompletedSent   bool

	MessageItemID      string
	MessageOutputIndex int
	NextOutputIndex    int
	Text               strings.Builder
	Reasoning          strings.Builder
	ToolCalls          map[int]*ChatToolCall
	ToolItemIDs        map[int]string
	ToolOutputIndices  map[int]int
	CustomToolNames    map[string]bool

	FinishReason string
	Usage        *ResponsesUsage
}

// NewChatCompletionsToResponsesStreamState returns an initialized stream state.
func NewChatCompletionsToResponsesStreamState(model string) *ChatCompletionsToResponsesStreamState {
	return &ChatCompletionsToResponsesStreamState{
		ResponseID:         generateResponsesID(),
		Model:              model,
		Created:            time.Now().Unix(),
		MessageOutputIndex: -1,
		ToolCalls:          make(map[int]*ChatToolCall),
		ToolItemIDs:        make(map[int]string),
		ToolOutputIndices:  make(map[int]int),
		CustomToolNames:    make(map[string]bool),
	}
}

// ChatCompletionsChunkToResponsesEvents converts one Chat Completions stream
// chunk into zero or more Responses stream events.
func ChatCompletionsChunkToResponsesEvents(
	chunk *ChatCompletionsChunk,
	state *ChatCompletionsToResponsesStreamState,
) []ResponsesStreamEvent {
	if chunk == nil || state == nil {
		return nil
	}
	if chunk.ID != "" {
		state.ResponseID = chunk.ID
	}
	if state.Model == "" && chunk.Model != "" {
		state.Model = chunk.Model
	}
	if chunk.Usage != nil {
		state.Usage = ChatUsageToResponsesUsage(chunk.Usage)
	}

	var events []ResponsesStreamEvent
	events = append(events, ensureChatToResponsesCreated(state)...)
	events = append(events, ensureChatToResponsesInProgress(state)...)

	for _, choice := range chunk.Choices {
		if choice.Delta.Content != nil && (state.MessageItemID != "" || strings.TrimSpace(*choice.Delta.Content) != "") {
			events = append(events, ensureChatToResponsesMessageItem(state)...)
			events = append(events, ensureChatToResponsesContentPart(state)...)
			_, _ = state.Text.WriteString(*choice.Delta.Content)
			events = append(events, chatToResponsesEvent(state, "response.output_text.delta", &ResponsesStreamEvent{
				OutputIndex:  0,
				ContentIndex: 0,
				Delta:        *choice.Delta.Content,
				ItemID:       state.MessageItemID,
			}))
		}
		if choice.Delta.ReasoningContent != nil {
			_, _ = state.Reasoning.WriteString(*choice.Delta.ReasoningContent)
			events = append(events, chatToResponsesEvent(state, "response.reasoning_summary_text.delta", &ResponsesStreamEvent{
				OutputIndex:  0,
				SummaryIndex: 0,
				Delta:        *choice.Delta.ReasoningContent,
			}))
		}
		for _, toolCall := range choice.Delta.ToolCalls {
			idx := 0
			if toolCall.Index != nil {
				idx = *toolCall.Index
			}
			stored, ok := state.ToolCalls[idx]
			if !ok {
				copyCall := toolCall
				copyCall.Function.Arguments = ""
				if copyCall.ID == "" {
					copyCall.ID = generateItemID()
				}
				copyCall.Type = "function"
				state.ToolCalls[idx] = &copyCall
				stored = &copyCall
				itemID := generateItemID()
				state.ToolItemIDs[idx] = itemID
				state.ToolOutputIndices[idx] = state.NextOutputIndex
				state.NextOutputIndex++
				outputType := "function_call"
				if state.CustomToolNames[stored.Function.Name] {
					outputType = "custom_tool_call"
				}
				events = append(events, chatToResponsesEvent(state, "response.output_item.added", &ResponsesStreamEvent{
					OutputIndex: state.ToolOutputIndices[idx],
					Item: &ResponsesOutput{
						Type:   outputType,
						ID:     itemID,
						CallID: stored.ID,
						Name:   stored.Function.Name,
						Status: "in_progress",
					},
				}))
			} else {
				if toolCall.ID != "" {
					stored.ID = toolCall.ID
				}
				if toolCall.Function.Name != "" {
					stored.Function.Name = toolCall.Function.Name
				}
			}
			if toolCall.Function.Arguments != "" {
				stored.Function.Arguments += toolCall.Function.Arguments
				if !state.CustomToolNames[stored.Function.Name] {
					events = append(events, chatToResponsesEvent(state, "response.function_call_arguments.delta", &ResponsesStreamEvent{
						OutputIndex: state.ToolOutputIndices[idx],
						Delta:       toolCall.Function.Arguments,
						ItemID:      state.ToolItemIDs[idx],
						CallID:      stored.ID,
						Name:        stored.Function.Name,
					}))
				}
			}
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			state.FinishReason = *choice.FinishReason
		}
	}

	return events
}

// FinalizeChatCompletionsResponsesStream emits terminal Responses events.
func FinalizeChatCompletionsResponsesStream(state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if state == nil || state.CompletedSent {
		return nil
	}
	var events []ResponsesStreamEvent
	events = append(events, ensureChatToResponsesCreated(state)...)
	if state.MessageItemID != "" {
		events = append(events, chatToResponsesEvent(state, "response.output_text.done", &ResponsesStreamEvent{
			OutputIndex:  state.MessageOutputIndex,
			ContentIndex: 0,
			Text:         state.Text.String(),
			ItemID:       state.MessageItemID,
		}))
		if state.ContentPartSent {
			events = append(events, chatToResponsesEvent(state, "response.content_part.done", &ResponsesStreamEvent{OutputIndex: state.MessageOutputIndex, ContentIndex: 0, ItemID: state.MessageItemID, Part: &ResponsesContentPart{Type: "output_text", Text: state.Text.String()}}))
		}
		events = append(events, chatToResponsesEvent(state, "response.output_item.done", &ResponsesStreamEvent{
			OutputIndex: state.MessageOutputIndex,
			Item: &ResponsesOutput{
				Type:    "message",
				ID:      state.MessageItemID,
				Role:    "assistant",
				Status:  "completed",
				Phase:   "final_answer",
				Content: []ResponsesContentPart{{Type: "output_text", Text: state.Text.String()}},
			},
		}))
	}
	for i := 0; i < len(state.ToolCalls); i++ {
		toolCall, ok := state.ToolCalls[i]
		if !ok || toolCall == nil {
			continue
		}
		arguments := toolCall.Function.Arguments
		if strings.TrimSpace(arguments) == "" {
			arguments = "{}"
		}
		outputIndex := state.ToolOutputIndices[i]
		itemID := state.ToolItemIDs[i]
		if state.CustomToolNames[toolCall.Function.Name] {
			input := unwrapCustomToolInput(arguments)
			if input != "" {
				events = append(events, chatToResponsesEvent(state, "response.custom_tool_call_input.delta", &ResponsesStreamEvent{
					OutputIndex: outputIndex,
					Delta:       input,
					ItemID:      itemID,
				}))
			}
			events = append(events, chatToResponsesEvent(state, "response.custom_tool_call_input.done", &ResponsesStreamEvent{
				OutputIndex: outputIndex,
				Input:       input,
				ItemID:      itemID,
			}))
			events = append(events, chatToResponsesEvent(state, "response.output_item.done", &ResponsesStreamEvent{
				OutputIndex: outputIndex,
				Item: &ResponsesOutput{
					Type:   "custom_tool_call",
					ID:     itemID,
					CallID: toolCall.ID,
					Name:   toolCall.Function.Name,
					Input:  input,
					Status: "completed",
				},
			}))
			continue
		}
		events = append(events, chatToResponsesEvent(state, "response.function_call_arguments.done", &ResponsesStreamEvent{
			OutputIndex: outputIndex,
			Arguments:   arguments,
			ItemID:      itemID,
		}))
		events = append(events, chatToResponsesEvent(state, "response.output_item.done", &ResponsesStreamEvent{
			OutputIndex: outputIndex,
			Item: &ResponsesOutput{
				Type:      "function_call",
				ID:        itemID,
				CallID:    toolCall.ID,
				Name:      toolCall.Function.Name,
				Arguments: arguments,
				Status:    "completed",
			},
		}))
	}

	status := "completed"
	var incompleteDetails *ResponsesIncompleteDetails
	if state.FinishReason == "length" {
		status = "incomplete"
		incompleteDetails = &ResponsesIncompleteDetails{Reason: "max_output_tokens"}
	}

	state.CompletedSent = true
	events = append(events, chatToResponsesEvent(state, "response.completed", &ResponsesStreamEvent{
		Response: &ResponsesResponse{
			ID:                state.ResponseID,
			Object:            "response",
			Model:             state.Model,
			Status:            status,
			Output:            state.chatOutput(),
			Usage:             state.Usage,
			IncompleteDetails: incompleteDetails,
		},
	}))
	return events
}

func ensureChatToResponsesInProgress(state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if state.InProgressSent {
		return nil
	}
	state.InProgressSent = true
	return []ResponsesStreamEvent{chatToResponsesEvent(state, "response.in_progress", &ResponsesStreamEvent{Response: &ResponsesResponse{ID: state.ResponseID, Object: "response", Model: state.Model, Status: "in_progress", Output: []ResponsesOutput{}}})}
}

func ensureChatToResponsesContentPart(state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if state.ContentPartSent {
		return nil
	}
	state.ContentPartSent = true
	return []ResponsesStreamEvent{chatToResponsesEvent(state, "response.content_part.added", &ResponsesStreamEvent{OutputIndex: state.MessageOutputIndex, ContentIndex: 0, ItemID: state.MessageItemID, Part: &ResponsesContentPart{Type: "output_text", Text: ""}})}
}

func ensureChatToResponsesCreated(state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if state.CreatedSent {
		return nil
	}
	state.CreatedSent = true
	return []ResponsesStreamEvent{chatToResponsesEvent(state, "response.created", &ResponsesStreamEvent{
		Response: &ResponsesResponse{
			ID:     state.ResponseID,
			Object: "response",
			Model:  state.Model,
			Status: "in_progress",
			Output: []ResponsesOutput{},
		},
	})}
}

func ensureChatToResponsesMessageItem(state *ChatCompletionsToResponsesStreamState) []ResponsesStreamEvent {
	if state.MessageItemID != "" {
		return nil
	}
	state.MessageItemID = generateItemID()
	state.MessageOutputIndex = state.NextOutputIndex
	state.NextOutputIndex++
	return []ResponsesStreamEvent{chatToResponsesEvent(state, "response.output_item.added", &ResponsesStreamEvent{
		OutputIndex: state.MessageOutputIndex,
		Item: &ResponsesOutput{
			Type:    "message",
			ID:      state.MessageItemID,
			Role:    "assistant",
			Status:  "in_progress",
			Phase:   "final_answer",
			Content: []ResponsesContentPart{},
		},
	})}
}

func (state *ChatCompletionsToResponsesStreamState) chatOutput() []ResponsesOutput {
	var outputs []ResponsesOutput
	if state.Reasoning.Len() > 0 {
		outputs = append(outputs, ResponsesOutput{
			Type: "reasoning",
			ID:   generateItemID(),
			Summary: []ResponsesSummary{{
				Type: "summary_text",
				Text: state.Reasoning.String(),
			}},
		})
	}
	if state.MessageItemID != "" || len(state.ToolCalls) == 0 {
		outputs = append(outputs, ResponsesOutput{
			Type:  "message",
			ID:    nonEmpty(state.MessageItemID, generateItemID()),
			Role:  "assistant",
			Phase: "final_answer",
			Content: []ResponsesContentPart{{
				Type: "output_text",
				Text: state.Text.String(),
			}},
			Status: "completed",
		})
	}
	for i := 0; i < len(state.ToolCalls); i++ {
		toolCall, ok := state.ToolCalls[i]
		if !ok || toolCall == nil {
			continue
		}
		arguments := toolCall.Function.Arguments
		if strings.TrimSpace(arguments) == "" {
			arguments = "{}"
		}
		output := ResponsesOutput{
			Type:      "function_call",
			ID:        state.ToolItemIDs[i],
			CallID:    toolCall.ID,
			Name:      toolCall.Function.Name,
			Arguments: arguments,
			Status:    "completed",
		}
		if state.CustomToolNames[toolCall.Function.Name] {
			output.Type = "custom_tool_call"
			output.Input = unwrapCustomToolInput(arguments)
			output.Arguments = ""
		}
		outputs = append(outputs, output)
	}
	return outputs
}

func chatToResponsesEvent(
	state *ChatCompletionsToResponsesStreamState,
	eventType string,
	template *ResponsesStreamEvent,
) ResponsesStreamEvent {
	seq := state.SequenceNumber
	state.SequenceNumber++
	evt := *template
	evt.Type = eventType
	evt.SequenceNumber = seq
	return evt
}

func rawString(raw json.RawMessage) string {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

func rawNestedString(raw json.RawMessage, key string) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	return rawString(obj[key])
}

func wrapCustomToolInput(input string) string {
	wrapped, err := json.Marshal(map[string]string{"input": input})
	if err != nil {
		return `{"input":""}`
	}
	return string(wrapped)
}

func unwrapCustomToolInput(arguments string) string {
	var wrapped struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal([]byte(arguments), &wrapped); err == nil {
		return wrapped.Input
	}
	return arguments
}

func bytesTrimSpace(raw json.RawMessage) json.RawMessage {
	return json.RawMessage(strings.TrimSpace(string(raw)))
}

func nonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
