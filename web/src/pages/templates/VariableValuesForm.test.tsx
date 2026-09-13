import { useState } from 'react';
import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { TemplateVariable } from '@/api/templates';
import { t } from '@/i18n';
import { VariableValuesForm } from './VariableValuesForm';
import { buildRenderVariables, initialValues } from './variables';

const variables: TemplateVariable[] = [
  { name: 'nombre', type: 'string', required: true },
  { name: 'total', type: 'number', required: false },
  { name: 'vip', type: 'boolean', required: false, default: true },
  { name: 'enlace', type: 'url', required: false },
  { name: 'correo', type: 'email', required: false, default: 'ana@empresa.pe' },
];

function Harness({ declared = variables }: { declared?: TemplateVariable[] }) {
  const [values, setValues] = useState(() => initialValues(declared));
  return (
    <>
      <VariableValuesForm idPrefix="p" variables={declared} values={values} onChange={setValues} />
      <output data-testid="payload">
        {JSON.stringify(buildRenderVariables(declared, values).variables)}
      </output>
    </>
  );
}

describe('formulario de variables de una plantilla', () => {
  it('genera el control que corresponde a cada tipo declarado', () => {
    render(<Harness />);

    expect(screen.getByLabelText(/^nombre/)).toHaveAttribute('type', 'text');
    expect(screen.getByLabelText('total')).toHaveAttribute('type', 'number');
    expect(screen.getByLabelText('enlace')).toHaveAttribute('type', 'url');
    expect(screen.getByLabelText('correo')).toHaveAttribute('type', 'email');
    const vip = screen.getByLabelText('vip');
    expect(vip).toHaveAttribute('type', 'checkbox');
    expect(vip).toBeChecked();
  });

  it('muestra el valor por defecto como pista, sin enviarlo desde la interfaz', () => {
    render(<Harness />);
    expect(screen.getByLabelText('correo')).toHaveAttribute(
      'placeholder',
      t('templates.preview.defaultPlaceholder', { value: 'ana@empresa.pe' }),
    );
    expect(screen.getByLabelText('correo')).toHaveValue('ana@empresa.pe');
  });

  it('lo escrito sale hacia /preview con el tipo de cada variable', async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.type(screen.getByLabelText(/^nombre/), 'Ana');
    await user.type(screen.getByLabelText('total'), '42');
    await user.click(screen.getByLabelText('vip'));

    expect(JSON.parse(screen.getByTestId('payload').textContent ?? '{}')).toEqual({
      nombre: 'Ana',
      total: 42,
      vip: false,
      correo: 'ana@empresa.pe',
    });
  });

  it('sin variables declaradas lo dice en lugar de pintar un formulario vacio', () => {
    render(<Harness declared={[]} />);
    expect(screen.getByText(t('templates.preview.noVariables'))).toBeInTheDocument();
  });
});
