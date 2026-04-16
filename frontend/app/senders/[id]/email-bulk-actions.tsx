'use client';

import { bulkTrashEmails, bulkUntrashEmails, type SenderEmail } from '@/lib/go/client';
import { useEffect, useMemo, useState } from 'react';
import { useRouter } from 'next/navigation';
import { toast } from 'sonner';

export default function SenderEmailsBulkActions({ emails }: { emails: SenderEmail[] }) {
  const router = useRouter();
  const [search, setSearch] = useState('');
  const filteredEmails = useMemo(() => {
    const query = search.trim().toLowerCase();
    if (!query) {
      return emails;
    }

    return emails.filter((email) => {
      const haystack = [email.subject, email.snippet, email.bodyText]
        .filter(Boolean)
        .join(' ')
        .toLowerCase();
      return haystack.includes(query);
    });
  }, [emails, search]);
  const allGmailIds = useMemo(
    () => filteredEmails.map((e) => e.gmailMessageId),
    [filteredEmails],
  );
  const [selected, setSelected] = useState<Set<string>>(() => new Set());

  const [pendingOp, setPendingOp] = useState<'trash' | null>(null);

  useEffect(() => {
    setSelected(new Set());
    setPendingOp(null);
  }, [allGmailIds.join('|')]);

  const selectedCount = selected.size;
  const allSelected = allGmailIds.length > 0 && selectedCount === allGmailIds.length;

  const toggleOne = (gmailMessageId: string, checked: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (checked) next.add(gmailMessageId);
      else next.delete(gmailMessageId);
      return next;
    });
  };

  const toggleAll = (checked: boolean) => {
    setSelected(() => {
      if (!checked) return new Set();
      return new Set(allGmailIds);
    });
  };

  const onTrashSelected = async () => {
    if (selectedCount === 0) return;
    setPendingOp('trash');
    const trashedIds = Array.from(selected);
    try {
      const result = await bulkTrashEmails(trashedIds);
      setSelected(new Set());
      router.refresh();

      toast.success(`Moved ${result.processed} emails to trash`, {
        action: {
          label: 'Undo',
          onClick: async () => {
            try {
              await bulkUntrashEmails(trashedIds);
              toast.success('Restored emails from trash');
              router.refresh();
            } catch {
              toast.error('Failed to undo trash');
            }
          },
        },
        duration: 10000,
      });
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : 'Failed to move selected emails to Trash',
      );
    } finally {
      setPendingOp(null);
    }
  };

  return (
    <div className="mt-4">
      <div className="mb-3 flex flex-wrap items-center gap-3">
        <input
          type="search"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Search subject, snippet, or email body"
          className="w-full max-w-md rounded-md border border-input bg-background px-3 py-2 text-sm outline-none ring-0 placeholder:text-muted-foreground focus:border-ring"
        />
        <span className="text-sm text-muted-foreground">
          Showing {filteredEmails.length} of {emails.length} emails
        </span>
      </div>

      <div className="mb-3 flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={onTrashSelected}
          disabled={pendingOp !== null || selectedCount === 0}
          className="rounded-md bg-red-600 px-3 py-2 text-sm font-medium text-white hover:bg-red-700 disabled:opacity-50"
        >
          {pendingOp === 'trash' ? 'Moving...' : `Move to Trash (${selectedCount})`}
        </button>
      </div>

      <div className="overflow-hidden rounded-lg border border-border">
        <table className="w-full text-left text-sm">
          <thead className="bg-muted">
            <tr>
              <th className="w-12 px-4 py-3 font-medium">
                <input
                  type="checkbox"
                  aria-label="Select all emails"
                  checked={allSelected}
                  onChange={(e) => toggleAll(e.target.checked)}
                  disabled={filteredEmails.length === 0 || pendingOp !== null}
                />
              </th>
              <th className="px-4 py-3 font-medium">Subject</th>
              <th className="px-4 py-3 font-medium">Snippet</th>
              <th className="px-4 py-3 font-medium">Received</th>
            </tr>
          </thead>
          <tbody>
            {filteredEmails.map((email) => (
              <tr key={email.id} className="border-t border-border align-top">
                <td className="px-4 py-3">
                  <input
                    type="checkbox"
                    aria-label={`Select ${email.subject || '(No subject)'}`}
                    checked={selected.has(email.gmailMessageId)}
                    disabled={pendingOp !== null}
                    onChange={(e) => toggleOne(email.gmailMessageId, e.target.checked)}
                  />
                </td>
                <td className="px-4 py-3 font-medium">{email.subject || '(No subject)'}</td>
                <td className="max-w-2xl px-4 py-3 text-muted-foreground">
                  <div className="line-clamp-3">{email.snippet || email.bodyText}</div>
                </td>
                <td className="px-4 py-3 whitespace-nowrap text-muted-foreground">
                  {email.receivedAt ? new Date(email.receivedAt).toLocaleString() : '-'}
                </td>
              </tr>
            ))}
            {filteredEmails.length === 0 && (
              <tr>
                <td colSpan={4} className="px-4 py-6 text-center text-muted-foreground">
                  {emails.length === 0
                    ? 'No emails for this sender yet.'
                    : 'No emails match your search.'}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
