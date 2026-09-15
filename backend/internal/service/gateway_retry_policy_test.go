package service

import (
	"net/http"
	"testing"
	"time"
)

func TestRetryAfterFromHeaders(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{name: "delta seconds", header: "3", want: 3 * time.Second},
		{name: "clamped delta", header: "60", want: 10 * time.Second},
		{name: "invalid", header: "nope", want: 0},
		{name: "negative", header: "-1", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			headers.Set("Retry-After", tt.header)
			if got := RetryAfterFromHeaders(headers, now); got != tt.want {
				t.Fatalf("RetryAfterFromHeaders() = %s, want %s", got, tt.want)
			}
		})
	}

	headers := make(http.Header)
	headers.Set("Retry-After", now.Add(4*time.Second).Format(http.TimeFormat))
	if got := RetryAfterFromHeaders(headers, now); got != 4*time.Second {
		t.Fatalf("HTTP-date RetryAfterFromHeaders() = %s, want 4s", got)
	}
}
