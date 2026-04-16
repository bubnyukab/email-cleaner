'use client';

import { useEffect, useRef, useState } from 'react';
import { useRouter } from 'next/navigation';

type SyncStatus = {
  running: boolean;
  scanned: number; // processed uncached messages (new work)
  checked?: number; // ids evaluated (cached + uncached)
  pendingTotal?: number; // uncached message ids discovered so far
  total: number; // total inbox message count estimate
  inserted: number;
  failed: number;
  startedAt?: string;
};

export function InboxControls({
  connectUrl,
  backendUrl,
}: {
  connectUrl: string;
  backendUrl: string;
}) {
  const router = useRouter();
  const [syncPending, setSyncPending] = useState(false);
  const [syncError, setSyncError] = useState<string | null>(null);
  const [syncSuccess, setSyncSuccess] = useState(false);
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
    setSyncError(null);
    setSyncSuccess(false);
    startPolling();

    try {
      const response = await fetch(`${backendUrl}/api/go/sync/gmail`, {
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
      setSyncSuccess(true);
      // One final read to display the completed progress snapshot.
      const statusResponse = await fetch(`${backendUrl}/api/go/sync/status`, {
        cache: 'no-store',
      });
      if (statusResponse.ok) {
        const status = (await statusResponse.json()) as SyncStatus;
        setSyncProgress(status);
      }
    } catch (error) {
      setSyncError(error instanceof Error ? error.message : 'Failed to sync Gmail inbox');
    } finally {
      stopPolling();
      setSyncPending(false);
      router.refresh();
    }
  };

  return (
    <div className="mb-6 flex flex-wrap gap-3">
      <a
        href={connectUrl}
        className="rounded-md bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-700"
      >
        Connect Gmail
      </a>
      <button
        type="button"
        onClick={onSync}
        disabled={syncPending}
        className="rounded-md border border-gray-300 px-4 py-2 text-sm font-medium hover:bg-gray-100 disabled:opacity-50"
      >
        {syncPending ? 'Syncing...' : 'Sync Inbox'}
      </button>

      {syncProgress?.running && (
        <span className="self-center text-sm text-gray-600">
          Scanning emails{syncProgress.total > 0
            ? ` … ${syncProgress.checked ?? 0} / ${syncProgress.total}`
            : '…'}
        </span>
      )}
      {syncError && (
        <span className="self-center text-sm text-red-600">{syncError}</span>
      )}
      {syncSuccess && (
        <span className="self-center text-sm text-green-600">
          Done
        </span>
      )}
    </div>
  );
}
