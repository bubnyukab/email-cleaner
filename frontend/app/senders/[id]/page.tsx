import { NavMenu } from '@/app/components/menu';
import { getSenderEmails, getSenderSummaries } from '@/lib/go/client';
import Link from 'next/link';
import { Suspense } from 'react';

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
      <div className="sticky top-0 flex h-[70px] items-center border-b border-gray-200 bg-white px-4">
        <NavMenu />
        <h1 className="text-xl font-semibold">Sender Emails</h1>
      </div>
      <div className="mx-auto max-w-5xl p-4 text-sm text-gray-500 sm:p-6">
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
      <div className="sticky top-0 flex h-[70px] items-center border-b border-gray-200 bg-white px-4">
        <NavMenu />
        <h1 className="text-xl font-semibold">
          {sender ? sender.displayName : 'Sender Emails'}
        </h1>
      </div>

      <div className="mx-auto max-w-5xl p-4 sm:p-6">
        <Link href="/senders" className="text-sm text-blue-600 hover:underline">
          Back to sender groups
        </Link>

        <div className="mt-4 overflow-hidden rounded-lg border border-gray-200">
          <table className="w-full text-left text-sm">
            <thead className="bg-gray-50">
              <tr>
                <th className="px-4 py-3 font-medium">Subject</th>
                <th className="px-4 py-3 font-medium">Snippet</th>
                <th className="px-4 py-3 font-medium">Received</th>
              </tr>
            </thead>
            <tbody>
              {emails.map((email) => (
                <tr key={email.id} className="border-t border-gray-100 align-top">
                  <td className="px-4 py-3 font-medium">
                    {email.subject || '(No subject)'}
                  </td>
                  <td className="max-w-2xl px-4 py-3 text-gray-600">
                    <div className="line-clamp-3">{email.snippet || email.bodyText}</div>
                  </td>
                  <td className="px-4 py-3 whitespace-nowrap text-gray-600">
                    {email.receivedAt
                      ? new Date(email.receivedAt).toLocaleString()
                      : '-'}
                  </td>
                </tr>
              ))}
              {emails.length === 0 && (
                <tr>
                  <td colSpan={3} className="px-4 py-6 text-center text-gray-500">
                    No emails for this sender yet.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}
