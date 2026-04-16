'use client';

import {
  Sheet,
  SheetContent,
  SheetTitle,
  SheetTrigger,
} from '@/components/ui/sheet';
import { Inbox, Menu, Settings, Users } from 'lucide-react';
import Link from 'next/link';
import { usePathname } from 'next/navigation';
import { useEffect, useState } from 'react';
import { ThemeToggle } from './theme-toggle';

export function NavMenu() {
  const [open, setOpen] = useState(false);
  const pathname = usePathname();

  useEffect(() => {
    setOpen(false);
  }, [pathname]);

  return (
    <Sheet open={open} onOpenChange={setOpen}>
      <SheetTrigger asChild>
        <button className="mr-2 -ml-1 cursor-pointer rounded-full p-2 hover:bg-accent">
          <Menu size={20} />
        </button>
      </SheetTrigger>
      <SheetContent
        side="left"
        className="w-[300px] transition-transform duration-200 ease-out data-[state=open]:duration-200 data-[state=open]:ease-out sm:w-[400px]"
      >
        <SheetTitle>Menu</SheetTitle>
        <nav className="mt-4 flex flex-1 flex-col space-y-4">
          <Link
            href="/inbox"
            className="flex items-center space-x-2 rounded p-2 text-foreground hover:bg-accent"
          >
            <Inbox size={20} />
            <span>Inbox Overview</span>
          </Link>
          <Link
            href="/senders"
            className="flex items-center space-x-2 rounded p-2 text-foreground hover:bg-accent"
          >
            <Users size={20} />
            <span>Sender Groups</span>
          </Link>
          <Link
            href="/settings"
            className="flex items-center space-x-2 rounded p-2 text-foreground hover:bg-accent"
          >
            <Settings size={20} />
            <span>Settings</span>
          </Link>
          <div className="mt-auto border-t border-border pt-4">
            <ThemeToggle />
          </div>
        </nav>
      </SheetContent>
    </Sheet>
  );
}
