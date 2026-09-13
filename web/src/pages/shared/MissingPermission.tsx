import { Card, EmptyState, PageHeader } from '@/design/components';
import { IconLock } from '@/design/icons';
import { t } from '@/i18n';

/**
 * Pantalla de un modulo al que el usuario entra, pero cuyo permiso de lectura concreto no
 * tiene: se dice en lugar de pedir datos que el handler va a negar con un 403.
 */
export function MissingPermission({ title, description }: { title: string; description?: string }) {
  return (
    <div>
      <PageHeader title={title} description={description} />
      <Card>
        <EmptyState icon={<IconLock size={32} />} title={t('common.missingPermission')} />
      </Card>
    </div>
  );
}
