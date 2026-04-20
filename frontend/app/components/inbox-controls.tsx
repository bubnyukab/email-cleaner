'use client';

import { useEffect, useRef, useState } from 'react';
import { useRouter } from 'next/navigation';
import { toast } from 'sonner';

type SyncStatus = {
  running: boolean;
  scanned: number;
  checked?: number;
  pendingTotal?: number;
  total: number;
  inserted: number;
  failed: number;
  startedAt?: string;
  finishedAt?: string;
  nextSyncAt?: string | null;
};

export function InboxControls({
  connectUrl,
  backendUrl,
  account,
}: {
  connectUrl: string;
  backendUrl: string;
  account?: string;
}) {
  const router = useRouter();
  const [syncPending, setSyncPending] = useState(false);
  const [syncProgress, setSyncProgress] = useState<SyncStatus | null>(null);
  const pollerRef = useRef<number | null>(null);

  const stopPolling = () => {
    if (pollerRef.current !== null) {
      window.clearInterval(pollerRef.current);
      pollerRef.current = null;
    }
  };

  const startPolling = () => {
    stopPolling();
    pollerRef.current = window.setInterval(async () => {
      try {
        const response = await fetch(`${backendUrl}/api/go/sync/status`, {
          cache: 'no-store',
        });
        if (!response.ok) {
          return;
        }
        const status = (await response.json()) as SyncStatus;
        setSyncProgress(status);
        setSyncPending(status.running);
        if (!status.running) {
          stopPolling();
        }
      } catch {
        // Ignore transient polling errors while sync is running.
      }
    }, 500);
  };

  useEffect(() => {
    let cancelled = false;

    const loadInitialStatus = async () => {
      try {
        const response = await fetch(`${backendUrl}/api/go/sync/status`, {
          cache: 'no-store',
        });
        if (!response.ok || cancelled) {
          return;
        }
        const status = (await response.json()) as SyncStatus;
        setSyncProgress(status);
        if (status.running) {
          setSyncPending(true);
          startPolling();
        }
      } catch {
        // Ignore initial status errors.
      }
    };

    void loadInitialStatus();

    return () => {
      cancelled = true;
      stopPolling();
    };
  }, [backendUrl]);

  const onSync = async () => {
    setSyncPending(true);
    startPolling();

    const accountQuery = account ? `?account=${encodeURIComponent(account)}` : '';
    try {
      const response = await fetch(`${backendUrl}/api/go/sync/gmail${accountQuery}`, {
        method: 'POST',
      });
      if (!response.ok) {
        let details: string | undefined;
        try {
          const body = (await response.json()) as { error?: string };
          details = body?.error;
        } catch {
          try {
            details = await response.text();
          } catch {
            // ignore
          }
        }
        throw new Error(details || 'Failed to sync inbox');
      }
      const statusResponse = await fetch(`${backendUrl}/api/go/sync/status`, {
        cache: 'no-store',
      });
      if (statusResponse.ok) {
        const status = (await statusResponse.json()) as SyncStatus;
        setSyncProgress(status);
        toast.success(
          `Synced ${status.inserted.toLocaleString()} new email${status.inserted !== 1 ? 's' : ''}`,
        );
      } else {
        toast.success('Sync complete');
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : 'Failed to sync Gmail inbox');
    } finally {
      stopPolling();
      setSyncPending(false);
      router.refresh();
    }
  };

  return (
    <div className="mb-6 space-y-2">
      <div className="flex flex-wrap gap-3">
        <a
          href={connectUrl}
          className="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:opacity-90"
        >
          Connect Gmail
        </a>
        <button
          type="button"
          onClick={onSync}
          disabled={syncPending}
          className="rounded-md border border-input px-4 py-2 text-sm font-medium hover:bg-accent disabled:opacity-50"
        >
          {syncPending ? 'Syncing...' : 'Sync Inbox'}
        </button>

        {syncProgress?.running && (
          <span className="self-center text-sm text-muted-foreground">
            Scanning emails
            {syncProgress.total > 0
              ? ` … ${syncProgress.checked ?? 0} / ${syncProgress.total}`
              : '…'}
          </span>
        )}
      </div>

      <div className="flex flex-wrap gap-4 text-xs text-muted-foreground">
        {syncProgress?.finishedAt && !syncProgress.running && (
          <span>
            Last sync: {new Date(syncProgress.finishedAt).toLocaleString()}
          </span>
        )}
        {syncProgress?.nextSyncAt && (
          <span>
            Next sync: {new Date(syncProgress.nextSyncAt).toLocaleString()}
          </span>
        )}
      </div>
    </div>
  );
}
