import { InboxControls } from '@/app/components/inbox-controls';
import { InboxStatsCards } from '@/app/components/inbox-stats-cards';
import { NavMenu } from '@/app/components/menu';
import { getInboxStats } from '@/lib/go/client';
import Link from 'next/link';
import { Suspense } from 'react';

export default function InboxPage() {
  return (
    <Suspense fallback={<InboxSkeleton />}>
      <InboxContent />
    </Suspense>
  );
}

function InboxSkeleton() {
  return (
    <div className="h-screen overflow-auto">
      <div className="sticky top-0 flex h-[70px] items-center border-b border-gray-200 bg-white px-4">
        <NavMenu />
        <h1 className="text-xl font-semibold">Inbox Overview</h1>
      </div>
      <div className="mx-auto max-w-4xl p-4 text-sm text-gray-500 sm:p-6">
        Loading inbox overview...
      </div>
    </div>
  );
}

async function InboxContent() {
  const stats = await getInboxStats();
  const publicBackendUrl =
    process.env.NEXT_PUBLIC_BACKEND_URL ?? 'http://localhost:8080';
  const connectUrl = `${publicBackendUrl}/api/go/auth/google/start`;

  return (
    <div className="h-screen overflow-auto">
      <div className="sticky top-0 flex h-[70px] items-center border-b border-gray-200 bg-white px-4">
        <NavMenu />
        <h1 className="text-xl font-semibold">Inbox Overview</h1>
      </div>

      <div className="mx-auto max-w-4xl p-4 sm:p-6">
        <InboxControls connectUrl={connectUrl} backendUrl={publicBackendUrl} />

        <InboxStatsCards backendUrl={publicBackendUrl} initialStats={stats} />

        <div className="mt-6 rounded-lg border border-gray-200 p-4">
          <h2 className="text-lg font-semibold">Next Step</h2>
          <p className="mt-2 text-sm text-gray-600">
            Open sender groups to review who sends the most emails and inspect each
            sender thread in detail.
          </p>
          <Link
            href="/senders"
            className="mt-3 inline-block text-sm font-medium text-blue-600 hover:underline"
          >
            View sender groups
          </Link>
        </div>
      </div>
    </div>
  );
}
