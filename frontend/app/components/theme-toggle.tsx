'use client';

import { Moon, Sun, Monitor } from 'lucide-react';
import { useEffect, useState } from 'react';

type Theme = 'light' | 'dark' | 'system';

function applyTheme(theme: Theme) {
  const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
  const isDark = theme === 'dark' || (theme === 'system' && prefersDark);
  document.documentElement.classList.toggle('dark', isDark);
}

export function ThemeToggle() {
  const [theme, setTheme] = useState<Theme>('system');

  useEffect(() => {
    const stored = localStorage.getItem('theme') as Theme | null;
    if (stored === 'light' || stored === 'dark' || stored === 'system') {
      setTheme(stored);
      applyTheme(stored);
    } else {
      applyTheme('system');
    }
  }, []);

  useEffect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: dark)');
    const handler = () => {
      if (theme === 'system') applyTheme('system');
    };
    mq.addEventListener('change', handler);
    return () => mq.removeEventListener('change', handler);
  }, [theme]);

  const cycle = () => {
    const order: Theme[] = ['system', 'light', 'dark'];
    const next = order[(order.indexOf(theme) + 1) % order.length];
    setTheme(next);
    localStorage.setItem('theme', next);
    applyTheme(next);
  };

  const Icon = theme === 'dark' ? Moon : theme === 'light' ? Sun : Monitor;
  const label =
    theme === 'dark' ? 'Dark' : theme === 'light' ? 'Light' : 'System';

  return (
    <button
      type="button"
      onClick={cycle}
      className="flex items-center space-x-2 rounded p-2 text-foreground hover:bg-accent"
      title={`Theme: ${label}`}
    >
      <Icon size={20} />
      <span>{label}</span>
    </button>
  );
}
