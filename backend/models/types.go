package models

import (
	"database/sql"
	"time"
)

type SyncStatus struct {
	Running      bool       `json:"running"`
	Scanned      int        `json:"scanned"`
	Checked      int64      `json:"checked"`
	PendingTotal int64      `json:"pendingTotal"`
	Total        int64      `json:"total"`
	Inserted     int        `json:"inserted"`
	Failed       int        `json:"failed"`
	ConnectedAs  string     `json:"connectedAs,omitempty"`
	LastError    string     `json:"lastError,omitempty"`
	StartedAt    time.Time  `json:"startedAt,omitempty"`
	FinishedAt   time.Time  `json:"finishedAt,omitempty"`
	NextSyncAt   *time.Time `json:"nextSyncAt,omitempty"`
}

type InboxStats struct {
	TotalEmails    int   `json:"totalEmails"`
	TotalSenders   int   `json:"totalSenders"`
	ConnectedApps  int   `json:"connectedAccounts"`
	TotalSizeBytes int64 `json:"totalSizeBytes"`
}

type SenderSummary struct {
	ID             int        `json:"id"`
	Email          string     `json:"email"`
	DisplayName    string     `json:"displayName"`
	Domain         string     `json:"domain"`
	Category       string     `json:"category"`
	KeepScore      int        `json:"keepScore"`
	HasInbox       bool       `json:"hasInbox"`
	EmailCount     int        `json:"emailCount"`
	ThreadCount    int        `json:"threadCount"`
	TotalSizeBytes int64      `json:"totalSizeBytes"`
	CanUnsubscribe bool       `json:"canUnsubscribe"`
	UnsubscribedAt *time.Time `json:"unsubscribedAt"`
	BlockedAt      *time.Time `json:"blockedAt"`
	LastReceivedAt *time.Time `json:"lastReceivedAt"`
}

type SenderEmail struct {
	ID             int        `json:"id"`
	GmailMessageID string     `json:"gmailMessageId"`
	GmailThreadID  string     `json:"gmailThreadId"`
	Subject        string     `json:"subject"`
	Snippet        string     `json:"snippet"`
	BodyText       string     `json:"bodyText"`
	BodyHTML       string     `json:"bodyHtml"`
	ReceivedAt     *time.Time `json:"receivedAt"`
	LabelIDs       string     `json:"labelIds"`
}

type PaginatedSenderEmails struct {
	Data  []SenderEmail `json:"data"`
	Total int           `json:"total"`
	Page  int           `json:"page"`
	Limit int           `json:"limit"`
}

type GetSendersParams struct {
	AccountID int
	Search    string
	SortCol   string
	SortOrder string
	Labels    []string
}

type SenderDomainSummary struct {
	Domain         string     `json:"domain"`
	SenderCount    int        `json:"senderCount"`
	EmailCount     int        `json:"emailCount"`
	TotalSizeBytes int64      `json:"totalSizeBytes"`
	LastReceivedAt *time.Time `json:"lastReceivedAt"`
	HasInbox       bool       `json:"hasInbox"`
	Category       string     `json:"category"`
	KeepScore      int        `json:"keepScore"`
	SenderEmails   []string   `json:"senderEmails"`
}

type Account struct {
	ID        int       `json:"id"`
	Email     string    `json:"email"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type SenderExportRow struct {
	Email          string
	DisplayName    string
	EmailCount     int
	ThreadCount    int
	TotalSizeBytes int64
	LastReceivedAt sql.NullTime
	UnsubscribedAt sql.NullTime
}

type TopSenderAnalytic struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type EmailTimelinePoint struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
}

type LabelCount struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}
