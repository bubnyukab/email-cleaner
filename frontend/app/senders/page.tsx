import { NavMenu } from '@/app/components/menu';
import { getLabels, getSenderSummaries } from '@/lib/go/client';
import { Suspense } from 'react';
import SenderGroupsTable from './sender-groups-table';

type PageProps = { searchParams: Promise<{ labels?: string; search?: string; sort?: string; order?: string; account?: string }> };

export default function SenderGroupsPage({ searchParams }: PageProps) {
  return (
    <Suspense fallback={<SenderGroupsSkeleton />}>
      <SenderGroupsContent searchParams={searchParams} />
    </Suspense>
  );
}

function SenderGroupsSkeleton() {
  return (
    <div className="h-screen overflow-auto">
      <div className="sticky top-0 flex h-[70px] items-center border-b border-border bg-background px-4">
        <NavMenu />
        <h1 className="text-xl font-semibold">Sender Groups</h1>
      </div>
      <div className="mx-auto max-w-7xl p-4 text-sm text-muted-foreground sm:p-6">
        Loading sender groups...
      </div>
    </div>
  );
}

async function SenderGroupsContent({ searchParams }: PageProps) {
  const { labels: labelsParam, search, sort, order, account } = await searchParams;
  const normalizedOrder = order === 'asc' || order === 'desc' ? order : 'desc';
  const normalizedSort = sort === 'thread_count' || sort === 'display_name' || sort === 'last_received'
    ? sort
    : 'email_count';

  const [senders, labels] = await Promise.all([
    getSenderSummaries({
      labels: labelsParam,
      search,
      sort: normalizedSort,
      order: normalizedOrder,
      account,
    }),
    getLabels(account).catch(() => [] as string[]),
  ]);

  return (
    <div className="h-screen overflow-auto">
      <div className="sticky top-0 flex h-[70px] items-center justify-between border-b border-border bg-background px-4">
        <div className="flex items-center gap-2">
          <NavMenu />
          <h1 className="text-xl font-semibold">Sender Groups</h1>
        </div>
        <a
          href={`${process.env.NEXT_PUBLIC_BACKEND_URL ?? 'http://localhost:8080'}/api/go/export/senders?format=csv`}
          className="rounded-md border border-input px-3 py-1.5 text-sm font-medium hover:bg-accent"
          download
        >
          Export CSV
        </a>
      </div>

      <div className="mx-auto max-w-7xl p-4 sm:p-6">
        <p className="mb-4 text-sm text-muted-foreground">
          Grouped by sender using the Go API. Use this to identify high-volume
          newsletters and cleanup candidates.
        </p>

        <SenderGroupsTable
          senders={senders}
          labels={labels}
          initialLabelFilter={labelsParam}
          initialSearch={search ?? ''}
          initialSort={normalizedSort}
          initialOrder={normalizedOrder}
          account={account}
        />
      </div>
    </div>
  );
}
