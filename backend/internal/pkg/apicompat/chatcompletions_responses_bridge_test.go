package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesInputToChatMessages_DeveloperRoleMapsToSystem(t *testing.T) {
	messages, err := responsesInputToChatMessages("", json.RawMessage(`[{"role":"developer","content":"follow project instructions"}]`))
	require.NoError(t, err)
	require.Len(t, messages, 1)

	assert.Equal(t, "system", messages[0].Role)
	assert.JSONEq(t, `"follow project instructions"`, string(messages[0].Content))
}

func TestResponsesInputToChatMessages_KeepsChatCompletionRoles(t *testing.T) {
	input := json.RawMessage(`[
		{"role":"system","content":"system message"},
		{"role":"user","content":"user message"},
		{"role":"assistant","content":"assistant message"},
		{"role":"tool","content":"tool message"}
	]`)

	messages, err := responsesInputToChatMessages("", input)
	require.NoError(t, err)
	require.Len(t, messages, 4)

	assert.Equal(t, []string{"system", "user", "assistant", "tool"}, chatMessageRoles(messages))
}

func TestResponsesInputToChatMessages_EmptyRoleFallsBackToUser(t *testing.T) {
	messages, err := responsesInputToChatMessages("", json.RawMessage(`[{"role":"","content":"hello"}]`))
	require.NoError(t, err)
	require.Len(t, messages, 1)

	assert.Equal(t, "user", messages[0].Role)
}

func TestResponsesInputToChatMessages_DeveloperRoleTrimAndCaseInsensitive(t *testing.T) {
	input := json.RawMessage(`[
		{"role":" Developer ","content":"one"},
		{"role":"\tDEVELOPER\n","content":"two"}
	]`)

	messages, err := responsesInputToChatMessages("", input)
	require.NoError(t, err)
	require.Len(t, messages, 2)

	assert.Equal(t, []string{"system", "system"}, chatMessageRoles(messages))
}

func TestResponsesToChatCompletionsRequest_InstructionsAndInputDeveloperRole(t *testing.T) {
	req := &ResponsesRequest{
		Model:        "gpt-4o",
		Instructions: "Use concise answers.",
		Input: json.RawMessage(`[
			{"role":"developer","content":[{"type":"input_text","text":"Prefer JSON."}]},
			{"role":"user","content":"Hello"}
		]`),
	}

	out, err := ResponsesToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Len(t, out.Messages, 3)

	assert.Equal(t, []string{"system", "system", "user"}, chatMessageRoles(out.Messages))
	assert.JSONEq(t, `"Use concise answers."`, string(out.Messages[0].Content))
	assert.JSONEq(t, `"Prefer JSON."`, string(out.Messages[1].Content))
	assert.JSONEq(t, `"Hello"`, string(out.Messages[2].Content))
}

func TestResponsesToChatCompletionsRequest_PreservesParallelToolCallsFalse(t *testing.T) {
	parallel := false
	req := &ResponsesRequest{
		Model:             "gpt-4o",
		Input:             json.RawMessage(`"Hi"`),
		ParallelToolCalls: &parallel,
	}

	out, err := ResponsesToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.NotNil(t, out.ParallelToolCalls)
	assert.False(t, *out.ParallelToolCalls)
}

func TestResponsesToChatCompletionsRequest_ConvertsCustomToolToFunctionWrapper(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-5.5",
		Input: json.RawMessage(`"patch it"`),
		Tools: []ResponsesTool{{Type: "custom", Name: "apply_patch", Description: "Apply a patch", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}

	out, err := ResponsesToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Len(t, out.Tools, 1)
	assert.Equal(t, "function", out.Tools[0].Type)
	assert.Equal(t, "apply_patch", out.Tools[0].Function.Name)
	assert.True(t, out.CustomToolNames["apply_patch"])
	assert.JSONEq(t, `{"type":"object","properties":{"input":{"type":"string"}},"required":["input"],"additionalProperties":false}`, string(out.Tools[0].Function.Parameters))
}

func TestResponsesToChatCompletionsRequest_ConvertsCustomToolHistory(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-5.5",
		Input: json.RawMessage(`[
			{"type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":"patch"},
			{"type":"custom_tool_call_output","call_id":"call_1","output":"done"},
			{"role":"user","content":"continue"}
		]`),
	}

	out, err := ResponsesToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Len(t, out.Messages, 3)
	assert.Equal(t, "assistant", out.Messages[0].Role)
	assert.JSONEq(t, `{"input":"patch"}`, out.Messages[0].ToolCalls[0].Function.Arguments)
	assert.Equal(t, "tool", out.Messages[1].Role)
}

