package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// ForwardAsResponses accepts an OpenAI Responses API request body, converts it
// to Anthropic Messages format, forwards to the Anthropic upstream, and converts
// the response back to Responses format. This enables OpenAI Responses API
// clients to access Anthropic models through Anthropic platform groups.
//
// The method follows the same pattern as OpenAIGatewayService.ForwardAsAnthropic
// but in reverse direction: Responses → Anthropic upstream → Responses.
func (s *GatewayService) ForwardAsResponses(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	parsed *ParsedRequest,
) (*ForwardResult, error) {
	startTime := time.Now()

	// 1. Parse Responses request
	var responsesReq apicompat.ResponsesRequest
	if err := json.Unmarshal(body, &responsesReq); err != nil {
		return nil, fmt.Errorf("parse responses request: %w", err)
	}
	originalModel := responsesReq.Model
	clientStream := responsesReq.Stream

	// 2. Convert Responses → Anthropic
	anthropicReq, err := apicompat.ResponsesToAnthropicRequest(&responsesReq)
	if err != nil {
		return nil, fmt.Errorf("convert responses to anthropic: %w", err)
	}

	// 3. Force upstream streaming (Anthropic works best with streaming)
	anthropicReq.Stream = true
	reqStream := true

	// 4. Model mapping
	mappedModel := originalModel
	reasoningEffort := ExtractResponsesReasoningEffortFromBody(body)
	if account.Type == AccountTypeAPIKey || account.Type == AccountTypeServiceAccount {
		mappedModel = account.GetMappedModel(originalModel)
	}
	if mappedModel == originalModel && account.Platform == PlatformAnthropic && account.Type == AccountTypeServiceAccount {
		normalized := normalizeVertexAnthropicModelID(claude.NormalizeModelID(originalModel))
		if normalized != originalModel {
			mappedModel = normalized
		}
	} else if mappedModel == originalModel && account.Platform == PlatformAnthropic && account.Type != AccountTypeAPIKey {
		normalized := claude.NormalizeModelID(originalModel)
		if normalized != originalModel {
			mappedModel = normalized
		}
	}
	anthropicReq.Model = mappedModel

	logger.L().Debug("gateway forward_as_responses: model mapping applied",
		zap.Int64("account_id", account.ID),
		zap.String("original_model", originalModel),
		zap.String("mapped_model", mappedModel),
		zap.Bool("client_stream", clientStream),
	)

	// 5. Marshal Anthropic request body
	anthropicBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	// 6. Apply Claude Code mimicry for OAuth accounts (non-Claude-Code endpoints).
	// OpenAI Responses 协议进来的请求永远不是 Claude Code 客户端，所以对 OAuth 账号
	// 必须完整执行 /v1/messages 主路径上的伪装链路（system 重写 + normalize + metadata 注入），
	// 否则会被 Anthropic 判为第三方应用并扣 extra usage。
	// 见 applyClaudeCodeOAuthMimicryToBody 的 godoc。
	isClaudeCode := false
	shouldMimicClaudeCode := account.IsOAuth() && !isClaudeCode

	if shouldMimicClaudeCode {
		anthropicBody = s.applyClaudeCodeOAuthMimicryToBody(ctx, c, account, anthropicBody, anthropicReq.System, mappedModel)
	}

	// 7. Enforce cache_control block limit
	anthropicBody = enforceCacheControlLimit(anthropicBody)

	// 8. Get access token
	token, tokenType, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}

	// 9. Get proxy URL
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	// 10. Build upstream request
	upstreamCtx, releaseUpstreamCtx := detachStreamUpstreamContext(ctx, reqStream)
	upstreamReq, err := s.buildUpstreamRequest(upstreamCtx, c, account, anthropicBody, token, tokenType, mappedModel, reqStream, shouldMimicClaudeCode)
	releaseUpstreamCtx()
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}

	// 11. Send request
	upstreamStart := time.Now()
	resp, err := s.httpUpstream.DoWithTLS(upstreamReq, proxyURL, account.ID, account.Concurrency, s.tlsFPProfileService.ResolveTLSProfile(account))
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(upstreamStart).Milliseconds())
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		setOpsUpstreamError(c, 0, safeErr, "")
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: 0,
			Kind:               "request_error",
			Message:            safeErr,
		})
		return nil, &UpstreamFailoverError{
			StatusCode:             http.StatusBadGateway,
			ResponseBody:           []byte(safeErr),
			RetryableOnSameAccount: true,
		}
	}
	defer func() { _ = resp.Body.Close() }()

	// 12. Handle error response with failover
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))

		upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
		upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)

		if shouldFailoverAnthropicCompatResponse(resp.StatusCode, respBody) {
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  resp.Header.Get("x-request-id"),
				Kind:               "failover",
				Message:            upstreamMsg,
			})
			if s.rateLimitService != nil {
				s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
			}
			return nil, &UpstreamFailoverError{
				StatusCode:   anthropicCompatFailoverStatus(resp.StatusCode, respBody),
				ResponseBody: respBody,
			}
		}

		// Non-failover error: return Responses-formatted error to client
		writeResponsesError(c, mapUpstreamStatusCode(resp.StatusCode), "server_error", upstreamMsg)
		return nil, fmt.Errorf("upstream error: %d %s", resp.StatusCode, upstreamMsg)
	}

	// 13. Handle normal response (convert Anthropic → Responses)
	var result *ForwardResult
	var handleErr error
	if clientStream {
		result, handleErr = s.handleResponsesStreamingResponse(resp, c, account, originalModel, mappedModel, reasoningEffort, startTime)
	} else {
		result, handleErr = s.handleResponsesBufferedStreamingResponse(resp, c, account, originalModel, mappedModel, reasoningEffort, startTime)
	}

	return result, handleErr
}

