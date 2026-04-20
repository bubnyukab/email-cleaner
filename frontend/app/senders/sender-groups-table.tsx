'use client';

import {
  blockSender,
  bulkTrashBySenders,
  bulkUntrashEmails,
  getSenderSummaries,
  unsubscribeFromSender,
  type SenderSortCol,
  type SenderSummary,
} from '@/lib/go/client';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { toast } from 'sonner';
import Link from 'next/link';
import { ArrowDown, ArrowUp, ArrowUpDown, Ban, Check, MailX, Trash2 } from 'lucide-react';
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';

type SortState = { col: SenderSortCol; order: 'asc' | 'desc' };
type QuickFilter = 'newsletter' | 'noreply' | 'neverInbox' | 'oneEmail' | 'olderYear';

const LABEL_DISPLAY: Record<string, string> = {
  INBOX: 'Inbox',
  CATEGORY_PERSONAL: 'Personal',
  CATEGORY_SOCIAL: 'Social',
  CATEGORY_PROMOTIONS: 'Promotions',
  CATEGORY_UPDATES: 'Updates',
  CATEGORY_FORUMS: 'Forums',
  IMPORTANT: 'Important',
  UNREAD: 'Unread',
  SENT: 'Sent',
  SPAM: 'Spam',
  STARRED: 'Starred',
};

const CHIP_ORDER = [
  'CATEGORY_PERSONAL',
  'CATEGORY_PROMOTIONS',
  'CATEGORY_SOCIAL',
  'CATEGORY_UPDATES',
  'CATEGORY_FORUMS',
  'IMPORTANT',
  'INBOX',
  'UNREAD',
  'SENT',
  'SPAM',
  'STARRED',
];

