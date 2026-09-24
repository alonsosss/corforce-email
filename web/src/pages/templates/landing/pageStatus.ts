import type { LandingPageStatus, PageVersionStatus } from '@/api/pages';
import type { BadgeTone } from '@/design/components';

export function pageStatusTone(status: LandingPageStatus): BadgeTone {
  return status === 'active' ? 'success' : 'neutral';
}

export function pageVersionTone(status: PageVersionStatus): BadgeTone {
  if (status === 'published') return 'success';
  if (status === 'draft') return 'warning';
  return 'neutral';
}

/** Hay una version publicada servida en la URL publica. */
export function isPublished(page: { current_version: number }): boolean {
  return page.current_version > 0;
}
