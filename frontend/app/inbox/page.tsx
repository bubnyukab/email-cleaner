import { AccountSwitcher } from '@/app/components/account-switcher';
import { AnalyticsDashboard } from '@/app/components/analytics-dashboard';
import { InboxControls } from '@/app/components/inbox-controls';
import { InboxStatsCards } from '@/app/components/inbox-stats-cards';
import { NavMenu } from '@/app/components/menu';
import { getInboxStats } from '@/lib/go/client';
import Link from 'next/link';
import { Suspense } from 'react';

type PageProps = { searchParams: Promise<{ account?: string }> };

export default function InboxPage({ searchParams }: PageProps) {
  return (
    <Suspense fallback={<InboxSkeleton />}>
      <InboxContent searchParams={searchParams} />
    </Suspense>
  );
}

function InboxSkeleton() {
  return (
    <div className="h-screen overflow-auto">
      <div className="sticky top-0 flex h-[70px] items-center border-b border-border bg-background px-4">
        <NavMenu />
        <h1 className="text-xl font-semibold">Inbox Overview</h1>
      </div>
      <div className="mx-auto max-w-4xl p-4 text-sm text-muted-foreground sm:p-6">
        Loading inbox overview...
      </div>
    </div>
  );
}

async function InboxContent({ searchParams }: PageProps) {
  const { account } = await searchParams;
  const stats = await getInboxStats(account);
  const publicBackendUrl =
    process.env.NEXT_PUBLIC_BACKEND_URL ?? 'http://localhost:8080';
  const connectUrl = `${publicBackendUrl}/api/go/auth/google/start`;
  const sendersHref = account ? `/senders?account=${encodeURIComponent(account)}` : '/senders';

  return (
    <div className="h-screen overflow-auto">
      <div className="sticky top-0 flex h-[70px] items-center justify-between border-b border-border bg-background px-4">
        <div className="flex items-center">
          <NavMenu />
          <h1 className="text-xl font-semibold">Inbox Overview</h1>
        </div>
        <AccountSwitcher />
      </div>

      <div className="mx-auto max-w-4xl p-4 sm:p-6">
        <InboxControls connectUrl={connectUrl} backendUrl={publicBackendUrl} account={account} />

        <InboxStatsCards backendUrl={publicBackendUrl} initialStats={stats} account={account} />

        <div className="mt-6 rounded-lg border border-border p-4">
          <h2 className="text-lg font-semibold">Next Step</h2>
          <p className="mt-2 text-sm text-muted-foreground">
            Open sender groups to review who sends the most emails and inspect each
            sender thread in detail.
          </p>
          <Link
            href={sendersHref}
            className="mt-3 inline-block text-sm font-medium text-blue-600 hover:underline dark:text-blue-400"
          >
            View sender groups
          </Link>
        </div>

        <AnalyticsDashboard account={account} />
      </div>
    </div>
  );
}
