// Package gmailutil provides pure helper functions for working with Gmail API
// payloads. It has no dependency on the local database or HTTP layer.
package gmailutil

import (
	"encoding/base64"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
)

const (
	RetryMaxAttempts = 8
	RetryBaseDelay   = 500 * time.Millisecond
	RetryMaxDelay    = 15 * time.Second
)

// WithRetry runs operation with exponential back-off on transient Gmail errors.
func WithRetry[T any](operation func() (T, error)) (T, error) {
	var zero T
	var lastErr error

	for attempt := 0; attempt < RetryMaxAttempts; attempt++ {
		result, err := operation()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !IsRetryable(err) || attempt == RetryMaxAttempts-1 {
			break
		}
		time.Sleep(RetryDelay(err, attempt))
	}
	return zero, lastErr
}

// IsRetryable returns true for transient Gmail API errors worth retrying.
func IsRetryable(err error) bool {
	var gErr *googleapi.Error
	if gErr = toGoogleAPIError(err); gErr == nil {
		return false
	}
	switch gErr.Code {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// RetryDelay returns how long to wait before the next attempt.
func RetryDelay(err error, attempt int) time.Duration {
	if gErr := toGoogleAPIError(err); gErr != nil && gErr.Header != nil {
		if ra := gErr.Header.Get("Retry-After"); ra != "" {
			if d, parseErr := time.ParseDuration(ra + "s"); parseErr == nil && d > 0 {
				if d > RetryMaxDelay {
					return RetryMaxDelay
				}
				return d
			}
		}
	}
	delay := RetryBaseDelay * time.Duration(1<<attempt)
	if delay > RetryMaxDelay {
		return RetryMaxDelay
	}
	return delay
}

func toGoogleAPIError(err error) *googleapi.Error {
	var gErr *googleapi.Error
	if ok := isGoogleAPIError(err, &gErr); ok {
		return gErr
	}
	return nil
}

func isGoogleAPIError(err error, target **googleapi.Error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*googleapi.Error); ok {
		*target = e
		return true
	}
	// Try unwrapping.
	type unwrapper interface{ Unwrap() error }
	if u, ok := err.(unwrapper); ok {
		return isGoogleAPIError(u.Unwrap(), target)
	}
	return false
}

// HeaderValue finds a header value (case-insensitive) in a Gmail message payload.
func HeaderValue(headers []*gmail.MessagePartHeader, key string) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, key) {
			return h.Value
		}
	}
	return ""
}

// ParseDate parses a Gmail Date header value.
func ParseDate(v string) *time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	parsed, err := mail.ParseDate(v)
	if err != nil {
		return nil
	}
	return &parsed
}

var fromRegex = regexp.MustCompile(`(?i)^(.*)<([^>]+)>$`)

// ParseFromHeader splits a "From" header into display name and email address.
func ParseFromHeader(from string) (string, string) {
	from = strings.TrimSpace(from)
	if from == "" {
		return "", ""
	}
	matches := fromRegex.FindStringSubmatch(from)
	if len(matches) == 3 {
		name := strings.Trim(strings.TrimSpace(matches[1]), `"`)
		email := strings.ToLower(strings.TrimSpace(matches[2]))
		return name, email
	}
	return "", strings.ToLower(from)
}

// ExtractPlainBody walks a Gmail message payload and returns the first text/plain body.
func ExtractPlainBody(payload *gmail.MessagePart) string {
	if payload == nil {
		return ""
	}
	if strings.HasPrefix(payload.MimeType, "text/plain") && payload.Body != nil && payload.Body.Data != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(payload.Body.Data)
		if err == nil {
			return string(decoded)
		}
	}
	for _, part := range payload.Parts {
		if body := ExtractPlainBody(part); body != "" {
			return body
		}
	}
	return ""
}

// ExtractHTMLBody walks a Gmail message payload and returns the first text/html body.
func ExtractHTMLBody(payload *gmail.MessagePart) string {
	if payload == nil {
		return ""
	}
	if strings.HasPrefix(payload.MimeType, "text/html") && payload.Body != nil && payload.Body.Data != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(payload.Body.Data)
		if err == nil {
			return string(decoded)
		}
	}
	for _, part := range payload.Parts {
		if body := ExtractHTMLBody(part); body != "" {
			return body
		}
	}
	return ""
}

// ParseListUnsubscribe extracts an HTTP URL and/or mailto from a List-Unsubscribe header.
func ParseListUnsubscribe(header string) (url string, mailto string) {
	parts := strings.Split(header, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "<") && strings.HasSuffix(p, ">") {
			inner := p[1 : len(p)-1]
			if strings.HasPrefix(strings.ToLower(inner), "mailto:") {
				mailto = inner
			} else if strings.HasPrefix(strings.ToLower(inner), "http") {
				url = inner
			}
		}
	}
	return
}
