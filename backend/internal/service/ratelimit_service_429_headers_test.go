//go:build unit

package service

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCalculateOpenAI429ResetTime_StandardHeaders(t *testing.T) {
	t.Run("Retry-After秒数", func(t *testing.T) {
		before := time.Now()
		headers := make(http.Header)
		headers.Set("Retry-After", "30")
		resetAt := calculateOpenAI429ResetTime(headers)
		require.NotNil(t, resetAt)
		require.WithinDuration(t, before.Add(30*time.Second), *resetAt, 2*time.Second)
	})

	t.Run("Retry-After HTTP日期", func(t *testing.T) {
		expected := time.Now().UTC().Add(45 * time.Second).Truncate(time.Second)
		headers := make(http.Header)
		headers.Set("Retry-After", expected.Format(http.TimeFormat))
		resetAt := calculateOpenAI429ResetTime(headers)
		require.NotNil(t, resetAt)
		require.WithinDuration(t, expected, *resetAt, time.Second)
	})

	t.Run("RateLimit-Reset Unix时间", func(t *testing.T) {
		expected := time.Now().Add(60 * time.Second).Truncate(time.Second)
		headers := make(http.Header)
		headers.Set("RateLimit-Reset", strconv.FormatInt(expected.Unix(), 10))
		resetAt := calculateOpenAI429ResetTime(headers)
		require.NotNil(t, resetAt)
		require.WithinDuration(t, expected, *resetAt, time.Second)
	})

	t.Run("RateLimit-Reset相对秒数", func(t *testing.T) {
		before := time.Now()
		headers := make(http.Header)
		headers.Set("RateLimit-Reset", "20")
		resetAt := calculateOpenAI429ResetTime(headers)
		require.NotNil(t, resetAt)
		require.WithinDuration(t, before.Add(20*time.Second), *resetAt, 2*time.Second)
	})
}
