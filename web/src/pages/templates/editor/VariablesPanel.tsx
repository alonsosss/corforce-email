import type { ReservedVariable, VariableType } from '@/api/templates';
import { Badge, Button, CopyButton } from '@/design/components';
import { t, tEnum } from '@/i18n';
import { isVariableName, type VariableDraft } from '../variables';
import { variableToken } from './blocks';
import type { EngineSelection } from './engine';

interface Row {
  name: string;
  type: VariableType;
  reserved: boolean;
}

export interface VariablesPanelProps {
  declared: VariableDraft[];
  reserved: ReservedVariable[];
  selection: EngineSelection;
  onInsertText: (token: string) => void;
  onAppendToLink: (token: string) => void;
}

/**
 * Variables que la plantilla puede usar: las declaradas en el documento y las reservadas
 * del servicio. Se insertan en el cursor del texto que se edita o al final del enlace del
 * elemento seleccionado (una URL con {{.variable}}).
 */
export function VariablesPanel({
  declared,
  reserved,
  selection,
  onInsertText,
  onAppendToLink,
}: VariablesPanelProps) {
  const rows: Row[] = [
    ...declared
      .filter((d) => isVariableName(d.name))
      .map((d) => ({ name: d.name, type: d.type, reserved: false })),
    ...reserved.map((r) => ({ name: r.name, type: r.type, reserved: true })),
  ];
  return (
    <div className="cf-stack" style={{ gap: 'var(--cf-space-3)' }}>
      <span className="cf-text-sm cf-text-secondary">{t('templates.editor.variablesHint')}</span>
      {!declared.length ? (
        <span className="cf-text-sm cf-text-secondary">
          {t('templates.editor.variablesDeclareHint')}
        </span>
      ) : null}
      <ul className="cf-var-list">
        {rows.map((row) => {
          const token = variableToken(row.name);
          return (
            <li key={`${row.reserved ? 'r' : 'd'}-${row.name}`} className="cf-var-list__item">
              <div className="cf-var-list__head">
                <code className="cf-mono">{token}</code>
                <Badge tone={row.reserved ? 'neutral' : 'accent'}>
                  {row.reserved
                    ? t('templates.editor.variableReserved')
                    : tEnum('templates.variableType', row.type)}
                </Badge>
              </div>
              <div className="cf-var-list__actions">
                <Button
                  size="sm"
                  disabled={!selection.editingText}
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => onInsertText(token)}
                >
                  {t('templates.editor.insertInText')}
                </Button>
                <Button
                  size="sm"
                  disabled={!selection.hasLink}
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => onAppendToLink(token)}
                >
                  {t('templates.editor.insertInLink')}
                </Button>
                <CopyButton value={token} />
              </div>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
