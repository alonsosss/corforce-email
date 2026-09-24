import type { FormStatus } from '@/api/forms';
import type { BadgeTone } from '@/design/components';

export function formStatusTone(status: FormStatus): BadgeTone {
  return status === 'active' ? 'success' : 'neutral';
}