func TestChatCompletionsStream_EmitsContentPartLifecycleBeforeTextDelta(t *testing.T) {
	state := NewChatCompletionsToResponsesStreamState("gpt-5.5")
	text := "hello"
	events := ChatCompletionsChunkToResponsesEvents(&ChatCompletionsChunk{
		ID: "chatcmpl_1", Model: "gpt-5.5",
		Choices: []ChatChunkChoice{{Index: 0, Delta: ChatDelta{Content: &text}}},
	}, state)

	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	assert.Equal(t, []string{
		"response.created", "response.in_progress", "response.output_item.added",
		"response.content_part.added", "response.output_text.delta",
	}, types)
	require.NotNil(t, events[3].Part)
	assert.Equal(t, "output_text", events[3].Part.Type)
	assert.Equal(t, state.MessageItemID, events[4].ItemID)
	addedJSON, err := json.Marshal(events[2])
	require.NoError(t, err)
	assert.Contains(t, string(addedJSON), `"output_index":0`)
	assert.Contains(t, string(addedJSON), `"content":[]`)
	assert.Contains(t, string(addedJSON), `"phase":"final_answer"`)
	partAddedJSON, err := json.Marshal(events[3])
	require.NoError(t, err)
	assert.Contains(t, string(partAddedJSON), `"text":""`)
	deltaJSON, err := json.Marshal(events[4])
	require.NoError(t, err)
	assert.Contains(t, string(deltaJSON), `"output_index":0`)
	assert.Contains(t, string(deltaJSON), `"content_index":0`)

	terminal := FinalizeChatCompletionsResponsesStream(state)
	terminalTypes := make([]string, 0, len(terminal))
	for _, event := range terminal {
		terminalTypes = append(terminalTypes, event.Type)
	}
	assert.Equal(t, []string{
		"response.output_text.done", "response.content_part.done",
		"response.output_item.done", "response.completed",
	}, terminalTypes)
	doneJSON, err := json.Marshal(terminal[2])
	require.NoError(t, err)
	assert.Contains(t, string(doneJSON), `"content":[{"type":"output_text","text":"hello"}]`)
	assert.Contains(t, string(doneJSON), `"phase":"final_answer"`)
}

func TestChatCompletionsStream_ClosesFunctionCallWithoutEmptyFinalMessage(t *testing.T) {
	state := NewChatCompletionsToResponsesStreamState("gpt-5.5")
	arg := `{"command":"TOOL_OK"}`
	idx := 0
	events := ChatCompletionsChunkToResponsesEvents(&ChatCompletionsChunk{
		ID: "chatcmpl_tool", Model: "gpt-5.5",
		Choices: []ChatChunkChoice{{Index: 0, Delta: ChatDelta{ToolCalls: []ChatToolCall{{Index: &idx, ID: "call_1", Type: "function", Function: ChatFunctionCall{Name: "terminal", Arguments: arg}}}}}},
	}, state)

	require.Len(t, events, 4)
	assert.Equal(t, "response.output_item.added", events[2].Type)
	assert.Equal(t, 0, events[2].OutputIndex)
	assert.Equal(t, "function_call", events[2].Item.Type)
	assert.Equal(t, "response.function_call_arguments.delta", events[3].Type)

	terminal := FinalizeChatCompletionsResponsesStream(state)
	require.Len(t, terminal, 3)
	assert.Equal(t, "response.function_call_arguments.done", terminal[0].Type)
	assert.Equal(t, arg, terminal[0].Arguments)
	assert.Equal(t, 0, terminal[0].OutputIndex)
	assert.Equal(t, "response.output_item.done", terminal[1].Type)
	assert.Equal(t, arg, terminal[1].Item.Arguments)
	assert.Equal(t, "response.completed", terminal[2].Type)
	assert.Len(t, terminal[2].Response.Output, 1)
	assert.Equal(t, "function_call", terminal[2].Response.Output[0].Type)
	assert.Equal(t, events[2].Item.ID, terminal[1].Item.ID)
	assert.Equal(t, events[2].Item.ID, terminal[2].Response.Output[0].ID)
	assert.Equal(t, arg, terminal[2].Response.Output[0].Arguments)
}

