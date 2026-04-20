'use client';

import { getAccounts, type GmailAccount } from '@/lib/go/client';
import { useEffect, useMemo, useState, useTransition } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { Check, ChevronDown, Loader2, Mail } from 'lucide-react';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';

const ACCOUNT_STORAGE_KEY = 'email_cleaner_account';

export function getStoredAccount(): string | null {
  if (typeof window === 'undefined') return null;
  return localStorage.getItem(ACCOUNT_STORAGE_KEY);
}

export function AccountSwitcher() {
  const [accounts, setAccounts] = useState<GmailAccount[]>([]);
  const [selected, setSelected] = useState<string>('');
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [pendingEmail, setPendingEmail] = useState<string | null>(null);
  const router = useRouter();
  const pathname = usePathname();
  const [isPending, startTransition] = useTransition();

  useEffect(() => {
    const params = typeof window !== 'undefined'
      ? new URLSearchParams(window.location.search)
      : new URLSearchParams();
    const accountFromUrl = params.get('account');
    const stored = getStoredAccount();
    const initial = accountFromUrl || stored || '';
    if (initial) setSelected(initial);

    getAccounts()
      .then((accts) => {
        setAccounts(accts);
        if (!initial && accts.length > 0) {
          setSelected(accts[0].email);
        }
      })
      .catch(() => {});
  }, []);

  useEffect(() => {
    if (!open) setQuery('');
  }, [open]);

  useEffect(() => {
    if (!isPending) setPendingEmail(null);
  }, [isPending]);

  const filteredAccounts = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return accounts;
    return accounts.filter((a) => a.email.toLowerCase().includes(q));
  }, [accounts, query]);

  if (accounts.length <= 1) return null;

  const onChange = (email: string) => {
    if (email === selected) {
      setOpen(false);
      return;
    }
    setSelected(email);
    const params = typeof window !== 'undefined'
      ? new URLSearchParams(window.location.search)
      : new URLSearchParams();
    if (email) {
      localStorage.setItem(ACCOUNT_STORAGE_KEY, email);
      params.set('account', email);
    } else {
      localStorage.removeItem(ACCOUNT_STORAGE_KEY);
      params.delete('account');
    }
    const query = params.toString();
    setPendingEmail(email);
    startTransition(() => {
      router.push(query ? `${pathname}?${query}` : pathname);
    });
    setOpen(false);
  };

  const currentLabel = selected || accounts[0]?.email || 'Select account';

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="group flex max-w-[320px] items-center gap-2 rounded-xl border border-border bg-background px-3 py-1.5 shadow-sm transition-colors hover:bg-accent"
        aria-label="Open account switcher"
      >
        <Mail size={14} className="text-muted-foreground" />
        <span className="min-w-0 truncate text-sm font-medium">{currentLabel}</span>
        {isPending ? (
          <Loader2 size={14} className="ml-auto animate-spin text-muted-foreground" />
        ) : (
          <ChevronDown size={14} className="ml-auto text-muted-foreground transition-transform group-hover:translate-y-[1px]" />
        )}
      </button>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Switch account</DialogTitle>
          </DialogHeader>

          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search accounts..."
            className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm outline-none placeholder:text-muted-foreground focus:border-ring"
          />

          <div className="max-h-72 space-y-1 overflow-y-auto pr-1">
            {filteredAccounts.map((a) => {
              const active = a.email === selected;
              const switching = isPending && pendingEmail === a.email;
              return (
                <button
                  key={a.id}
                  type="button"
                  onClick={() => onChange(a.email)}
                  disabled={switching}
                  className={
                    'flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-left text-sm transition-colors ' +
                    (active
                      ? 'bg-accent text-foreground'
                      : 'hover:bg-accent/70')
                  }
                >
                  {switching ? (
                    <Loader2 size={14} className="shrink-0 animate-spin text-muted-foreground" />
                  ) : active ? (
                    <Check size={14} className="shrink-0 text-foreground" />
                  ) : (
                    <span className="h-[14px] w-[14px] shrink-0" />
                  )}
                  <span className="truncate">{a.email}</span>
                  {active && <span className="ml-auto text-xs text-muted-foreground">Current</span>}
                </button>
              );
            })}
            {filteredAccounts.length === 0 && (
              <p className="px-2 py-4 text-center text-sm text-muted-foreground">
                No accounts match your search.
              </p>
            )}
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}