type DomainGroup = {
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

function labelName(raw: string) {
  return LABEL_DISPLAY[raw] ?? raw;
}

function formatDate(v: string | Date | null | undefined): string {
  if (!v) return '—';
  const d = new Date(v);
  if (isNaN(d.getTime())) return '—';
  return `${d.getUTCMonth() + 1}/${d.getUTCDate()}/${d.getUTCFullYear()}`;
}

function SortIcon({ col, sort }: { col: SenderSortCol; sort: SortState }) {
  if (sort.col !== col) return <ArrowUpDown size={13} className="ml-1 inline opacity-40" />;
  return sort.order === 'asc' ? (
    <ArrowUp size={13} className="ml-1 inline" />
  ) : (
    <ArrowDown size={13} className="ml-1 inline" />
  );
}

function categoryBadgeClass(category: string) {
  switch (category) {
    case 'Newsletter':
      return 'bg-orange-100 text-orange-700 dark:bg-orange-900 dark:text-orange-300';
    case 'Promotional':
      return 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900 dark:text-yellow-300';
    case 'Social':
      return 'bg-blue-100 text-blue-700 dark:bg-blue-900 dark:text-blue-300';
    case 'Notification':
      return 'bg-zinc-200 text-zinc-700 dark:bg-zinc-700 dark:text-zinc-200';
    case 'No-reply':
      return 'bg-red-100 text-red-700 dark:bg-red-900 dark:text-red-300';
    case 'Personal':
      return 'bg-green-100 text-green-700 dark:bg-green-900 dark:text-green-300';
    default:
      return 'bg-muted text-muted-foreground';
  }
}

function scoreDotClass(score: number) {
  if (score >= 70) return 'bg-green-500';
  if (score >= 40) return 'bg-yellow-500';
  return 'bg-red-500';
}

function buildQueryString({
  search,
  sort,
  labels,
  account,
}: {
  search: string;
  sort: SortState;
  labels: Set<string>;
  account?: string;
}) {
  const params = new URLSearchParams();
  if (search.trim()) params.set('search', search.trim());
  if (labels.size > 0) params.set('labels', Array.from(labels).join(','));
  params.set('sort', sort.col);
  params.set('order', sort.order);
  if (account) params.set('account', account);
  const query = params.toString();
  return query ? `?${query}` : '';
}

export default function SenderGroupsTable({
  senders: initialSenders,
  labels: availableLabels = [],
  initialLabelFilter,
  initialSearch = '',
  initialSort = 'email_count',
  initialOrder = 'desc',
  account,
}: {
  senders: SenderSummary[];
  labels?: string[];
  initialLabelFilter?: string;
  initialSearch?: string;
  initialSort?: string;
  initialOrder?: string;
  account?: string;
}) {
  const router = useRouter();
  const pathname = usePathname();

  const [senders, setSenders] = useState<SenderSummary[]>(initialSenders);
  const [selected, setSelected] = useState<Set<number>>(() => new Set());
  const [pendingOp, setPendingOp] = useState(false);
  const [unsubPending, setUnsubPending] = useState<Set<number>>(() => new Set());
  const [blockPending, setBlockPending] = useState<Set<number>>(() => new Set());
  const [trashDialogSender, setTrashDialogSender] = useState<SenderSummary | null>(null);
  const [search, setSearch] = useState(initialSearch);
  const [sort, setSort] = useState<SortState>({
    col: (initialSort as SenderSortCol) || 'email_count',
    order: initialOrder === 'asc' ? 'asc' : 'desc',
  });
  const [groupByDomain, setGroupByDomain] = useState(false);
  const [quickFilter, setQuickFilter] = useState<QuickFilter | null>(null);
  const [activeLabels, setActiveLabels] = useState<Set<string>>(() =>
    initialLabelFilter
      ? new Set(initialLabelFilter.split(',').filter(Boolean))
      : new Set(),
  );
  const searchDebounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const filterableLabels = CHIP_ORDER.filter((l) => availableLabels.includes(l));

  const syncURL = useCallback(
    (nextSearch: string, nextSort: SortState, nextLabels: Set<string>) => {
      const query = buildQueryString({
        search: nextSearch,
        sort: nextSort,
        labels: nextLabels,
        account,
      });
      router.replace(`${pathname}${query}`, { scroll: false });
    },
    [account, pathname, router],
  );

  const fetchSenders = useCallback(
    async (searchVal: string, sortVal: SortState, labelsVal: Set<string>) => {
      try {
        const data = await getSenderSummaries({
          search: searchVal || undefined,
          sort: sortVal.col,
          order: sortVal.order,
          labels: labelsVal.size > 0 ? Array.from(labelsVal).join(',') : undefined,
          account,
        });
        setSenders(data);
      } catch {
        // Keep existing data on error.
      }
    },
    [account],
  );

  const onSearchChange = (value: string) => {
    setSearch(value);
    syncURL(value, sort, activeLabels);
    if (searchDebounceRef.current) clearTimeout(searchDebounceRef.current);
    searchDebounceRef.current = setTimeout(() => {
      void fetchSenders(value, sort, activeLabels);
    }, 300);
  };

  const onSortChange = (col: SenderSortCol) => {
    const next: SortState =
      sort.col === col
        ? { col, order: sort.order === 'desc' ? 'asc' : 'desc' }
        : { col, order: 'desc' };
    setSort(next);
    syncURL(search, next, activeLabels);
    void fetchSenders(search, next, activeLabels);
  };

  const onLabelToggle = (label: string) => {
    const next = new Set(activeLabels);
    if (next.has(label)) next.delete(label);
    else next.add(label);
    setActiveLabels(next);
    syncURL(search, sort, next);
    void fetchSenders(search, sort, next);
  };

  const onClearLabels = () => {
    const empty = new Set<string>();
    setActiveLabels(empty);
    syncURL(search, sort, empty);
    void fetchSenders(search, sort, empty);
  };

  useEffect(() => {
    setSenders(initialSenders);
  }, [initialSenders]);

  const visibleSenders = useMemo(() => {
    if (!quickFilter) return senders;
    const cutoff = new Date();
    cutoff.setFullYear(cutoff.getFullYear() - 1);
    return senders.filter((sender) => {
      if (quickFilter === 'newsletter') return sender.category === 'Newsletter';
      if (quickFilter === 'noreply') return sender.category === 'No-reply';
      if (quickFilter === 'neverInbox') return !sender.hasInbox;
      if (quickFilter === 'oneEmail') return (sender.emailCount ?? 0) === 1;
      if (!sender.lastReceivedAt) return false;
      return new Date(sender.lastReceivedAt) < cutoff;
    });
  }, [quickFilter, senders]);

  const domainGroups = useMemo<DomainGroup[]>(() => {
    const byDomain = new Map<string, DomainGroup>();
    for (const sender of visibleSenders) {
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
  }, [visibleSenders]);

  const allIds = visibleSenders.map((s) => s.id);
  const selectedCount = selected.size;
  const allSelected = allIds.length > 0 && selectedCount === allIds.length;
  const selectedEmailCount = visibleSenders
    .filter((s) => selected.has(s.id))
    .reduce((sum, s) => sum + (s.emailCount ?? 0), 0);

  useEffect(() => {
    const visibleIds = new Set(visibleSenders.map((sender) => sender.id));
    setSelected((prev) => {
      const next = new Set(Array.from(prev).filter((id) => visibleIds.has(id)));
      return next.size === prev.size ? prev : next;
    });
  }, [visibleSenders]);

  const currentQuery = buildQueryString({
    search,
    sort,
    labels: activeLabels,
    account,
  });
  const returnTo = encodeURIComponent(`${pathname}${currentQuery}`);

  const toggleOne = (id: number, checked: boolean) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });

  const toggleGroup = (ids: number[], checked: boolean) =>
    setSelected((prev) => {
      const next = new Set(prev);
      for (const id of ids) {
        if (checked) next.add(id);
        else next.delete(id);
      }
      return next;
    });

  const toggleAll = (checked: boolean) =>
    setSelected(checked ? new Set(allIds) : new Set());

  const applyQuickSelect = (kind: QuickFilter) => {
    setQuickFilter((prev) => (prev === kind ? null : kind));
  };

  const onTrashSelected = async () => {
    if (selectedCount === 0) return;
    const ok = window.confirm(
      `Move all emails from ${selectedCount} sender(s) (~${selectedEmailCount} emails) to trash?`,
    );
    if (!ok) return;
    setPendingOp(true);
    try {
      const result = await bulkTrashBySenders(Array.from(selected), account);
      const trashedIds = result.gmailMessageIds ?? [];
      setSelected(new Set());
      router.refresh();
      if (trashedIds.length > 0) {
        toast.success(`Moved ${result.processed} emails to trash`, {
          action: {
            label: 'Undo',
            onClick: async () => {
              try {
                await bulkUntrashEmails(trashedIds, account);
                toast.success('Restored emails from trash');
                router.refresh();
              } catch {
                toast.error('Failed to undo trash');
              }
            },
          },
          duration: 10000,
        });
      } else {
        toast.success(`Moved ${result.processed} emails to trash`);
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : 'Failed to trash emails');
    } finally {
      setPendingOp(false);
    }
  };

  const onTrashOne = async (sender: SenderSummary) => {
    setTrashDialogSender(null);
    setPendingOp(true);
    try {
      const result = await bulkTrashBySenders([sender.id], account);
      const trashedIds = result.gmailMessageIds ?? [];
      router.refresh();
      if (trashedIds.length > 0) {
        toast.success(`Moved ${result.processed} emails from ${sender.email} to trash`, {
          action: {
            label: 'Undo',
            onClick: async () => {
              try {
                await bulkUntrashEmails(trashedIds, account);
                toast.success('Restored emails from trash');
                router.refresh();
              } catch {
                toast.error('Failed to undo trash');
              }
            },
          },
          duration: 10000,
        });
      } else {
        toast.success(`Moved ${result.processed} emails from ${sender.email} to trash`);
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : 'Failed to trash emails');
    } finally {
      setPendingOp(false);
    }
  };

  const onUnsubscribe = async (sender: SenderSummary) => {
    setUnsubPending((prev) => new Set(prev).add(sender.id));
    try {
      await unsubscribeFromSender(sender.id, account);
      toast.success(`Unsubscribed from ${sender.email}`);
      router.refresh();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : `Failed to unsubscribe from ${sender.email}`);
    } finally {
      setUnsubPending((prev) => {
        const next = new Set(prev);
        next.delete(sender.id);
        return next;
      });
    }
  };

  const onBlock = async (sender: SenderSummary) => {
    setBlockPending((prev) => new Set(prev).add(sender.id));
    try {
      await blockSender(sender.id, account);
      toast.success(`Blocked ${sender.email}`);
      router.refresh();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : `Failed to block ${sender.email}`);
    } finally {
      setBlockPending((prev) => {
        const next = new Set(prev);
        next.delete(sender.id);
        return next;
      });
    }
  };

  const thBtn = (col: SenderSortCol, label: string) => (
    <button
      type="button"
      onClick={() => onSortChange(col)}
      className="flex items-center whitespace-nowrap font-medium hover:text-foreground"
    >
      {label}
      <SortIcon col={col} sort={sort} />
    </button>
  );

  const noResults = search || activeLabels.size > 0 || !!quickFilter
    ? 'No senders match your filters.'
    : 'No sender data yet.';

  return (
    <>
      <Dialog
        open={trashDialogSender !== null}
        onOpenChange={(open) => { if (!open) setTrashDialogSender(null); }}
      >
        {trashDialogSender && (
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Move to Trash</DialogTitle>
              <DialogDescription>
                Move {trashDialogSender.emailCount} email
                {trashDialogSender.emailCount !== 1 ? 's' : ''} from{' '}
                <strong>{trashDialogSender.email}</strong> to trash?
              </DialogDescription>
            </DialogHeader>
            <DialogFooter className="mt-4">
              <DialogClose asChild>
                <button type="button" className="rounded-md border border-input px-4 py-2 text-sm font-medium hover:bg-accent">
                  Cancel
                </button>
              </DialogClose>
              <button
                type="button"
                onClick={() => onTrashOne(trashDialogSender)}
                className="rounded-md bg-red-600 px-4 py-2 text-sm font-medium text-white hover:bg-red-700"
              >
                Move to Trash
              </button>
            </DialogFooter>
          </DialogContent>
        )}
      </Dialog>

      <div className="mb-3 flex flex-wrap items-center gap-3">
        <input
          type="search"
          value={search}
          onChange={(e) => onSearchChange(e.target.value)}
          placeholder="Search by name or email…"
          className="w-full max-w-sm rounded-md border border-input bg-background px-3 py-2 text-sm outline-none placeholder:text-muted-foreground focus:border-ring"
        />
        <span className="text-sm text-muted-foreground">
          {visibleSenders.length} sender{visibleSenders.length === 1 ? '' : 's'}
          {quickFilter ? ` (filtered from ${senders.length})` : ''}
        </span>
        <button
          type="button"
          onClick={() => setGroupByDomain((prev) => !prev)}
          className="rounded-md border border-input px-3 py-1.5 text-xs font-medium hover:bg-accent"
        >
          {groupByDomain ? 'Show by sender' : 'Group by domain'}
        </button>
      </div>

      <div className="mb-3 flex flex-wrap gap-2">
        <button type="button" onClick={() => applyQuickSelect('newsletter')} className={`rounded-full border px-3 py-1 text-xs ${quickFilter === 'newsletter' ? 'border-foreground bg-foreground text-background' : 'hover:bg-accent'}`}>All Newsletters</button>
        <button type="button" onClick={() => applyQuickSelect('noreply')} className={`rounded-full border px-3 py-1 text-xs ${quickFilter === 'noreply' ? 'border-foreground bg-foreground text-background' : 'hover:bg-accent'}`}>All No-reply</button>
        <button type="button" onClick={() => applyQuickSelect('neverInbox')} className={`rounded-full border px-3 py-1 text-xs ${quickFilter === 'neverInbox' ? 'border-foreground bg-foreground text-background' : 'hover:bg-accent'}`}>Never in Inbox</button>
        <button type="button" onClick={() => applyQuickSelect('oneEmail')} className={`rounded-full border px-3 py-1 text-xs ${quickFilter === 'oneEmail' ? 'border-foreground bg-foreground text-background' : 'hover:bg-accent'}`}>1 email only</button>
        <button type="button" onClick={() => applyQuickSelect('olderYear')} className={`rounded-full border px-3 py-1 text-xs ${quickFilter === 'olderYear' ? 'border-foreground bg-foreground text-background' : 'hover:bg-accent'}`}>Older than 1 year</button>
        {quickFilter && (
          <button type="button" onClick={() => setQuickFilter(null)} className="rounded-full border border-dashed px-3 py-1 text-xs text-muted-foreground hover:border-foreground hover:text-foreground">
            Clear quick filter
          </button>
        )}
      </div>

      {filterableLabels.length > 0 && (
        <div className="mb-4 flex flex-wrap gap-2">
          {filterableLabels.map((label) => {
            const active = activeLabels.has(label);
            return (
              <button
                key={label}
                type="button"
                onClick={() => onLabelToggle(label)}
                className={
                  'rounded-full border px-3 py-1 text-xs font-medium transition-colors ' +
                  (active
                    ? 'border-foreground bg-foreground text-background'
                    : 'border-border bg-background text-muted-foreground hover:border-foreground hover:text-foreground')
                }
              >
                {labelName(label)}
              </button>
            );
          })}
          {activeLabels.size > 0 && (
            <button
              type="button"
              onClick={onClearLabels}
              className="rounded-full border border-dashed border-muted-foreground px-3 py-1 text-xs text-muted-foreground hover:border-foreground hover:text-foreground"
            >
              Clear filters
            </button>
          )}
        </div>
      )}

      {selectedCount > 0 && (
        <div className="mb-3 flex items-center gap-3">
          <button
            type="button"
            onClick={onTrashSelected}
            disabled={pendingOp}
            className="rounded-md bg-red-600 px-3 py-2 text-sm font-medium text-white hover:bg-red-700 disabled:opacity-50"
          >
            {pendingOp
              ? 'Moving...'
              : `Move to Trash (${selectedCount} senders, ~${selectedEmailCount} emails)`}
          </button>
        </div>
      )}

      <div className="hidden overflow-hidden rounded-lg border border-border sm:block">
        <table className="w-full text-left text-sm">
          <thead className="bg-muted text-muted-foreground">
            <tr>
              <th className="w-10 px-3 py-3">
                <input
                  type="checkbox"
                  aria-label="Select all senders"
                  checked={allSelected}
                  onChange={(e) => toggleAll(e.target.checked)}
                  disabled={visibleSenders.length === 0 || pendingOp}
                />
              </th>
              <th className="px-3 py-3">{thBtn('display_name', groupByDomain ? 'Domain' : 'Sender')}</th>
              {!groupByDomain && <th className="px-3 py-3 font-medium">Email</th>}
              <th className="px-3 py-3 font-medium">Category</th>
              <th className="w-20 px-3 py-3 text-right">{thBtn('email_count', 'Emails')}</th>
              <th className="w-28 whitespace-nowrap px-3 py-3">{thBtn('last_received', 'Last received')}</th>
              <th className="w-24 px-3 py-3 font-medium">Actions</th>
            </tr>
          </thead>
          <tbody>
            {groupByDomain
              ? domainGroups.map((group) => (
                <tr key={group.domain} className="border-t border-border hover:bg-muted/30">
                  <td className="w-10 px-3 py-3">
                    <input
                      type="checkbox"
                      aria-label={`Select ${group.domain}`}
                      checked={group.senderIds.every((id) => selected.has(id))}
                      disabled={pendingOp}
                      onChange={(e) => toggleGroup(group.senderIds, e.target.checked)}
                    />
                  </td>
                  <td className="px-3 py-3 font-medium">{group.domain}</td>
                  <td className="px-3 py-3">
                    <span className={`inline-flex rounded-full px-2 py-0.5 text-xs ${categoryBadgeClass(group.category)}`}>
                      {group.category}
                    </span>
                  </td>
                  <td className="w-20 px-3 py-3 text-right">{group.emailCount}</td>
                  <td className="w-28 whitespace-nowrap px-3 py-3 text-muted-foreground">{formatDate(group.lastReceivedAt)}</td>
                  <td className="w-24 px-3 py-3">
                    <Link
                      href={`/senders/${group.exampleSenderId}?returnTo=${returnTo}`}
                      className="text-xs text-blue-600 hover:underline dark:text-blue-400"
                    >
                      Open
                    </Link>
                  </td>
                </tr>
              ))
              : visibleSenders.map((sender) => (
                <tr key={sender.id} className="border-t border-border hover:bg-muted/30">
                  <td className="w-10 px-3 py-3">
                    <input
                      type="checkbox"
                      aria-label={`Select ${sender.displayName}`}
                      checked={selected.has(sender.id)}
                      disabled={pendingOp}
                      onChange={(e) => toggleOne(sender.id, e.target.checked)}
                    />
                  </td>
                  <td className="max-w-[180px] px-3 py-3 font-medium">
                    <div className="flex min-w-0 items-center gap-2">
                      <Link
                        href={`/senders/${sender.id}?returnTo=${returnTo}`}
                        className="block truncate text-blue-600 hover:underline dark:text-blue-400"
                        title={sender.displayName}
                      >
                        {sender.displayName}
                      </Link>
                      <span className={`h-2.5 w-2.5 rounded-full ${scoreDotClass(sender.keepScore ?? 0)}`} title={`Keep score ${sender.keepScore ?? 0}`} />
                      {sender.blockedAt && (
                        <span className="shrink-0 rounded-full bg-red-100 px-1.5 py-0.5 text-xs font-medium text-red-700 dark:bg-red-900 dark:text-red-300">
                          Blocked
                        </span>
                      )}
                    </div>
                  </td>
                  <td className="max-w-[220px] px-3 py-3 text-muted-foreground">
                    <span className="block truncate" title={sender.email}>{sender.email}</span>
                  </td>
                  <td className="px-3 py-3">
                    <span className={`inline-flex rounded-full px-2 py-0.5 text-xs ${categoryBadgeClass(sender.category)}`}>
                      {sender.category}
                    </span>
                  </td>
                  <td className="w-20 px-3 py-3 text-right">{Number(sender.emailCount ?? 0)}</td>
                  <td className="w-28 whitespace-nowrap px-3 py-3 text-muted-foreground">
                    {formatDate(sender.lastReceivedAt)}
                  </td>
                  <td className="w-24 px-3 py-3">
                    <div className="flex items-center gap-0.5">
                      {sender.unsubscribedAt ? (
                        <span className="inline-flex items-center rounded p-1 text-green-600" title="Unsubscribed">
                          <Check size={15} />
                        </span>
                      ) : sender.canUnsubscribe ? (
                        <button
                          type="button"
                          onClick={() => onUnsubscribe(sender)}
                          disabled={unsubPending.has(sender.id)}
                          className="inline-flex items-center rounded p-1 text-muted-foreground hover:bg-accent disabled:opacity-50"
                          title="Unsubscribe"
                        >
                          <MailX size={15} />
                        </button>
                      ) : null}
                      <button
                        type="button"
                        onClick={() => onBlock(sender)}
                        disabled={blockPending.has(sender.id) || !!sender.blockedAt}
                        className="inline-flex items-center rounded p-1 text-muted-foreground hover:bg-accent disabled:opacity-50"
                        title={sender.blockedAt ? 'Already blocked' : 'Block sender'}
                      >
                        <Ban size={15} />
                      </button>
                      <button
                        type="button"
                        title={`Trash all emails from ${sender.email}`}
                        disabled={pendingOp}
                        onClick={() => setTrashDialogSender(sender)}
                        className="inline-flex items-center rounded p-1 text-muted-foreground hover:bg-red-50 hover:text-red-600 disabled:opacity-50 dark:hover:bg-red-950"
                      >
                        <Trash2 size={15} />
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            {visibleSenders.length === 0 && (
              <tr>
                <td colSpan={groupByDomain ? 6 : 7} className="px-4 py-6 text-center text-muted-foreground">
                  {noResults}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="flex flex-col gap-2 sm:hidden">
        {visibleSenders.length === 0 ? (
          <p className="py-6 text-center text-sm text-muted-foreground">{noResults}</p>
        ) : (
          visibleSenders.map((sender) => (
            <div key={sender.id} className="rounded-lg border border-border p-3">
              <div className="flex items-start justify-between gap-2">
                <div className="flex min-w-0 items-center gap-2">
                  <input
                    type="checkbox"
                    aria-label={`Select ${sender.displayName}`}
                    checked={selected.has(sender.id)}
                    disabled={pendingOp}
                    onChange={(e) => toggleOne(sender.id, e.target.checked)}
                  />
                  <div className="min-w-0">
                    <div className="flex items-center gap-1.5">
                      <Link
                        href={`/senders/${sender.id}?returnTo=${returnTo}`}
                        className="truncate font-medium text-blue-600 hover:underline dark:text-blue-400"
                      >
                        {sender.displayName}
                      </Link>
                      <span className={`h-2.5 w-2.5 rounded-full ${scoreDotClass(sender.keepScore ?? 0)}`} />
                    </div>
                    <p className="truncate text-xs text-muted-foreground">{sender.email}</p>
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-0.5">
                  {sender.unsubscribedAt ? (
                    <span className="rounded p-1 text-green-600" title="Unsubscribed">
                      <Check size={14} />
                    </span>
                  ) : sender.canUnsubscribe ? (
                    <button
                      type="button"
                      onClick={() => onUnsubscribe(sender)}
                      disabled={unsubPending.has(sender.id)}
                      className="rounded p-1 text-muted-foreground hover:bg-accent disabled:opacity-50"
                      title="Unsubscribe"
                    >
                      <MailX size={14} />
                    </button>
                  ) : null}
                  <button
                    type="button"
                    onClick={() => onBlock(sender)}
                    disabled={blockPending.has(sender.id) || !!sender.blockedAt}
                    className="rounded p-1 text-muted-foreground hover:bg-accent disabled:opacity-50"
                    title={sender.blockedAt ? 'Already blocked' : 'Block sender'}
                  >
                    <Ban size={14} />
                  </button>
                  <button
                    type="button"
                    title="Trash all emails"
                    disabled={pendingOp}
                    onClick={() => setTrashDialogSender(sender)}
                    className="rounded p-1 text-muted-foreground hover:bg-red-50 hover:text-red-600 disabled:opacity-50 dark:hover:bg-red-950"
                  >
                    <Trash2 size={14} />
                  </button>
                </div>
              </div>
              <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <span className={`inline-flex rounded-full px-2 py-0.5 ${categoryBadgeClass(sender.category)}`}>
                  {sender.category}
                </span>
                <span>{sender.emailCount} emails</span>
                {sender.lastReceivedAt && (
                  <span>Last: {formatDate(sender.lastReceivedAt)}</span>
                )}
              </div>
            </div>
          ))
        )}
      </div>
    </>
  );
}
