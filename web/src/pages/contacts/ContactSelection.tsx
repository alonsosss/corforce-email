import { useCallback, useState } from 'react';
import { Checkbox, type Column } from '@/design/components';
import { t } from '@/i18n';

/** Seleccion de filas de una pagina para las acciones en bloque. */
export function useRowSelection() {
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const toggle = useCallback((id: string) => {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);
  const setAll = useCallback((ids: readonly string[], on: boolean) => {
    setSelected(on ? new Set(ids) : new Set());
  }, []);
  const clear = useCallback(() => setSelected(new Set()), []);
  return { selected, toggle, setAll, clear };
}

/**
 * Columna de casillas. El clic en la casilla no abre la fila: la tabla navega al detalle
 * al pulsar la fila.
 */
export function selectionColumn<T>(
  rows: readonly T[],
  idOf: (row: T) => string,
  labelOf: (row: T) => string,
  selection: ReturnType<typeof useRowSelection>,
): Column<T> {
  const ids = rows.map(idOf);
  const all = ids.length > 0 && ids.every((id) => selection.selected.has(id));
  return {
    key: 'select',
    width: '40px',
    header: (
      <Checkbox
        aria-label={t('common.selectPage')}
        checked={all}
        onChange={(e) => selection.setAll(ids, e.target.checked)}
      />
    ),
    render: (row) => (
      <span onClick={(e) => e.stopPropagation()}>
        <Checkbox
          aria-label={t('common.selectRow', { name: labelOf(row) })}
          checked={selection.selected.has(idOf(row))}
          onChange={() => selection.toggle(idOf(row))}
        />
      </span>
    ),
  };
}
