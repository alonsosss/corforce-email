import { useState } from 'react';
import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { normalizeEmail } from '@/lib/mailAddress';
import { t } from '@/i18n';
import { ChipsInput } from './ChipsInput';

function Harness({ initial = [] }: { initial?: string[] }) {
  const [values, setValues] = useState<string[]>(initial);
  return (
    <>
      <label htmlFor="destinos">destinos</label>
      <ChipsInput
        id="destinos"
        values={values}
        onChange={setValues}
        normalize={normalizeEmail}
        removeLabel={(value) => t('common.removeValue', { value })}
        rejectedLabel={(rejected) =>
          t('validation.invalidAddresses', { list: rejected.join(', ') })
        }
      />
      <output data-testid="goto">{values.join(',')}</output>
    </>
  );
}

describe('ChipsInput con destinos de alias', () => {
  it('confirma con coma y con Enter, normalizando cada direccion', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const input = screen.getByLabelText('destinos');

    await user.type(input, 'Ana@Empresa.pe,');
    await user.type(input, 'luis@empresa.pe{Enter}');

    expect(screen.getByTestId('goto')).toHaveTextContent('ana@empresa.pe,luis@empresa.pe');
    expect(input).toHaveValue('');
  });

  it('deja en el campo lo que no es una direccion, con su error', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const input = screen.getByLabelText('destinos');

    await user.type(input, 'no-valida{Enter}');

    expect(screen.getByTestId('goto')).toHaveTextContent('');
    expect(input).toHaveValue('no-valida');
    expect(screen.getByRole('alert')).toHaveTextContent(
      t('validation.invalidAddresses', { list: 'no-valida' }),
    );
  });

  it('no repite un destino que ya esta en la lista', async () => {
    const user = userEvent.setup();
    render(<Harness initial={['ana@empresa.pe']} />);

    await user.type(screen.getByLabelText('destinos'), 'ANA@empresa.pe{Enter}');

    expect(screen.getByTestId('goto')).toHaveTextContent(/^ana@empresa\.pe$/);
  });

  it('quita una ficha con su boton y la ultima con retroceso', async () => {
    const user = userEvent.setup();
    render(<Harness initial={['a@x.pe', 'b@x.pe', 'c@x.pe']} />);

    await user.click(
      screen.getByRole('button', { name: t('common.removeValue', { value: 'a@x.pe' }) }),
    );
    expect(screen.getByTestId('goto')).toHaveTextContent('b@x.pe,c@x.pe');

    await user.click(screen.getByLabelText('destinos'));
    await user.keyboard('{Backspace}');
    expect(screen.getByTestId('goto')).toHaveTextContent(/^b@x\.pe$/);
  });
});
