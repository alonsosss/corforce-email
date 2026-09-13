import { useState } from 'react';
import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { t, tEnum } from '@/i18n';
import { catalogFixture as catalog } from './catalogFixture';
import { SegmentEditor } from './SegmentEditor';
import { fromDefinition, toDefinition, type DraftGroup } from './segmentDraft';

function Harness({ initial }: { initial: DraftGroup }) {
  const [draft, setDraft] = useState(initial);
  const built = toDefinition(draft, catalog);
  return (
    <>
      <SegmentEditor
        catalog={catalog}
        value={draft}
        onChange={setDraft}
        errors={{}}
        lists={[{ id: '0b6c0d2e-9a5f-4a57-8a0e-2f7f1c3d4e5a', name: 'Clientes' }]}
      />
      <output data-testid="definition">{JSON.stringify(built.definition)}</output>
    </>
  );
}

const start = () =>
  fromDefinition({ match: 'all', rules: [{ field: 'email', op: 'contains', value: 'acme' }] });

describe('editor de segmentos', () => {
  it('construye condiciones con los campos y operadores del catalogo', async () => {
    const user = userEvent.setup();
    render(<Harness initial={start()} />);

    await user.click(screen.getByRole('button', { name: t('segments.editor.addCondition') }));
    const fields = screen.getAllByRole('combobox', { name: t('segments.editor.field') });
    await user.selectOptions(fields[1] as HTMLElement, 'status');

    const operators = screen.getAllByRole('combobox', { name: t('segments.editor.operator') });
    expect(
      within(operators[1] as HTMLElement)
        .getAllByRole('option')
        .map((o) => o.getAttribute('value')),
    ).toEqual(['eq', 'neq', 'in']);
    const values = screen.getAllByRole('combobox', { name: t('segments.editor.value') });
    await user.selectOptions(values[0] as HTMLElement, 'bounced');
    expect(
      screen.getByRole('option', { name: tEnum('contacts.status', 'bounced') }),
    ).toBeInTheDocument();

    expect(JSON.parse(screen.getByTestId('definition').textContent ?? 'null')).toEqual({
      match: 'all',
      rules: [
        { field: 'email', op: 'contains', value: 'acme' },
        { field: 'status', op: 'eq', value: 'bounced' },
      ],
    });
  });

  it('los grupos se anidan hasta la profundidad del catalogo', async () => {
    const user = userEvent.setup();
    render(<Harness initial={start()} />);
    await user.click(screen.getByRole('button', { name: t('segments.editor.addGroup') }));

    // El boton del grupo anidado aparece antes que el de la raiz en el documento.
    const addGroup = screen.getAllByRole('button', { name: t('segments.editor.addGroup') });
    expect(addGroup.map((b) => (b as HTMLButtonElement).disabled)).toEqual([true, false]);

    // La condicion nueva del grupo aun no tiene valor: no hay definicion hasta escribirlo.
    expect(screen.getByTestId('definition').textContent).toBe('null');
    const values = screen.getAllByRole('textbox', { name: t('segments.editor.value') });
    await user.type(values[1] as HTMLElement, 'acme.test');
    const definition = JSON.parse(screen.getByTestId('definition').textContent ?? 'null');
    expect(definition.rules[1]).toEqual({
      match: 'all',
      rules: [{ field: 'email', op: 'eq', value: 'acme.test' }],
    });
  });

  it('un operador sin valor no pide valor y quitar una condicion la saca de la definicion', async () => {
    const user = userEvent.setup();
    render(<Harness initial={start()} />);
    await user.selectOptions(
      screen.getByRole('combobox', { name: t('segments.editor.field') }),
      'tags',
    );
    await user.selectOptions(
      screen.getByRole('combobox', { name: t('segments.editor.operator') }),
      'exists',
    );
    expect(screen.queryByRole('textbox', { name: t('segments.editor.value') })).toBeNull();
    expect(JSON.parse(screen.getByTestId('definition').textContent ?? 'null')).toEqual({
      match: 'all',
      rules: [{ field: 'tags', op: 'exists' }],
    });

    await user.click(screen.getByRole('button', { name: t('segments.editor.removeCondition') }));
    expect(screen.getByTestId('definition').textContent).toBe('null');
  });
});
