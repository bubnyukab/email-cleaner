import { NavMenu } from '@/app/components/menu';
import { getSenderEmails, getSenderSummaries } from '@/lib/go/client';
import Link from 'next/link';
import { Suspense } from 'react';
import SenderEmailsBulkActions from './email-bulk-actions';
import SenderUnsubscribeButton from './unsubscribe-button';

export default function SenderEmailsPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  return (
    <Suspense fallback={<SenderEmailsSkeleton />}>
      <SenderEmailsContent params={params} />
    </Suspense>
  );
}

function SenderEmailsSkeleton() {
  return (
    <div className="h-screen overflow-auto">
      <div className="sticky top-0 flex h-[70px] items-center border-b border-border bg-background px-4">
        <NavMenu />
        <h1 className="text-xl font-semibold">Sender Emails</h1>
      </div>
      <div className="mx-auto max-w-5xl p-4 text-sm text-muted-foreground sm:p-6">
        Loading sender emails...
      </div>
    </div>
  );
}

async function SenderEmailsContent({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;
  const [emails, senders] = await Promise.all([
    getSenderEmails(id),
    getSenderSummaries(),
  ]);

  const sender = senders.find((item) => String(item.id) === id);

  return (
    <div className="h-screen overflow-auto">
      <div className="sticky top-0 flex h-[70px] items-center border-b border-border bg-background px-4">
        <NavMenu />
        <h1 className="text-xl font-semibold">
          {sender ? sender.displayName : 'Sender Emails'}
        </h1>
      </div>

      <div className="mx-auto max-w-5xl p-4 sm:p-6">
        <div className="flex items-center gap-4">
          <Link href="/senders" className="text-sm text-blue-600 hover:underline dark:text-blue-400">
            Back to sender groups
          </Link>
          {sender && (
            <SenderUnsubscribeButton
              senderId={sender.id}
              email={sender.email}
              canUnsubscribe={sender.canUnsubscribe ?? false}
              unsubscribedAt={sender.unsubscribedAt ?? null}
            />
          )}
        </div>

        <SenderEmailsBulkActions emails={emails} />
      </div>
    </div>
  );
}
