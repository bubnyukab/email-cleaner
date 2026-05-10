package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"api/classify"
	"api/models"

	"github.com/lib/pq"
	"golang.org/x/oauth2"
)

// dbQuerier is satisfied by both *sql.DB and *sql.Tx.
type dbQuerier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Store is the data access interface. All SQL lives here except sync writes (see #3).
type Store interface {
	// Accounts
	UpsertAccount(email, accessToken, refreshToken string, expiry time.Time) error
	GetAccountToken(email string) (string, *oauth2.Token, error)
	ResolveAccountID(email string) (int, error)
	ListAccounts() ([]models.Account, error)

	// Stats
	GetInboxStats(accountID int) (models.InboxStats, error)

	// Senders
	GetSenders(p models.GetSendersParams) ([]models.SenderSummary, error)
	GetSendersByDomain(accountID int) ([]models.SenderDomainSummary, error)
	GetDistinctLabels(accountID int) ([]string, error)
	GetSenderUnsubInfo(senderID string) (unsubURL, unsubMailto sql.NullString, unsubscribedAt sql.NullTime, err error)
	MarkUnsubscribed(senderID string) error
	GetSenderBlockInfo(senderID string) (email string, blockedAt sql.NullTime, err error)
	MarkBlocked(senderID string) error

	// Emails
	GetSenderEmails(senderID string, accountID int, page, limit int) (models.PaginatedSenderEmails, error)
	GetEmailIDsBySenders(senderIDs []int, accountID int) ([]string, error)
	DeleteEmailsByMessageIDs(messageIDs []string) error
	DeleteCacheByMessageIDs(messageIDs []string) error

	// Export
	GetSendersForExport() ([]models.SenderExportRow, error)

	// Analytics
	GetTopSenders(accountID int) ([]models.TopSenderAnalytic, error)
	GetEmailTimeline(accountID int) ([]models.EmailTimelinePoint, error)
	GetLabelCounts(accountID int) ([]models.LabelCount, error)

	// Preferences
	GetPreferences() (map[string]string, error)
	UpsertPreferences(prefs map[string]string) error
	GetPreferenceValue(key string) (string, error)

	// Transactions
	WithTx(fn func(Store) error) error
}

// PostgresStore is the production implementation of Store.
type PostgresStore struct {
	db    dbQuerier
	rawDB *sql.DB // needed for BeginTx
}

func New(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db, rawDB: db}
}

func (s *PostgresStore) WithTx(fn func(Store) error) error {
	tx, err := s.rawDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(&PostgresStore{db: tx, rawDB: s.rawDB}); err != nil {
		return err
	}
	return tx.Commit()
}

// ── Accounts ─────────────────────────────────────────────────────────────────

