import { NavMenu } from '@/app/components/menu';
import { getSenderSummaries } from '@/lib/go/client';
import { Suspense } from 'react';
import Link from 'next/link';

export default function SenderGroupsPage() {
  return (
    <Suspense fallback={<SenderGroupsSkeleton />}>
      <SenderGroupsContent />
    </Suspense>
  );
}

function SenderGroupsSkeleton() {
  return (
    <div className="h-screen overflow-auto">
      <div className="sticky top-0 flex h-[70px] items-center border-b border-gray-200 bg-white px-4">
        <NavMenu />
        <h1 className="text-xl font-semibold">Sender Groups</h1>
      </div>
      <div className="mx-auto max-w-4xl p-4 text-sm text-gray-500 sm:p-6">
        Loading sender groups...
      </div>
    </div>
  );
}

async function SenderGroupsContent() {
  const senders = await getSenderSummaries();

  return (
    <div className="h-screen overflow-auto">
      <div className="sticky top-0 flex h-[70px] items-center border-b border-gray-200 bg-white px-4">
        <NavMenu />
        <h1 className="text-xl font-semibold">Sender Groups</h1>
      </div>

      <div className="mx-auto max-w-4xl p-4 sm:p-6">
        <p className="mb-4 text-sm text-gray-600">
          Grouped by sender using the Go API. Use this to identify high-volume
          newsletters and cleanup candidates.
        </p>

        <div className="overflow-hidden rounded-lg border border-gray-200">
          <table className="w-full text-left text-sm">
            <thead className="bg-gray-50">
              <tr>
                <th className="px-4 py-3 font-medium">Sender</th>
                <th className="px-4 py-3 font-medium">Email</th>
                <th className="px-4 py-3 font-medium">Emails</th>
                <th className="px-4 py-3 font-medium">Threads</th>
                <th className="px-4 py-3 font-medium">Last received</th>
              </tr>
            </thead>
            <tbody>
              {senders.map((sender) => (
                <tr key={sender.id} className="border-t border-gray-100">
                  <td className="px-4 py-3 font-medium">
                    <Link
                      href={`/senders/${sender.id}`}
                      className="text-blue-600 hover:underline"
                    >
                      {sender.displayName}
                    </Link>
                  </td>
                  <td className="px-4 py-3 text-gray-600">{sender.email}</td>
                  <td className="px-4 py-3">{sender.emailCount}</td>
                  <td className="px-4 py-3">{sender.threadCount}</td>
                  <td className="px-4 py-3 text-gray-600">
                    {sender.lastReceivedAt
                      ? new Date(sender.lastReceivedAt).toLocaleString()
                      : '-'}
                  </td>
                </tr>
              ))}
              {senders.length === 0 && (
                <tr>
                  <td colSpan={5} className="px-4 py-6 text-center text-gray-500">
                    No sender data yet.
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
