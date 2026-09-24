import { useState } from 'react';
import { describe, expect, it } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { t, tEnum } from '@/i18n';
import { automationsMetaFixture as meta } from './metaFixture';
import { WorkflowEditor } from './WorkflowEditor';
import { emptyDraft, type WorkflowDraft } from './workflowDraft';
import type { WorkflowOptions } from './workflowOptions';

const options: WorkflowOptions = {
  templates: null,
  lists: null,
  campaigns: null,
  segments: [{ id: '0b6c0d2e-9a5f-4a57-8a0e-2f7f1c3d4e5a', name: 'Clientes vip' }],
  catalog: null,
};

function Harness({ readOnly = false }: { readOnly?: boolean }) {
  const [draft, setDraft] = useState<WorkflowDraft>(() => emptyDraft(meta));
  return (
    <>
      <WorkflowEditor
        meta={meta}
        options={options}
        draft={draft}
        onChange={setDraft}
        errors={{}}
        readOnly={readOnly}
      />
      <output data-testid="steps">
        {JSON.stringify(draft.steps.map((s) => [s.id, s.type, s.next, s.then, s.else]))}
      </output>
    </>
  );
}

const steps = () => JSON.parse(screen.getByTestId('steps').textContent ?? '[]') as string[][];
const slot = (where: string) =>
  screen.getByRole('button', { name: t('automations.canvas.insert', { where }) });

describe('editor visual del flujo', () => {
  it('anade pasos eligiendolos en la paleta y pulsando un hueco', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole('button', { name: tEnum('automations.stepType', 'wait') }));
    await user.click(slot(t('automations.canvas.afterTrigger')));
    expect(steps()).toEqual([['s1', 'wait', '', '', '']]);

    await user.click(screen.getByRole('button', { name: tEnum('automations.stepType', 'branch') }));
    await user.click(slot(t('automations.canvas.port.next')));
    expect(steps()).toEqual([
      ['s1', 'wait', 's2', '', ''],
      ['s2', 'branch', '', '', ''],
    ]);
    // La rama ofrece sus dos salidas como huecos.
    expect(slot(t('automations.canvas.port.then'))).toBeInTheDocument();
    expect(slot(t('automations.canvas.port.else'))).toBeInTheDocument();
    // Queda seleccionada y su panel edita la condicion.
    expect(
      screen.getAllByLabelText(t('automations.condition.kindLabel'), { exact: false }).length,
    ).toBeGreaterThan(0);
  });

  it('acepta un paso arrastrado desde la paleta', () => {
    render(<Harness />);
    const data = new Map<string, string>();
    const dataTransfer = {
      setData: (k: string, v: string) => data.set(k, v),
      getData: (k: string) => data.get(k) ?? '',
      get types() {
        return [...data.keys()];
      },
      effectAllowed: '',
      dropEffect: '',
    };
    fireEvent.dragStart(
      screen.getByRole('button', { name: tEnum('automations.stepType', 'send_email') }),
      {
        dataTransfer,
      },
    );
    const target = slot(t('automations.canvas.afterTrigger'));
    fireEvent.dragOver(target, { dataTransfer });
    fireEvent.drop(target, { dataTransfer });
    expect(steps()).toEqual([['s1', 'send_email', '', '', '']]);
  });

  it('avisa en vivo de un flujo invalido y quita pasos reenganchando', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const add = async (type: string, where: string) => {
      await user.click(screen.getByRole('button', { name: tEnum('automations.stepType', type) }));
      await user.click(slot(where));
    };
    await add('wait', t('automations.canvas.afterTrigger'));
    await add('wait', t('automations.canvas.port.next'));
    // El paso seleccionado (s2) apunta de vuelta a s1: ciclo.
    await user.selectOptions(screen.getByLabelText(t('automations.canvas.port.next')), 's1');
    expect(screen.getByText(t('automations.canvas.issues'))).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText(t('automations.canvas.port.next')), '');
    expect(screen.queryByText(t('automations.canvas.issues'))).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: t('automations.canvas.remove') }));
    expect(steps()).toEqual([['s1', 'wait', '', '', '']]);
  });

  it('en solo lectura no ofrece paleta ni huecos', () => {
    render(<Harness readOnly />);
    expect(screen.queryByRole('toolbar')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Anadir un paso/ })).not.toBeInTheDocument();
  });

  it('el disparador por fecha pide atributo, hora y zona y activa la reentrada', async () => {
    const user = userEvent.setup();
    const { container } = render(<Harness />);
    const byId = (id: string) => container.querySelector(`#${id}`) as HTMLElement;
    await user.selectOptions(byId('workflow-trigger'), 'contact.date');
    expect(byId('workflow-date-attribute')).toBeInTheDocument();
    expect(byId('workflow-hour')).toBeInTheDocument();
    expect(byId('workflow-timezone')).toBeInTheDocument();
    expect(screen.getByLabelText(t('automations.editor.reEntry'))).toBeChecked();
  });
});