// ExtractResponsesReasoningEffortFromBody reads Responses API reasoning.effort
// and normalizes it for usage logging.
func ExtractResponsesReasoningEffortFromBody(body []byte) *string {
	raw := strings.TrimSpace(gjson.GetBytes(body, "reasoning.effort").String())
	if raw == "" {
		return nil
	}
	normalized := normalizeOpenAIReasoningEffort(raw)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func mergeAnthropicUsage(dst *ClaudeUsage, src apicompat.AnthropicUsage) {
	if dst == nil {
		return
	}
	if src.InputTokens > 0 {
		dst.InputTokens = src.InputTokens
	}
	if src.OutputTokens > 0 {
		dst.OutputTokens = src.OutputTokens
	}
	if src.CacheReadInputTokens > 0 {
		dst.CacheReadInputTokens = src.CacheReadInputTokens
	}
	if src.CacheCreationInputTokens > 0 {
		dst.CacheCreationInputTokens = src.CacheCreationInputTokens
	}
}

// handleResponsesBufferedStreamingResponse reads all Anthropic SSE events from
// the upstream streaming response, assembles them into a complete Anthropic
// response, converts to Responses API JSON format, and writes it to the client.
func (s *GatewayService) handleResponsesBufferedStreamingResponse(
	resp *http.Response,
	c *gin.Context,
	account *Account,
	originalModel string,
	mappedModel string,
	reasoningEffort *string,
	startTime time.Time,
) (*ForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	// Accumulate the final Anthropic response from streaming events
	var finalResp *apicompat.AnthropicResponse
	var usage ClaudeUsage
	sawTerminal := false

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "event: ") {
			continue
		}
		eventType := strings.TrimPrefix(line, "event: ")

		// Read the data line
		if !scanner.Scan() {
			break
		}
		dataLine := scanner.Text()
		if !strings.HasPrefix(dataLine, "data: ") {
			continue
		}
		payload := dataLine[6:]

		var event apicompat.AnthropicStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			logger.L().Warn("forward_as_responses buffered: failed to parse event",
				zap.Error(err),
				zap.String("request_id", requestID),
				zap.String("event_type", eventType),
			)
			continue
		}
		if event.Type == "" {
			event.Type = eventType
		}
		if event.Type == "error" {
			return nil, s.anthropicResponsesStreamError(c, account, resp, &event, []byte(payload), false)
		}

		// message_start carries the initial response structure
		if event.Type == "message_start" && event.Message != nil {
			finalResp = event.Message
			mergeAnthropicUsage(&usage, event.Message.Usage)
		}

		// message_delta carries final usage and stop_reason
		if event.Type == "message_delta" {
			if event.Usage != nil {
				mergeAnthropicUsage(&usage, *event.Usage)
			}
			if event.Delta != nil && event.Delta.StopReason != "" && finalResp != nil {
				finalResp.StopReason = event.Delta.StopReason
			}
		}
		if event.Type == "message_stop" {
			sawTerminal = true
			break
		}

		// Accumulate content blocks
		if event.Type == "content_block_start" && event.ContentBlock != nil && finalResp != nil {
			finalResp.Content = append(finalResp.Content, *event.ContentBlock)
		}
		if event.Type == "content_block_delta" && event.Delta != nil && finalResp != nil && event.Index != nil {
			idx := *event.Index
			if idx < len(finalResp.Content) {
				switch event.Delta.Type {
				case "text_delta":
					finalResp.Content[idx].Text += event.Delta.Text
				case "thinking_delta":
					finalResp.Content[idx].Thinking += event.Delta.Thinking
				case "input_json_delta":
					finalResp.Content[idx].Input = appendRawJSON(finalResp.Content[idx].Input, event.Delta.PartialJSON)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			logger.L().Warn("forward_as_responses buffered: read error",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
		}
	}

	if finalResp == nil || !sawTerminal {
		return nil, newAnthropicCompatStreamDisconnectError(requestID)
	}

	// Update usage from accumulated delta
	if usage.InputTokens > 0 || usage.OutputTokens > 0 {
		finalResp.Usage = apicompat.AnthropicUsage{
			InputTokens:              usage.InputTokens,
			OutputTokens:             usage.OutputTokens,
			CacheCreationInputTokens: usage.CacheCreationInputTokens,
			CacheReadInputTokens:     usage.CacheReadInputTokens,
		}
	}

	// Convert to Responses format
	responsesResp := apicompat.AnthropicToResponsesResponse(finalResp)
	responsesResp.Model = originalModel // Use original model name

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	if respBytes, err := json.Marshal(responsesResp); err == nil {
		respBytes = reverseToolNamesIfPresent(c, respBytes)
		c.Data(http.StatusOK, "application/json; charset=utf-8", respBytes)
	} else {
		c.JSON(http.StatusOK, responsesResp)
	}

	return &ForwardResult{
		RequestID:       requestID,
		Usage:           usage,
		Model:           originalModel,
		UpstreamModel:   mappedModel,
		ReasoningEffort: reasoningEffort,
		Stream:          false,
		Duration:        time.Since(startTime),
	}, nil
}

// handleResponsesStreamingResponse reads Anthropic SSE events from upstream,
// converts each to Responses SSE events, and writes them to the client.
func (s *GatewayService) handleResponsesStreamingResponse(
	resp *http.Response,
	c *gin.Context,
	account *Account,
	originalModel string,
	mappedModel string,
	reasoningEffort *string,
	startTime time.Time,
) (*ForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	state := apicompat.NewAnthropicEventToResponsesState()
	state.Model = originalModel
	var usage ClaudeUsage
	var firstTokenMs *int
	responseStarted := false
	sawTerminal := false

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	resultWithUsage := func() *ForwardResult {
		return &ForwardResult{
			RequestID:       requestID,
			Usage:           usage,
			Model:           originalModel,
			UpstreamModel:   mappedModel,
			ReasoningEffort: reasoningEffort,
			Stream:          true,
			Duration:        time.Since(startTime),
			FirstTokenMs:    firstTokenMs,
		}
	}

	startResponse := func() {
		if responseStarted {
			return
		}
		responseStarted = true
		c.Writer.WriteHeader(http.StatusOK)
	}

	writeFailedEvent := func(errType, message string) {
		startResponse()
		evt := apicompat.AnthropicErrorToResponsesEvent(errType, message, state)
		if sse, err := apicompat.ResponsesEventToSSE(evt); err == nil {
			out := string(reverseToolNamesIfPresent(c, []byte(sse)))
			_, _ = fmt.Fprint(c.Writer, out)
			c.Writer.Flush()
		}
	}

	// processEvent handles a single parsed Anthropic SSE event.
	processEvent := func(event *apicompat.AnthropicStreamEvent) bool {
		// Extract usage from message_delta
		if event.Type == "message_delta" && event.Usage != nil {
			mergeAnthropicUsage(&usage, *event.Usage)
		}
		// Also capture usage from message_start
		if event.Type == "message_start" && event.Message != nil {
			mergeAnthropicUsage(&usage, event.Message.Usage)
		}

		// Convert to Responses events
		events := apicompat.AnthropicEventToResponsesEvents(event, state)
		if len(events) > 0 {
			startResponse()
			if firstTokenMs == nil {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
			}
		}
		for _, evt := range events {
			sse, err := apicompat.ResponsesEventToSSE(evt)
			if err != nil {
				logger.L().Warn("forward_as_responses stream: failed to marshal event",
					zap.Error(err),
					zap.String("request_id", requestID),
				)
				continue
			}
			out := string(reverseToolNamesIfPresent(c, []byte(sse)))
			if _, err := fmt.Fprint(c.Writer, out); err != nil {
				logger.L().Info("forward_as_responses stream: client disconnected",
					zap.String("request_id", requestID),
				)
				return true // client disconnected
			}
		}
		if len(events) > 0 {
			c.Writer.Flush()
		}
		if event.Type == "message_stop" {
			sawTerminal = true
		}
		return sawTerminal
	}

	// Read Anthropic SSE events
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "event: ") {
			continue
		}
		eventType := strings.TrimPrefix(line, "event: ")

		// Read data line
		if !scanner.Scan() {
			break
		}
		dataLine := scanner.Text()
		if !strings.HasPrefix(dataLine, "data: ") {
			continue
		}
		payload := dataLine[6:]

		var event apicompat.AnthropicStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			logger.L().Warn("forward_as_responses stream: failed to parse event",
				zap.Error(err),
				zap.String("request_id", requestID),
				zap.String("event_type", eventType),
			)
			continue
		}
		if event.Type == "" {
			event.Type = eventType
		}
		if event.Type == "error" {
			err := s.anthropicResponsesStreamError(c, account, resp, &event, []byte(payload), responseStarted)
			if responseStarted {
				writeFailedEvent(anthropicCompatStreamErrorType(&event), anthropicCompatStreamErrorMessage(&event))
				return resultWithUsage(), err
			}
			return nil, err
		}

		if processEvent(&event) {
			return resultWithUsage(), nil
		}
	}

	if err := scanner.Err(); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			logger.L().Warn("forward_as_responses stream: read error",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
		}
	}

	if !sawTerminal {
		err := newAnthropicCompatStreamDisconnectError(requestID)
		if !responseStarted {
			return nil, err
		}
		s.TempUnscheduleRetryableError(c.Request.Context(), account.ID, err)
		writeFailedEvent("stream_read_error", "Upstream stream ended before completion")
		return resultWithUsage(), err
	}

	return resultWithUsage(), nil
}

