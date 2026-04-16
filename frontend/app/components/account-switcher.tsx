'use client';

import { getAccounts, type GmailAccount } from '@/lib/go/client';
import { useEffect, useState } from 'react';
import { useRouter } from 'next/navigation';

const ACCOUNT_STORAGE_KEY = 'email_cleaner_account';

export function getStoredAccount(): string | null {
  if (typeof window === 'undefined') return null;
  return localStorage.getItem(ACCOUNT_STORAGE_KEY);
}

export function AccountSwitcher() {
  const [accounts, setAccounts] = useState<GmailAccount[]>([]);
  const [selected, setSelected] = useState<string>('');
  const router = useRouter();

  useEffect(() => {
    const stored = getStoredAccount();
    if (stored) setSelected(stored);

    getAccounts()
      .then((accts) => {
        setAccounts(accts);
        if (!stored && accts.length > 0) {
          setSelected(accts[0].email);
        }
      })
      .catch(() => {});
  }, []);

  if (accounts.length <= 1) return null;

  const onChange = (email: string) => {
    setSelected(email);
    if (email) {
      localStorage.setItem(ACCOUNT_STORAGE_KEY, email);
    } else {
      localStorage.removeItem(ACCOUNT_STORAGE_KEY);
    }
    router.refresh();
  };

  return (
    <select
      value={selected}
      onChange={(e) => onChange(e.target.value)}
      className="rounded-md border border-input bg-background px-2 py-1 text-sm focus:outline-none"
      aria-label="Switch account"
    >
      {accounts.map((a) => (
        <option key={a.id} value={a.email}>
          {a.email}
        </option>
      ))}
    </select>
  );
}
