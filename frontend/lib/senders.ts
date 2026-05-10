import type { SenderSummary } from '@/lib/go/client';

export type QuickFilter = 'newsletter' | 'noreply' | 'neverInbox' | 'oneEmail' | 'olderYear';

export type DomainGroup = {
  domain: string;
  senderIds: number[];
  senderCount: number;
  emailCount: number;
  threadCount: number;
  totalSizeBytes: number;
  lastReceivedAt?: string | null;
  category: string;
  keepScore: number;
  hasInbox: boolean;
  exampleSenderId: number;
};

export function applyQuickFilter(
  senders: SenderSummary[],
  filter: QuickFilter | null,
): SenderSummary[] {
  if (!filter) return senders;
  const cutoff = new Date();
  cutoff.setFullYear(cutoff.getFullYear() - 1);
  return senders.filter((sender) => {
    if (filter === 'newsletter') return sender.category === 'Newsletter';
    if (filter === 'noreply') return sender.category === 'No-reply';
    if (filter === 'neverInbox') return !sender.hasInbox;
    if (filter === 'oneEmail') return (sender.emailCount ?? 0) === 1;
    if (!sender.lastReceivedAt) return false;
    return new Date(sender.lastReceivedAt) < cutoff;
  });
}

export function groupSendersByDomain(senders: SenderSummary[]): DomainGroup[] {
  const byDomain = new Map<string, DomainGroup>();
  for (const sender of senders) {
    const key = sender.domain || sender.email;
    const existing = byDomain.get(key);
    if (!existing) {
      byDomain.set(key, {
        domain: key,
        senderIds: [sender.id],
        senderCount: 1,
        emailCount: sender.emailCount ?? 0,
        threadCount: sender.threadCount ?? 0,
        totalSizeBytes: sender.totalSizeBytes ?? 0,
        lastReceivedAt: sender.lastReceivedAt ?? null,
        category: sender.category,
        keepScore: sender.keepScore ?? 0,
        hasInbox: sender.hasInbox ?? false,
        exampleSenderId: sender.id,
      });
      continue;
    }
    existing.senderIds.push(sender.id);
    existing.senderCount += 1;
    existing.emailCount += sender.emailCount ?? 0;
    existing.threadCount += sender.threadCount ?? 0;
    existing.totalSizeBytes += sender.totalSizeBytes ?? 0;
    if (
      sender.lastReceivedAt &&
      (!existing.lastReceivedAt || new Date(sender.lastReceivedAt) > new Date(existing.lastReceivedAt))
    ) {
      existing.lastReceivedAt = sender.lastReceivedAt;
    }
    if ((sender.keepScore ?? 0) < existing.keepScore) {
      existing.keepScore = sender.keepScore ?? 0;
    }
    if (sender.category === 'Newsletter' || existing.category === 'Newsletter') {
      existing.category = 'Newsletter';
    }
    existing.hasInbox = existing.hasInbox || !!sender.hasInbox;
  }
  return Array.from(byDomain.values()).sort((a, b) => b.emailCount - a.emailCount);
}
