'use client';

import { useMemo, useState } from 'react';
import { useRouter } from 'next/navigation';

type SyncStatus = {
  running: boolean;
  scanned: number;
  total: number;
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

  const syncProgressText = useMemo(() => {
    if (!syncProgress) {
      return null;
    }
    const total = syncProgress.total > 0 ? syncProgress.total : '?';
    return `${syncProgress.scanned}/${total}`;
  }, [syncProgress]);

  const etaText = useMemo(() => {
    if (!syncProgress || !syncProgress.running) {
      return null;
    }
    if (!syncProgress.startedAt || syncProgress.total <= 0 || syncProgress.scanned <= 0) {
      return null;
    }

    const startedAtMs = new Date(syncProgress.startedAt).getTime();
    if (Number.isNaN(startedAtMs)) {
      return null;
    }

    const elapsedSeconds = (Date.now() - startedAtMs) / 1000;
    if (elapsedSeconds <= 0) {
      return null;
    }

    const rate = syncProgress.scanned / elapsedSeconds;
    if (rate <= 0) {
      return null;
    }

    const remaining = Math.max(syncProgress.total - syncProgress.scanned, 0);
    const remainingSeconds = Math.round(remaining / rate);
    return formatEta(remainingSeconds);
  }, [syncProgress]);

  const startPolling = () => {
    return window.setInterval(async () => {
      try {
        const response = await fetch(`${backendUrl}/api/go/sync/status`, {
          cache: 'no-store',
        });
        if (!response.ok) {
          return;
        }
        const status = (await response.json()) as SyncStatus;
        setSyncProgress(status);
      } catch {
        // Ignore transient polling errors while sync is running.
      }
    }, 500);
  };

  const onSync = async () => {
    setSyncPending(true);
    setSyncError(null);
    setSyncSuccess(false);
    const pollerId = startPolling();

    try {
      const response = await fetch(`${backendUrl}/api/go/sync/gmail`, {
        method: 'POST',
      });
      if (!response.ok) {
        throw new Error('Failed to sync inbox');
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
    } catch {
      setSyncError('Failed to sync Gmail inbox');
    } finally {
      window.clearInterval(pollerId);
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
      {syncPending && syncProgressText && (
        <span className="self-center text-sm text-gray-600">
          Scanned: {syncProgressText}
          {etaText ? ` | ETA: ${etaText}` : ''}
        </span>
      )}
      {syncError && (
        <span className="self-center text-sm text-red-600">{syncError}</span>
      )}
      {syncSuccess && (
        <span className="self-center text-sm text-green-600">
          Inbox synced successfully
        </span>
      )}
    </div>
  );
}

function formatEta(totalSeconds: number) {
  if (totalSeconds < 60) {
    return `${totalSeconds}s`;
  }
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes < 60) {
    return `${minutes}m ${seconds}s`;
  }
  const hours = Math.floor(minutes / 60);
  const remMinutes = minutes % 60;
  return `${hours}h ${remMinutes}m`;
}
