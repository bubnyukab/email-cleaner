export type InboxStats = {
  totalEmails: number;
  totalSenders: number;
  connectedAccounts: number;
};

export type SenderSummary = {
  id: number;
  email: string;
  displayName: string;
  emailCount: number;
  threadCount: number;
  lastReceivedAt?: string | null;
};

export type SenderEmail = {
  id: number;
  gmailMessageId: string;
  gmailThreadId: string;
  subject: string;
  snippet: string;
  bodyText: string;
  receivedAt?: string | null;
};

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

export async function getSenderSummaries() {
  return request<SenderSummary[]>('/api/go/senders');
}

export async function getInboxStats() {
  return request<InboxStats>('/api/go/inbox/stats');
}

export async function getSenderEmails(senderId: string) {
  return request<SenderEmail[]>(`/api/go/senders/${senderId}/emails`);
}

export async function syncGmailInbox() {
  return request<{ success: boolean; fetched: number; insertedCount: number }>(
    '/api/go/sync/gmail',
    {
      method: 'POST',
      body: JSON.stringify({}),
    },
  );
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

export async function bulkDeleteEmails(gmailMessageIds: string[]) {
  return request<{
    success: boolean;
    connectedAs: string;
    processed: number;
    failedCount: number;
  }>('/api/go/emails/bulk/delete', {
    method: 'POST',
    body: JSON.stringify({ gmailMessageIds }),
  });
}