func anthropicCompatStreamErrorType(event *apicompat.AnthropicStreamEvent) string {
	if event == nil || event.Error == nil {
		return "upstream_error"
	}
	if value := strings.TrimSpace(event.Error.Type); value != "" {
		return value
	}
	if value := strings.TrimSpace(event.Error.Code); value != "" {
		return value
	}
	return "upstream_error"
}

func anthropicCompatStreamErrorMessage(event *apicompat.AnthropicStreamEvent) string {
	if event == nil || event.Error == nil {
		return "Upstream stream failed"
	}
	message := strings.TrimSpace(event.Error.Message)
	if message == "" {
		return "Upstream stream failed"
	}
	return sanitizeUpstreamErrorMessage(message)
}

func anthropicCompatStreamErrorStatus(event *apicompat.AnthropicStreamEvent) int {
	switch strings.ToLower(anthropicCompatStreamErrorType(event)) {
	case "rate_limit_error", "rate_limit_exceeded":
		return http.StatusTooManyRequests
	case "authentication_error", "unauthorized":
		return http.StatusUnauthorized
	case "permission_error", "forbidden":
		return http.StatusForbidden
	case "invalid_request_error", "bad_request":
		return http.StatusBadRequest
	case "overloaded_error", "service_unavailable", "upstream_unavailable", "api_error":
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}

func (s *GatewayService) anthropicResponsesStreamError(
	c *gin.Context,
	account *Account,
	resp *http.Response,
	event *apicompat.AnthropicStreamEvent,
	payload []byte,
	responseStarted bool,
) error {
	statusCode := anthropicCompatStreamErrorStatus(event)
	message := anthropicCompatStreamErrorMessage(event)
	setOpsUpstreamError(c, statusCode, message, truncateString(string(payload), 4096))
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: statusCode,
		UpstreamRequestID:  resp.Header.Get("x-request-id"),
		Kind:               "stream_error",
		Message:            message,
		Detail:             truncateString(string(payload), 4096),
	})
	if s.rateLimitService != nil {
		s.rateLimitService.HandleUpstreamError(c.Request.Context(), account, statusCode, resp.Header, payload)
	}

	if s.shouldFailoverUpstreamError(statusCode) {
		failoverErr := &UpstreamFailoverError{
			StatusCode:      statusCode,
			ResponseBody:    payload,
			ResponseHeaders: resp.Header.Clone(),
			RetryAfter:      RetryAfterFromHeaders(resp.Header, time.Now()),
		}
		if responseStarted {
			s.TempUnscheduleRetryableError(c.Request.Context(), account.ID, failoverErr)
		}
		return failoverErr
	}
	return fmt.Errorf("anthropic upstream stream error: %s", message)
}

