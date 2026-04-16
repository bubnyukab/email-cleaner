'use client';

import { putPreferences } from '@/lib/go/client';
import { useState } from 'react';
import { useRouter } from 'next/navigation';
import { toast } from 'sonner';

const INTERVAL_OPTIONS = [
  { value: 'off', label: 'Off (manual sync only)' },
  { value: '1h', label: 'Every hour' },
  { value: '6h', label: 'Every 6 hours' },
  { value: '24h', label: 'Once a day' },
];

export function SettingsForm({
  initialPrefs,
}: {
  initialPrefs: Record<string, string>;
}) {
  const router = useRouter();
  const [syncInterval, setSyncInterval] = useState(
    initialPrefs.sync_interval ?? 'off',
  );
  const [saving, setSaving] = useState(false);

  const onSave = async () => {
    setSaving(true);
    try {
      await putPreferences({ sync_interval: syncInterval });
      toast.success('Settings saved');
      router.refresh();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : 'Failed to save settings');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-6">
      <div className="rounded-lg border border-border p-5">
        <h2 className="mb-4 text-base font-semibold">Automatic Sync</h2>
        <div className="space-y-3">
          {INTERVAL_OPTIONS.map((opt) => (
            <label key={opt.value} className="flex cursor-pointer items-center gap-3">
              <input
                type="radio"
                name="sync_interval"
                value={opt.value}
                checked={syncInterval === opt.value}
                onChange={() => setSyncInterval(opt.value)}
                className="h-4 w-4 accent-primary"
              />
              <span className="text-sm">{opt.label}</span>
            </label>
          ))}
        </div>
        <p className="mt-3 text-xs text-muted-foreground">
          When enabled, the server checks every minute whether a sync is due and runs
          it automatically in the background.
        </p>
      </div>

      <button
        type="button"
        onClick={onSave}
        disabled={saving}
        className="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:opacity-90 disabled:opacity-50"
      >
        {saving ? 'Saving…' : 'Save settings'}
      </button>
    </div>
  );
}
