import type { Metadata } from 'next';
import { Inter } from 'next/font/google';
import { Toaster } from 'sonner';
import './globals.css';

const inter = Inter({ subsets: ['latin'] });

export const metadata: Metadata = {
  title: 'Email Cleaner',
  description: 'Sender-centric Gmail cleaner.',
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en" className={`bg-white text-gray-800 ${inter.className}`}>
      <body className="h-screen">
        <main className="grow overflow-hidden">{children}</main>
        <Toaster closeButton />
      </body>
    </html>
  );
}
