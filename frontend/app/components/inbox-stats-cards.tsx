'use client';

import { useEffect, useState } from 'react';
import type { InboxStats } from '@/lib/go/client';

export function InboxStatsCards({
  backendUrl,
  initialStats,
}: {
  backendUrl: string;
  initialStats: InboxStats;
}) {
  const [stats, setStats] = useState<InboxStats>(initialStats);

  useEffect(() => {
    let cancelled = false;

    const refreshStats = async () => {
      try {
        const response = await fetch(`${backendUrl}/api/go/inbox/stats`, {
          cache: 'no-store',
        });
        if (!response.ok || cancelled) {
          return;
        }
        const next = (await response.json()) as InboxStats;
        if (!cancelled) {
          setStats(next);
        }
      } catch {
        // Ignore transient polling errors and keep last known values.
      }
    };

    const timer = window.setInterval(() => {
      void refreshStats();
    }, 1000);

    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [backendUrl]);

  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
      <div className="rounded-lg border border-gray-200 p-4">
        <p className="text-sm text-gray-500">Inbox emails scanned</p>
        <p className="mt-2 text-3xl font-semibold">{stats.totalEmails}</p>
      </div>
      <div className="rounded-lg border border-gray-200 p-4">
        <p className="text-sm text-gray-500">Unique senders</p>
        <p className="mt-2 text-3xl font-semibold">{stats.totalSenders}</p>
      </div>
      <div className="rounded-lg border border-gray-200 p-4">
        <p className="text-sm text-gray-500">Connected Gmail accounts</p>
        <p className="mt-2 text-3xl font-semibold">{stats.connectedAccounts}</p>
      </div>
    </div>
  );
}

