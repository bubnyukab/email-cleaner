'use client';

import { unsubscribeFromSender } from '@/lib/go/client';
import { useState } from 'react';
import { useRouter } from 'next/navigation';
import { toast } from 'sonner';
import { Check, MailX } from 'lucide-react';

export default function SenderUnsubscribeButton({
  senderId,
  email,
  canUnsubscribe,
  unsubscribedAt,
}: {
  senderId: number;
  email: string;
  canUnsubscribe: boolean;
  unsubscribedAt: string | null;
}) {
  const router = useRouter();
  const [pending, setPending] = useState(false);

  if (unsubscribedAt) {
    return (
      <span className="inline-flex items-center gap-1 text-sm text-green-600">
        <Check size={16} /> Unsubscribed
      </span>
    );
  }

  if (!canUnsubscribe) {
    return (
      <span className="text-sm text-muted-foreground" title="No unsubscribe link found">
        No unsubscribe link
      </span>
    );
  }

  const onClick = async () => {
    setPending(true);
    try {
      await unsubscribeFromSender(senderId);
      toast.success(`Unsubscribed from ${email}`);
      router.refresh();
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : `Failed to unsubscribe from ${email}`,
      );
    } finally {
      setPending(false);
    }
  };

  return (
    <button
      type="button"
      onClick={onClick}
      disabled={pending}
      className="inline-flex items-center gap-1 rounded-md bg-red-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-red-700 disabled:opacity-50"
    >
      <MailX size={14} />
      {pending ? 'Unsubscribing...' : 'Unsubscribe'}
    </button>
  );
}
