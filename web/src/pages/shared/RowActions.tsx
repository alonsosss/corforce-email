import type { ReactNode } from 'react';
import { Button } from '@/design/components';
import { IconEdit, IconTrash } from '@/design/icons';
import { t } from '@/i18n';

export interface RowActionsProps {
  onEdit?: () => void;
  onDelete?: () => void;
  children?: ReactNode;
}

/** Acciones de una fila. Solo aparecen las que el llamador habilita por permiso y estado. */
export function RowActions({ onEdit, onDelete, children }: RowActionsProps) {
  if (!onEdit && !onDelete && !children) return null;
  return (
    <div className="cf-table__actions">
      {children}
      {onEdit ? (
        <Button size="sm" variant="ghost" iconOnly icon={<IconEdit size={14} />} onClick={onEdit}>
          {t('common.edit')}
        </Button>
      ) : null}
      {onDelete ? (
        <Button
          size="sm"
          variant="ghost"
          iconOnly
          icon={<IconTrash size={14} />}
          onClick={onDelete}
        >
          {t('common.delete')}
        </Button>
      ) : null}
    </div>
  );
}
