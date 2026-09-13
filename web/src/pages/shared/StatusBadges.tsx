import type { ActiveState } from '@/api/mailDirectory';
import { Badge, type BadgeTone } from '@/design/components';
import { t, tEnum } from '@/i18n';

export function ActiveBadge({ active }: { active: boolean }) {
  return active ? (
    <Badge tone="success">{t('common.active')}</Badge>
  ) : (
    <Badge>{t('common.inactive')}</Badge>
  );
}

export function activeStateTone(state: ActiveState): BadgeTone {
  if (state === 1) return 'success';
  if (state === 2) return 'warning';
  return 'neutral';
}

/** Estado tri-estado de buzones y aliases (activo, solo recibe, inactivo). */
export function ActiveStateBadge({ state }: { state: ActiveState }) {
  return <Badge tone={activeStateTone(state)}>{tEnum('mail.active', String(state))}</Badge>;
}

export function YesNo({ value }: { value: boolean }) {
  return <>{value ? t('common.yes') : t('common.no')}</>;
}
