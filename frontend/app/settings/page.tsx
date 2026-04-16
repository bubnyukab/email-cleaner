import { NavMenu } from '@/app/components/menu';
import { SettingsForm } from './settings-form';
import { getPreferences } from '@/lib/go/client';

export default async function SettingsPage() {
  const prefs = await getPreferences().catch(() => ({}) as Record<string, string>);

  return (
    <div className="h-screen overflow-auto">
      <div className="sticky top-0 flex h-[70px] items-center border-b border-border bg-background px-4">
        <NavMenu />
        <h1 className="text-xl font-semibold">Settings</h1>
      </div>

      <div className="mx-auto max-w-xl p-4 sm:p-6">
        <SettingsForm initialPrefs={prefs} />
      </div>
    </div>
  );
}