func newAnthropicCompatStreamDisconnectError(requestID string) *UpstreamFailoverError {
	body, _ := json.Marshal(gin.H{
		"error": gin.H{
			"type":    "stream_read_error",
			"message": "Upstream stream ended before completion",
		},
		"request_id": requestID,
	})
	return &UpstreamFailoverError{
		StatusCode:             http.StatusBadGateway,
		ResponseBody:           body,
		RetryableOnSameAccount: true,
	}
}

func shouldFailoverAnthropicCompatResponse(statusCode int, body []byte) bool {
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden ||
		statusCode == http.StatusTooManyRequests || statusCode == 529 || statusCode >= 500 {
		return true
	}
	return isRetryableAnthropicCompatErrorBody(body)
}

func anthropicCompatFailoverStatus(statusCode int, body []byte) int {
	if statusCode >= 500 || statusCode == http.StatusTooManyRequests || statusCode == 529 {
		return statusCode
	}
	if isRetryableAnthropicCompatErrorBody(body) {
		return http.StatusServiceUnavailable
	}
	return statusCode
}

func isRetryableAnthropicCompatErrorBody(body []byte) bool {
	lower := strings.ToLower(string(body))
	for _, marker := range []string{
		"service_unavailable",
		"upstream_unavailable",
		"overloaded_error",
		"temporarily unavailable",
		"stream_read_error",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// appendRawJSON appends a JSON fragment string to existing raw JSON.
func appendRawJSON(existing json.RawMessage, fragment string) json.RawMessage {
	if len(existing) == 0 {
		return json.RawMessage(fragment)
	}
	return json.RawMessage(string(existing) + fragment)
}

// writeResponsesError writes an error response in OpenAI Responses API format.
func writeResponsesError(c *gin.Context, statusCode int, code, message string) {
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"code":    code,
			"message": message,
		},
	})
}

// mapUpstreamStatusCode maps upstream HTTP status codes to appropriate client-facing codes.
func mapUpstreamStatusCode(code int) int {
	if code >= 500 {
		return http.StatusBadGateway
	}
	return code
}