func TestChatCompletionsStream_EmitsCustomToolInputLifecycle(t *testing.T) {
	state := NewChatCompletionsToResponsesStreamState("gpt-5.5")
	state.CustomToolNames["apply_patch"] = true
	idx := 0
	arg := `{"input":"*** Begin Patch"}`
	events := ChatCompletionsChunkToResponsesEvents(&ChatCompletionsChunk{
		Choices: []ChatChunkChoice{{Delta: ChatDelta{ToolCalls: []ChatToolCall{{Index: &idx, ID: "call_1", Function: ChatFunctionCall{Name: "apply_patch", Arguments: arg}}}}}},
	}, state)

	require.Len(t, events, 3)
	assert.Equal(t, "custom_tool_call", events[2].Item.Type)
	assert.Empty(t, events[2].Item.Input)
	addedJSON, err := json.Marshal(events[2])
	require.NoError(t, err)
	assert.Contains(t, string(addedJSON), `"input":""`)

	terminal := FinalizeChatCompletionsResponsesStream(state)
	require.Len(t, terminal, 4)
	assert.Equal(t, "response.custom_tool_call_input.delta", terminal[0].Type)
	assert.Equal(t, "*** Begin Patch", terminal[0].Delta)
	assert.Equal(t, "response.custom_tool_call_input.done", terminal[1].Type)
	assert.Equal(t, "*** Begin Patch", terminal[1].Input)
	assert.Equal(t, "response.output_item.done", terminal[2].Type)
	assert.Equal(t, "custom_tool_call", terminal[2].Item.Type)
	assert.Equal(t, "*** Begin Patch", terminal[2].Item.Input)
	assert.Empty(t, terminal[2].Item.Arguments)
	assert.Equal(t, "response.completed", terminal[3].Type)
	require.Len(t, terminal[3].Response.Output, 1)
	assert.Equal(t, "*** Begin Patch", terminal[3].Response.Output[0].Input)
}

func TestResponsesUsageUnmarshalAndBridgePreservesCacheWriteTokens(t *testing.T) {
	var usage ResponsesUsage
	require.NoError(t, json.Unmarshal([]byte(`{
		"input_tokens": 100,
		"output_tokens": 20,
		"input_tokens_details": {
			"cached_tokens": 30,
			"cache_write_tokens": 12
		}
	}`), &usage))

	require.Equal(t, 12, usage.CacheCreationInputTokens)

	chatResp := ResponsesToChatCompletions(&ResponsesResponse{
		ID:     "resp_1",
		Status: "completed",
		Usage:  &usage,
	}, "gpt-4o")
	chatUsage := chatResp.Usage
	require.NotNil(t, chatUsage.PromptTokensDetails)
	require.Equal(t, 30, chatUsage.PromptTokensDetails.CachedTokens)
	require.Equal(t, 12, chatUsage.PromptTokensDetails.CacheWriteTokens)

	roundTrip := ChatUsageToResponsesUsage(chatUsage)
	require.NotNil(t, roundTrip.InputTokensDetails)
	require.Equal(t, 12, roundTrip.CacheCreationInputTokens)
	require.Equal(t, 12, roundTrip.InputTokensDetails.CacheWriteTokens)
}

func chatMessageRoles(messages []ChatMessage) []string {
	roles := make([]string, 0, len(messages))
	for _, message := range messages {
		roles = append(roles, message.Role)
	}
	return roles
}
