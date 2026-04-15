'use server';

import { revalidatePath } from 'next/cache';
import { syncGmailInbox } from './client';

type ActionState = { error: string | null; success: boolean };

const initialErrorState: ActionState = { error: null, success: false };

export async function syncInboxAction(
  _: ActionState = initialErrorState,
  __: FormData,
): Promise<ActionState> {
  try {
    await syncGmailInbox();
    revalidatePath('/inbox');
    revalidatePath('/senders');
    return { error: null, success: true };
  } catch (error) {
    console.error('Failed to sync Gmail inbox:', error);
    return { error: 'Failed to sync Gmail inbox', success: false };
  }
}
