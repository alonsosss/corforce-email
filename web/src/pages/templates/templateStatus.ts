import type { TemplateKind, TemplateStatus, VersionStatus } from '@/api/templates';
import type { BadgeTone } from '@/design/components';

export function templateStatusTone(status: TemplateStatus): BadgeTone {
  return status === 'active' ? 'success' : 'neutral';
}

export function templateKindTone(kind: TemplateKind): BadgeTone {
  return kind === 'transactional' ? 'info' : 'accent';
}

export function versionStatusTone(status: VersionStatus): BadgeTone {
  if (status === 'published') return 'success';
  if (status === 'draft') return 'warning';
  return 'neutral';
}
