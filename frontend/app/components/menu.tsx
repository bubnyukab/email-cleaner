import {
  Sheet,
  SheetContent,
  SheetTitle,
  SheetTrigger,
} from '@/components/ui/sheet';
import { Inbox, Menu, Users } from 'lucide-react';
import Link from 'next/link';

export function NavMenu() {
  return (
    <Sheet>
      <SheetTrigger asChild>
        <button className="mr-2 -ml-1 cursor-pointer rounded-full p-2 hover:bg-gray-100">
          <Menu size={20} />
        </button>
      </SheetTrigger>
      <SheetContent
        side="left"
        className="w-[300px] transition-transform duration-200 ease-out data-[state=open]:duration-200 data-[state=open]:ease-out sm:w-[400px]"
      >
        <SheetTitle>Menu</SheetTitle>
        <nav className="mt-4 flex flex-col space-y-4">
          <Link
            href="/inbox"
            className="flex items-center space-x-2 rounded p-2 text-gray-700 hover:bg-gray-100"
          >
            <Inbox size={20} />
            <span>Inbox Overview</span>
          </Link>
          <Link
            href="/senders"
            className="flex items-center space-x-2 rounded p-2 text-gray-700 hover:bg-gray-100"
          >
            <Users size={20} />
            <span>Sender Groups</span>
          </Link>
        </nav>
      </SheetContent>
    </Sheet>
  );
}