func (s *PostgresStore) UpsertAccount(email, accessToken, refreshToken string, expiry time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO gmail_accounts (email, access_token, refresh_token, token_expiry, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT(email)
		DO UPDATE SET
			access_token = EXCLUDED.access_token,
			refresh_token = COALESCE(EXCLUDED.refresh_token, gmail_accounts.refresh_token),
			token_expiry = EXCLUDED.token_expiry,
			updated_at = NOW()
	`, email, accessToken, refreshToken, expiry)
	return err
}

func (s *PostgresStore) GetAccountToken(email string) (string, *oauth2.Token, error) {
	var emailOut, accessToken string
	var refreshToken sql.NullString
	var expiry sql.NullTime

	var err error
	if email != "" {
		err = s.db.QueryRow(`
			SELECT email, access_token, refresh_token, token_expiry
			FROM gmail_accounts WHERE email = $1
		`, email).Scan(&emailOut, &accessToken, &refreshToken, &expiry)
	} else {
		err = s.db.QueryRow(`
			SELECT email, access_token, refresh_token, token_expiry
			FROM gmail_accounts ORDER BY updated_at DESC LIMIT 1
		`).Scan(&emailOut, &accessToken, &refreshToken, &expiry)
	}
	if err != nil {
		return "", nil, err
	}

	token := &oauth2.Token{AccessToken: accessToken}
	if refreshToken.Valid {
		token.RefreshToken = refreshToken.String
	}
	if expiry.Valid {
		token.Expiry = expiry.Time
	}
	return emailOut, token, nil
}

func (s *PostgresStore) ResolveAccountID(email string) (int, error) {
	var id int
	var err error
	if email != "" {
		err = s.db.QueryRow(`SELECT id FROM gmail_accounts WHERE email = $1`, email).Scan(&id)
	} else {
		err = s.db.QueryRow(`SELECT id FROM gmail_accounts ORDER BY updated_at DESC LIMIT 1`).Scan(&id)
	}
	return id, err
}

func (s *PostgresStore) ListAccounts() ([]models.Account, error) {
	rows, err := s.db.Query(`SELECT id, email, updated_at FROM gmail_accounts ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []models.Account
	for rows.Next() {
		var a models.Account
		if err := rows.Scan(&a.ID, &a.Email, &a.UpdatedAt); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, nil
}

// ── Stats ─────────────────────────────────────────────────────────────────────

func (s *PostgresStore) GetInboxStats(accountID int) (models.InboxStats, error) {
	var stats models.InboxStats

	if accountID != 0 {
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sender_emails WHERE gmail_account_id = $1`, accountID).Scan(&stats.TotalEmails); err != nil {
			return stats, err
		}
		if err := s.db.QueryRow(`SELECT COUNT(DISTINCT sender_id) FROM sender_emails WHERE gmail_account_id = $1`, accountID).Scan(&stats.TotalSenders); err != nil {
			return stats, err
		}
		if err := s.db.QueryRow(`SELECT COALESCE(SUM(size_bytes), 0) FROM sender_emails WHERE gmail_account_id = $1`, accountID).Scan(&stats.TotalSizeBytes); err != nil {
			return stats, err
		}
	} else {
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM gmail_message_cache`).Scan(&stats.TotalEmails); err != nil {
			return stats, err
		}
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM senders`).Scan(&stats.TotalSenders); err != nil {
			return stats, err
		}
		if err := s.db.QueryRow(`SELECT COALESCE(SUM(size_bytes), 0) FROM sender_emails`).Scan(&stats.TotalSizeBytes); err != nil {
			return stats, err
		}
	}

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM gmail_accounts`).Scan(&stats.ConnectedApps); err != nil {
		return stats, err
	}
	return stats, nil
}

// ── Senders ───────────────────────────────────────────────────────────────────

func (s *PostgresStore) GetSenders(p models.GetSendersParams) ([]models.SenderSummary, error) {
	args := []any{}
	conditions := []string{}
	accountArgIdx := 0

	if p.AccountID != 0 {
		args = append(args, p.AccountID)
		accountArgIdx = len(args)
		conditions = append(conditions, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM sender_emails se_acc
			WHERE se_acc.sender_id = s.id
			  AND se_acc.gmail_account_id = $%d
		)`, accountArgIdx))
	}

	if p.Search != "" {
		args = append(args, "%"+p.Search+"%")
		conditions = append(conditions, fmt.Sprintf("(s.email ILIKE $%d OR s.display_name ILIKE $%d)", len(args), len(args)))
	}

	if len(p.Labels) > 0 {
		args = append(args, pq.Array(p.Labels))
		labelArgIdx := len(args)
		labelCond := fmt.Sprintf(`EXISTS (
			SELECT 1 FROM sender_emails se2
			WHERE se2.sender_id = s.id
			  AND string_to_array(se2.label_ids, ',') && $%d`, labelArgIdx)
		if accountArgIdx > 0 {
			labelCond += fmt.Sprintf(`
			  AND se2.gmail_account_id = $%d`, accountArgIdx)
		}
		labelCond += `
		)`
		conditions = append(conditions, labelCond)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	secondarySort := ""
	if p.SortCol != "last_received_at" {
		secondarySort = ", last_received_at DESC NULLS LAST"
	}

	joinClause := "LEFT JOIN sender_emails e ON e.sender_id = s.id"
	if accountArgIdx > 0 {
		joinClause += fmt.Sprintf(" AND e.gmail_account_id = $%d", accountArgIdx)
	}

	query := fmt.Sprintf(`
		SELECT
			s.id,
			s.email,
			COALESCE(NULLIF(s.display_name, ''), s.email) AS display_name,
			COALESCE(NULLIF(SPLIT_PART(s.email, '@', 2), ''), s.email) AS domain,
			COUNT(e.id) AS email_count,
			COUNT(DISTINCT e.gmail_thread_id) AS thread_count,
			COALESCE(SUM(e.size_bytes), 0) AS total_size_bytes,
			(s.unsubscribe_url IS NOT NULL OR s.unsubscribe_mailto IS NOT NULL) AS can_unsubscribe,
			s.unsubscribed_at,
			s.blocked_at,
			MAX(e.received_at) AS last_received_at,
			COALESCE(BOOL_OR(COALESCE(e.list_unsubscribe, '') <> ''), FALSE) AS has_list_unsubscribe,
			COALESCE(BOOL_OR('CATEGORY_PROMOTIONS' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_promotions,
			COALESCE(BOOL_OR('CATEGORY_SOCIAL' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_social,
			COALESCE(BOOL_OR('CATEGORY_UPDATES' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_updates,
			COALESCE(BOOL_OR('CATEGORY_PERSONAL' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_personal,
			COALESCE(BOOL_OR('INBOX' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_inbox
		FROM senders s
		%s
		%s
		GROUP BY s.id, s.email, s.display_name, s.unsubscribe_url, s.unsubscribe_mailto, s.unsubscribed_at, s.blocked_at
		ORDER BY %s %s NULLS LAST%s
	`, joinClause, whereClause, p.SortCol, p.SortOrder, secondarySort)

	var rows *sql.Rows
	var err error
	if len(args) > 0 {
		rows, err = s.db.Query(query, args...)
	} else {
		rows, err = s.db.Query(query)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []models.SenderSummary
	for rows.Next() {
		var item models.SenderSummary
		var hasListUnsubscribe, hasPromotions, hasSocial, hasUpdates, hasPersonal bool
		if err := rows.Scan(
			&item.ID,
			&item.Email,
			&item.DisplayName,
			&item.Domain,
			&item.EmailCount,
			&item.ThreadCount,
			&item.TotalSizeBytes,
			&item.CanUnsubscribe,
			&item.UnsubscribedAt,
			&item.BlockedAt,
			&item.LastReceivedAt,
			&hasListUnsubscribe,
			&hasPromotions,
			&hasSocial,
			&hasUpdates,
			&hasPersonal,
			&item.HasInbox,
		); err != nil {
			return nil, err
		}
		item.Category = classify.Category(item.Email, hasListUnsubscribe, hasPromotions, hasSocial, hasUpdates, hasPersonal, item.HasInbox)
		item.KeepScore = classify.Score(item.Category, item.HasInbox)
		items = append(items, item)
	}
	return items, nil
}

func (s *PostgresStore) GetSendersByDomain(accountID int) ([]models.SenderDomainSummary, error) {
	conditions := []string{}
	args := []any{}
	accountArgIdx := 0

	if accountID != 0 {
		args = append(args, accountID)
		accountArgIdx = len(args)
		conditions = append(conditions, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM sender_emails se_acc
			WHERE se_acc.sender_id = s.id
			  AND se_acc.gmail_account_id = $%d
		)`, accountArgIdx))
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	joinClause := "LEFT JOIN sender_emails e ON e.sender_id = s.id"
	if accountArgIdx > 0 {
		joinClause += fmt.Sprintf(" AND e.gmail_account_id = $%d", accountArgIdx)
	}

	query := fmt.Sprintf(`
		SELECT
			COALESCE(NULLIF(SPLIT_PART(s.email, '@', 2), ''), s.email) AS domain,
			COUNT(DISTINCT s.id) AS sender_count,
			COUNT(e.id) AS email_count,
			COALESCE(SUM(e.size_bytes), 0) AS total_size_bytes,
			MAX(e.received_at) AS last_received_at,
			COALESCE(BOOL_OR(COALESCE(e.list_unsubscribe, '') <> ''), FALSE) AS has_list_unsubscribe,
			COALESCE(BOOL_OR('CATEGORY_PROMOTIONS' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_promotions,
			COALESCE(BOOL_OR('CATEGORY_SOCIAL' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_social,
			COALESCE(BOOL_OR('CATEGORY_UPDATES' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_updates,
			COALESCE(BOOL_OR('CATEGORY_PERSONAL' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_personal,
			COALESCE(BOOL_OR('INBOX' = ANY(string_to_array(COALESCE(e.label_ids, ''), ','))), FALSE) AS has_inbox,
			ARRAY_REMOVE(ARRAY_AGG(DISTINCT s.email), NULL) AS sender_emails
		FROM senders s
		%s
		%s
		GROUP BY domain
		ORDER BY email_count DESC, domain ASC
	`, joinClause, whereClause)

	var rows *sql.Rows
	var err error
	if len(args) > 0 {
		rows, err = s.db.Query(query, args...)
	} else {
		rows, err = s.db.Query(query)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []models.SenderDomainSummary
	for rows.Next() {
		var item models.SenderDomainSummary
		var hasListUnsubscribe, hasPromotions, hasSocial, hasUpdates, hasPersonal bool
		var senderEmails pq.StringArray
		if err := rows.Scan(
			&item.Domain,
			&item.SenderCount,
			&item.EmailCount,
			&item.TotalSizeBytes,
			&item.LastReceivedAt,
			&hasListUnsubscribe,
			&hasPromotions,
			&hasSocial,
			&hasUpdates,
			&hasPersonal,
			&item.HasInbox,
			&senderEmails,
		); err != nil {
			return nil, err
		}
		item.Category = classify.Category(item.Domain, hasListUnsubscribe, hasPromotions, hasSocial, hasUpdates, hasPersonal, item.HasInbox)
		item.KeepScore = classify.Score(item.Category, item.HasInbox)
		item.SenderEmails = []string(senderEmails)
		items = append(items, item)
	}
	return items, nil
}

func (s *PostgresStore) GetDistinctLabels(accountID int) ([]string, error) {
	conditions := []string{"label_ids IS NOT NULL", "label_ids != ''"}
	args := []any{}
	if accountID != 0 {
		args = append(args, accountID)
		conditions = append(conditions, fmt.Sprintf("gmail_account_id = $%d", len(args)))
	}
	query := fmt.Sprintf(`
		SELECT DISTINCT unnest(string_to_array(label_ids, ',')) AS label
		FROM sender_emails
		WHERE %s
		ORDER BY label
	`, strings.Join(conditions, " AND "))

	var rows *sql.Rows
	var err error
	if len(args) > 0 {
		rows, err = s.db.Query(query, args...)
	} else {
		rows, err = s.db.Query(query)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, err
		}
		if strings.TrimSpace(label) != "" {
			labels = append(labels, label)
		}
	}
	return labels, nil
}

func (s *PostgresStore) GetSenderUnsubInfo(senderID string) (sql.NullString, sql.NullString, sql.NullTime, error) {
	var unsubURL, unsubMailto sql.NullString
	var unsubscribedAt sql.NullTime
	err := s.db.QueryRow(`
		SELECT unsubscribe_url, unsubscribe_mailto, unsubscribed_at
		FROM senders WHERE id = $1
	`, senderID).Scan(&unsubURL, &unsubMailto, &unsubscribedAt)
	return unsubURL, unsubMailto, unsubscribedAt, err
}

func (s *PostgresStore) MarkUnsubscribed(senderID string) error {
	_, err := s.db.Exec(`UPDATE senders SET unsubscribed_at = NOW() WHERE id = $1`, senderID)
	return err
}

func (s *PostgresStore) GetSenderBlockInfo(senderID string) (string, sql.NullTime, error) {
	var email string
	var blockedAt sql.NullTime
	err := s.db.QueryRow(`SELECT email, blocked_at FROM senders WHERE id = $1`, senderID).
		Scan(&email, &blockedAt)
	return email, blockedAt, err
}

func (s *PostgresStore) MarkBlocked(senderID string) error {
	_, err := s.db.Exec(`UPDATE senders SET blocked_at = NOW() WHERE id = $1`, senderID)
	return err
}

// ── Emails ────────────────────────────────────────────────────────────────────

func (s *PostgresStore) GetSenderEmails(senderID string, accountID int, page, limit int) (models.PaginatedSenderEmails, error) {
	hasScope := accountID != 0

	countQuery := `SELECT COUNT(*) FROM sender_emails WHERE sender_id = $1`
	countArgs := []any{senderID}
	if hasScope {
		countQuery += ` AND gmail_account_id = $2`
		countArgs = append(countArgs, accountID)
	}
	var total int
	if err := s.db.QueryRow(countQuery, countArgs...).Scan(&total); err != nil {
		return models.PaginatedSenderEmails{}, err
	}

	baseQuery := `
		SELECT
			id,
			gmail_message_id,
			COALESCE(gmail_thread_id, ''),
			COALESCE(subject, ''),
			COALESCE(snippet, ''),
			COALESCE(body_text, ''),
			COALESCE(body_html, ''),
			received_at,
			COALESCE(label_ids, '')
		FROM sender_emails
		WHERE sender_id = $1
		ORDER BY received_at DESC NULLS LAST
	`
	queryArgs := []any{senderID}
	if hasScope {
		queryArgs = append(queryArgs, accountID)
		baseQuery = strings.Replace(baseQuery, "WHERE sender_id = $1", "WHERE sender_id = $1 AND gmail_account_id = $2", 1)
	}

	var rows *sql.Rows
	var err error
	if limit > 0 {
		offset := (page - 1) * limit
		if hasScope {
			rows, err = s.db.Query(baseQuery+` LIMIT $3 OFFSET $4`, senderID, accountID, limit, offset)
		} else {
			rows, err = s.db.Query(baseQuery+` LIMIT $2 OFFSET $3`, senderID, limit, offset)
		}
	} else {
		rows, err = s.db.Query(baseQuery, queryArgs...)
	}
	if err != nil {
		return models.PaginatedSenderEmails{}, err
	}
	defer rows.Close()

	items := []models.SenderEmail{}
	for rows.Next() {
		var item models.SenderEmail
		if err := rows.Scan(&item.ID, &item.GmailMessageID, &item.GmailThreadID, &item.Subject, &item.Snippet, &item.BodyText, &item.BodyHTML, &item.ReceivedAt, &item.LabelIDs); err != nil {
			return models.PaginatedSenderEmails{}, err
		}
		items = append(items, item)
	}

	return models.PaginatedSenderEmails{Data: items, Total: total, Page: page, Limit: limit}, nil
}

func (s *PostgresStore) GetEmailIDsBySenders(senderIDs []int, accountID int) ([]string, error) {
	var rows *sql.Rows
	var err error
	if accountID != 0 {
		rows, err = s.db.Query(`
			SELECT gmail_message_id FROM sender_emails
			WHERE sender_id = ANY($1) AND gmail_account_id = $2
		`, pq.Array(senderIDs), accountID)
	} else {
		rows, err = s.db.Query(`
			SELECT gmail_message_id FROM sender_emails WHERE sender_id = ANY($1)
		`, pq.Array(senderIDs))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *PostgresStore) DeleteEmailsByMessageIDs(messageIDs []string) error {
	_, err := s.db.Exec(`DELETE FROM sender_emails WHERE gmail_message_id = ANY($1)`, pq.Array(messageIDs))
	return err
}

func (s *PostgresStore) DeleteCacheByMessageIDs(messageIDs []string) error {
	_, err := s.db.Exec(`DELETE FROM gmail_message_cache WHERE gmail_message_id = ANY($1)`, pq.Array(messageIDs))
	return err
}

// ── Export ────────────────────────────────────────────────────────────────────

func (s *PostgresStore) GetSendersForExport() ([]models.SenderExportRow, error) {
	rows, err := s.db.Query(`
		SELECT
			s.email,
			COALESCE(NULLIF(s.display_name, ''), s.email),
			COUNT(e.id),
			COUNT(DISTINCT e.gmail_thread_id),
			COALESCE(SUM(e.size_bytes), 0),
			MAX(e.received_at),
			s.unsubscribed_at
		FROM senders s
		LEFT JOIN sender_emails e ON e.sender_id = s.id
		GROUP BY s.id, s.email, s.display_name, s.unsubscribed_at
		ORDER BY COUNT(e.id) DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.SenderExportRow
	for rows.Next() {
		var row models.SenderExportRow
		if err := rows.Scan(&row.Email, &row.DisplayName, &row.EmailCount, &row.ThreadCount, &row.TotalSizeBytes, &row.LastReceivedAt, &row.UnsubscribedAt); err != nil {
			continue
		}
		result = append(result, row)
	}
	return result, nil
}

// ── Analytics ─────────────────────────────────────────────────────────────────

func (s *PostgresStore) GetTopSenders(accountID int) ([]models.TopSenderAnalytic, error) {
	args := []any{}
	whereClause := ""
	if accountID != 0 {
		args = append(args, accountID)
		whereClause = fmt.Sprintf("WHERE e.gmail_account_id = $%d", len(args))
	}
	query := fmt.Sprintf(`
		SELECT
			COALESCE(NULLIF(s.display_name, ''), s.email) AS display_name,
			COUNT(e.id) AS email_count
		FROM senders s
		JOIN sender_emails e ON e.sender_id = s.id
		%s
		GROUP BY s.id, s.email, s.display_name
		ORDER BY email_count DESC
		LIMIT 10
	`, whereClause)

	var rows *sql.Rows
	var err error
	if len(args) > 0 {
		rows, err = s.db.Query(query, args...)
	} else {
		rows, err = s.db.Query(query)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.TopSenderAnalytic
	for rows.Next() {
		var item models.TopSenderAnalytic
		if err := rows.Scan(&item.Name, &item.Count); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *PostgresStore) GetEmailTimeline(accountID int) ([]models.EmailTimelinePoint, error) {
	args := []any{}
	conditions := []string{"received_at >= NOW() - INTERVAL '180 days'"}
	if accountID != 0 {
		args = append(args, accountID)
		conditions = append(conditions, fmt.Sprintf("gmail_account_id = $%d", len(args)))
	}
	query := fmt.Sprintf(`
		SELECT
			date_trunc('day', received_at)::date AS day,
			COUNT(*) AS count
		FROM sender_emails
		WHERE %s
		GROUP BY day
		ORDER BY day
	`, strings.Join(conditions, " AND "))

	var rows *sql.Rows
	var err error
	if len(args) > 0 {
		rows, err = s.db.Query(query, args...)
	} else {
		rows, err = s.db.Query(query)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.EmailTimelinePoint
	for rows.Next() {
		var item models.EmailTimelinePoint
		var day time.Time
		if err := rows.Scan(&day, &item.Count); err != nil {
			return nil, err
		}
		item.Day = day.Format("2006-01-02")
		result = append(result, item)
	}
	return result, nil
}

func (s *PostgresStore) GetLabelCounts(accountID int) ([]models.LabelCount, error) {
	args := []any{}
	conditions := []string{"label_ids IS NOT NULL", "label_ids != ''"}
	if accountID != 0 {
		args = append(args, accountID)
		conditions = append(conditions, fmt.Sprintf("gmail_account_id = $%d", len(args)))
	}
	query := fmt.Sprintf(`
		SELECT
			unnest(string_to_array(label_ids, ',')) AS label,
			COUNT(*) AS count
		FROM sender_emails
		WHERE %s
		GROUP BY label
		ORDER BY count DESC
	`, strings.Join(conditions, " AND "))

	var rows *sql.Rows
	var err error
	if len(args) > 0 {
		rows, err = s.db.Query(query, args...)
	} else {
		rows, err = s.db.Query(query)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.LabelCount
	for rows.Next() {
		var item models.LabelCount
		if err := rows.Scan(&item.Label, &item.Count); err != nil {
			return nil, err
		}
		if strings.TrimSpace(item.Label) != "" {
			result = append(result, item)
		}
	}
	return result, nil
}

// ── Preferences ───────────────────────────────────────────────────────────────

func (s *PostgresStore) GetPreferences() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM user_preferences`)
	if err != nil {
		return map[string]string{}, nil // table may not exist yet
	}
	defer rows.Close()

	result := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			continue
		}
		result[k] = v
	}
	return result, nil
}

func (s *PostgresStore) UpsertPreferences(prefs map[string]string) error {
	for k, v := range prefs {
		_, err := s.db.Exec(`
			INSERT INTO user_preferences (key, value, updated_at)
			VALUES ($1, $2, NOW())
			ON CONFLICT(key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()
		`, k, v)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) GetPreferenceValue(key string) (string, error) {
	var val string
	err := s.db.QueryRow(`SELECT value FROM user_preferences WHERE key = $1`, key).Scan(&val)
	return val, err
}
