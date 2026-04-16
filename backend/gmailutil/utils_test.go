package gmailutil_test

import (
	"testing"
	"time"

	"api/gmailutil"

	"google.golang.org/api/gmail/v1"
)

// ── HeaderValue ───────────────────────────────────────────────────────────────

func TestHeaderValue(t *testing.T) {
	headers := []*gmail.MessagePartHeader{
		{Name: "From", Value: "Alice <alice@example.com>"},
		{Name: "Subject", Value: "Hello"},
		{Name: "Date", Value: "Mon, 1 Jan 2024 12:00:00 +0000"},
	}

	tests := []struct {
		key  string
		want string
	}{
		{"From", "Alice <alice@example.com>"},
		{"from", "Alice <alice@example.com>"},   // case-insensitive
		{"FROM", "Alice <alice@example.com>"},   // case-insensitive
		{"Subject", "Hello"},
		{"Missing", ""},
	}

	for _, tc := range tests {
		got := gmailutil.HeaderValue(headers, tc.key)
		if got != tc.want {
			t.Errorf("HeaderValue(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

func TestHeaderValue_EmptyHeaders(t *testing.T) {
	got := gmailutil.HeaderValue(nil, "From")
	if got != "" {
		t.Errorf("expected empty string for nil headers, got %q", got)
	}
}

// ── ParseDate ─────────────────────────────────────────────────────────────────

func TestParseDate(t *testing.T) {
	tests := []struct {
		input   string
		wantNil bool
	}{
		{"Mon, 01 Jan 2024 12:00:00 +0000", false},
		{"1 Jan 2024 12:00:00 +0000", false},
		{"", true},
		{"not a date", true},
	}

	for _, tc := range tests {
		got := gmailutil.ParseDate(tc.input)
		if tc.wantNil && got != nil {
			t.Errorf("ParseDate(%q) = %v, want nil", tc.input, got)
		}
		if !tc.wantNil && got == nil {
			t.Errorf("ParseDate(%q) = nil, want non-nil", tc.input)
		}
	}
}

func TestParseDate_CorrectTime(t *testing.T) {
	got := gmailutil.ParseDate("Mon, 01 Jan 2024 12:30:00 +0000")
	if got == nil {
		t.Fatal("expected non-nil time")
	}
	if got.Year() != 2024 || got.Month() != time.January || got.Day() != 1 {
		t.Errorf("unexpected parsed time: %v", *got)
	}
}

// ── ParseFromHeader ───────────────────────────────────────────────────────────

func TestParseFromHeader(t *testing.T) {
	tests := []struct {
		input       string
		wantName    string
		wantEmail   string
	}{
		{`"Alice Smith" <alice@example.com>`, "Alice Smith", "alice@example.com"},
		{`Alice Smith <alice@example.com>`, "Alice Smith", "alice@example.com"},
		{`<alice@example.com>`, "", "alice@example.com"},
		{`alice@example.com`, "", "alice@example.com"},
		{`ALICE@EXAMPLE.COM`, "", "alice@example.com"}, // email lowercased
		{``, "", ""},
	}

	for _, tc := range tests {
		gotName, gotEmail := gmailutil.ParseFromHeader(tc.input)
		if gotName != tc.wantName {
			t.Errorf("ParseFromHeader(%q) name = %q, want %q", tc.input, gotName, tc.wantName)
		}
		if gotEmail != tc.wantEmail {
			t.Errorf("ParseFromHeader(%q) email = %q, want %q", tc.input, gotEmail, tc.wantEmail)
		}
	}
}

// ── ExtractPlainBody ──────────────────────────────────────────────────────────

func textPlainPart(b64data string) *gmail.MessagePart {
	return &gmail.MessagePart{
		MimeType: "text/plain",
		Body:     &gmail.MessagePartBody{Data: b64data},
	}
}

func TestExtractPlainBody(t *testing.T) {
	// "Hello World" base64url-encoded (no padding)
	// echo -n "Hello World" | base64 | tr '+/' '-_' | tr -d '='
	const encoded = "SGVsbG8gV29ybGQ"

	part := textPlainPart(encoded)
	got := gmailutil.ExtractPlainBody(part)
	if got != "Hello World" {
		t.Errorf("ExtractPlainBody = %q, want %q", got, "Hello World")
	}
}

func TestExtractPlainBody_Nested(t *testing.T) {
	const encoded = "SGVsbG8gV29ybGQ"
	multipart := &gmail.MessagePart{
		MimeType: "multipart/mixed",
		Parts: []*gmail.MessagePart{
			{MimeType: "text/html", Body: &gmail.MessagePartBody{Data: "aHRtbA"}},
			textPlainPart(encoded),
		},
	}
	got := gmailutil.ExtractPlainBody(multipart)
	if got != "Hello World" {
		t.Errorf("ExtractPlainBody nested = %q, want %q", got, "Hello World")
	}
}

func TestExtractPlainBody_Nil(t *testing.T) {
	if got := gmailutil.ExtractPlainBody(nil); got != "" {
		t.Errorf("expected empty for nil payload, got %q", got)
	}
}

// ── ExtractHTMLBody ───────────────────────────────────────────────────────────

func TestExtractHTMLBody(t *testing.T) {
	// "<b>Hi</b>" base64url-encoded
	// echo -n "<b>Hi</b>" | base64 | tr '+/' '-_' | tr -d '='
	const encoded = "PGI-SGk8L2I-"

	part := &gmail.MessagePart{
		MimeType: "text/html",
		Body:     &gmail.MessagePartBody{Data: encoded},
	}
	got := gmailutil.ExtractHTMLBody(part)
	if got != "<b>Hi</b>" {
		t.Errorf("ExtractHTMLBody = %q, want %q", got, "<b>Hi</b>")
	}
}

func TestExtractHTMLBody_Nil(t *testing.T) {
	if got := gmailutil.ExtractHTMLBody(nil); got != "" {
		t.Errorf("expected empty for nil payload, got %q", got)
	}
}

// ── ParseListUnsubscribe ──────────────────────────────────────────────────────

func TestParseListUnsubscribe(t *testing.T) {
	tests := []struct {
		header      string
		wantURL     string
		wantMailto  string
	}{
		{
			"<https://example.com/unsub>, <mailto:unsub@example.com>",
			"https://example.com/unsub",
			"mailto:unsub@example.com",
		},
		{
			"<mailto:unsub@example.com>",
			"",
			"mailto:unsub@example.com",
		},
		{
			"<https://example.com/unsub>",
			"https://example.com/unsub",
			"",
		},
		{"", "", ""},
	}

	for _, tc := range tests {
		gotURL, gotMailto := gmailutil.ParseListUnsubscribe(tc.header)
		if gotURL != tc.wantURL {
			t.Errorf("ParseListUnsubscribe(%q) url = %q, want %q", tc.header, gotURL, tc.wantURL)
		}
		if gotMailto != tc.wantMailto {
			t.Errorf("ParseListUnsubscribe(%q) mailto = %q, want %q", tc.header, gotMailto, tc.wantMailto)
		}
	}
}

// ── WithRetry ─────────────────────────────────────────────────────────────────

func TestWithRetry_SuccessFirstAttempt(t *testing.T) {
	calls := 0
	result, err := gmailutil.WithRetry(func() (string, error) {
		calls++
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected %q, got %q", "ok", result)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestWithRetry_NonRetryableErrorNoRetry(t *testing.T) {
	calls := 0
	_, err := gmailutil.WithRetry(func() (string, error) {
		calls++
		return "", &mockNonRetryableError{}
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 call for non-retryable error, got %d", calls)
	}
}

// mockNonRetryableError is a plain error that IsRetryable returns false for.
type mockNonRetryableError struct{}

func (e *mockNonRetryableError) Error() string { return "non-retryable" }
