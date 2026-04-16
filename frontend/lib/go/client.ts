export type InboxStats = {
  totalEmails: number;
  totalSenders: number;
  connectedAccounts: number;
  totalSizeBytes?: number;
};

export type SenderSummary = {
  id: number;
  email: string;
  displayName: string;
  emailCount: number;
  threadCount: number;
  totalSizeBytes?: number;
  canUnsubscribe?: boolean;
  unsubscribedAt?: string | null;
  blockedAt?: string | null;
  lastReceivedAt?: string | null;
};

export type SenderEmail = {
  id: number;
  gmailMessageId: string;
  gmailThreadId: string;
  subject: string;
  snippet: string;
  bodyText: string;
  bodyHtml?: string;
  receivedAt?: string | null;
  labelIds?: string;
};

export type SenderSortCol =
  | 'email_count'
  | 'thread_count'
  | 'display_name'
  | 'last_received';

export type PaginatedSenderEmails = {
  data: SenderEmail[];
  total: number;
  page: number;
  limit: number;
};

export type AnalyticsTopSender = { name: string; count: number };
export type AnalyticsTimelineEntry = { day: string; count: number };
export type AnalyticsLabelEntry = { label: string; count: number };
export type GmailAccount = { id: number; email: string; updatedAt: string };

export function getBackendUrl() {
  return process.env.BACKEND_URL ?? 'http://localhost:8080';
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${getBackendUrl()}${path}`, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(init?.headers ?? {}),
    },
    cache: 'no-store',
  });

  if (!response.ok) {
    let details = '';
    try {
      const body = (await response.json()) as { error?: string };
      details = body?.error ?? '';
    } catch {
      try {
        details = await response.text();
      } catch {
        details = '';
      }
    }
    const suffix = details ? ` - ${details}` : '';
    throw new Error(`Backend request failed: ${response.status}${suffix}`);
  }

  return response.json() as Promise<T>;
}

export async function getSenderSummaries(params?: {
  search?: string;
  sort?: SenderSortCol;
  order?: 'asc' | 'desc';
  labels?: string;
  account?: string;
}) {
  const qs = new URLSearchParams();
  if (params?.search) qs.set('search', params.search);
  if (params?.sort) qs.set('sort', params.sort);
  if (params?.order) qs.set('order', params.order);
  if (params?.labels) qs.set('labels', params.labels);
  if (params?.account) qs.set('account', params.account);
  const query = qs.toString() ? `?${qs.toString()}` : '';
  return request<SenderSummary[]>(`/api/go/senders${query}`);
}

export async function getInboxStats(account?: string) {
  const q = account ? `?account=${encodeURIComponent(account)}` : '';
  return request<InboxStats>(`/api/go/inbox/stats${q}`);
}

export async function getSenderEmails(senderId: string) {
  return request<SenderEmail[]>(`/api/go/senders/${senderId}/emails`);
}

export async function getLabels() {
  return request<string[]>('/api/go/labels');
}

export async function getAccounts() {
  return request<GmailAccount[]>('/api/go/accounts');
}

export async function getPreferences() {
  return request<Record<string, string>>('/api/go/preferences');
}

export async function putPreferences(prefs: Record<string, string>) {
  return request<{ success: boolean }>('/api/go/preferences', {
    method: 'PUT',
    body: JSON.stringify(prefs),
  });
}

export async function bulkTrashEmails(gmailMessageIds: string[]) {
  return request<{
    success: boolean;
    connectedAs: string;
    processed: number;
    failedCount: number;
    trashLabelId?: string;
  }>('/api/go/emails/bulk/trash', {
    method: 'POST',
    body: JSON.stringify({ gmailMessageIds }),
  });
}

export async function bulkTrashBySenders(senderIds: number[]) {
  return request<{
    success: boolean;
    processed: number;
    failedCount: number;
    gmailMessageIds?: string[];
  }>('/api/go/senders/bulk/trash', {
    method: 'POST',
    body: JSON.stringify({ senderIds }),
  });
}

export async function bulkUntrashEmails(gmailMessageIds: string[]) {
  return request<{
    success: boolean;
    processed: number;
    failedCount: number;
  }>('/api/go/emails/bulk/untrash', {
    method: 'POST',
    body: JSON.stringify({ gmailMessageIds }),
  });
}

export async function unsubscribeFromSender(senderId: number) {
  return request<{
    success: boolean;
    method?: string;
    alreadyDone?: boolean;
  }>(`/api/go/senders/${senderId}/unsubscribe`, {
    method: 'POST',
    body: JSON.stringify({}),
  });
}

export async function blockSender(senderId: number) {
  return request<{
    success: boolean;
    alreadyDone?: boolean;
  }>(`/api/go/senders/${senderId}/block`, {
    method: 'POST',
    body: JSON.stringify({}),
  });
}

export async function analyticsTopSenders() {
  return request<AnalyticsTopSender[]>('/api/go/analytics/top-senders');
}

export async function analyticsTimeline() {
  return request<AnalyticsTimelineEntry[]>('/api/go/analytics/timeline');
}

export async function analyticsLabels() {
  return request<AnalyticsLabelEntry[]>('/api/go/analytics/labels');
}
