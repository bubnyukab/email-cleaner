'use client';

import { useEffect, useState } from 'react';
import type { InboxStats } from '@/lib/go/client';

export function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(1024));
  const value = bytes / Math.pow(1024, i);
  return `${value.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

export function InboxStatsCards({
  backendUrl,
  initialStats,
  account,
}: {
  backendUrl: string;
  initialStats: InboxStats;
  account?: string;
}) {
  const [stats, setStats] = useState<InboxStats>(initialStats);

  useEffect(() => {
    let cancelled = false;

    const accountQuery = account ? `?account=${encodeURIComponent(account)}` : '';
    const refreshStats = async () => {
      try {
        const response = await fetch(`${backendUrl}/api/go/inbox/stats${accountQuery}`, {
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
  }, [backendUrl, account]);

  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
      <div className="rounded-lg border border-border p-4">
        <p className="text-sm text-muted-foreground">Emails scanned</p>
        <p className="mt-2 text-3xl font-semibold">{stats.totalEmails.toLocaleString()}</p>
      </div>
      <div className="rounded-lg border border-border p-4">
        <p className="text-sm text-muted-foreground">Unique senders</p>
        <p className="mt-2 text-3xl font-semibold">{stats.totalSenders.toLocaleString()}</p>
      </div>
      <div className="rounded-lg border border-border p-4">
        <p className="text-sm text-muted-foreground">Connected accounts</p>
        <p className="mt-2 text-3xl font-semibold">{stats.connectedAccounts}</p>
      </div>
    </div>
  );
}
