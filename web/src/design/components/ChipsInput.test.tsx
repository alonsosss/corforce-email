import { useState } from 'react';
import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { normalizeEmail } from '@/lib/mailAddress';
import { t } from '@/i18n';
import { ChipsInput, type ChipSuggestion } from './ChipsInput';

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

function Suggesting({ suggestions }: { suggestions: ChipSuggestion[] }) {
  const [values, setValues] = useState<string[]>(['eva@x.pe']);
  const [query, setQuery] = useState('');
  return (
    <>
      <label htmlFor="para">para</label>
      <ChipsInput
        id="para"
        values={values}
        onChange={setValues}
        normalize={normalizeEmail}
        removeLabel={(value) => t('common.removeValue', { value })}
        rejectedLabel={(rejected) => rejected.join(',')}
        suggestions={suggestions}
        onQueryChange={setQuery}
        suggestionsLabel="propuestas"
      />
      <output data-testid="valores">{values.join(',')}</output>
      <output data-testid="consulta">{query}</output>
    </>
  );
}

describe('ChipsInput con propuestas', () => {
  const SUGGESTIONS: ChipSuggestion[] = [
    { value: 'luis@x.pe', label: 'Luis', detail: 'luis@x.pe' },
    { value: 'lucia@x.pe', label: 'Lucia' },
    { value: 'eva@x.pe', label: 'Eva' },
  ];

  it('se recorren con flechas y Enter elige; las ya anadidas no se proponen', async () => {
    const user = userEvent.setup();
    render(<Suggesting suggestions={SUGGESTIONS} />);
    const input = screen.getByRole('combobox', { name: 'para' });

    await user.type(input, 'lu');
    expect(screen.getByTestId('consulta')).toHaveTextContent('lu');
    const list = screen.getByRole('listbox', { name: 'propuestas' });
    expect(within(list).getAllByRole('option')).toHaveLength(2);
    expect(input).toHaveAttribute('aria-expanded', 'true');

    await user.keyboard('{ArrowDown}{ArrowDown}');
    expect(input.getAttribute('aria-activedescendant')).toBe(
      within(list).getAllByRole('option')[1]?.id,
    );
    await user.keyboard('{Enter}');
    expect(screen.getByTestId('valores')).toHaveTextContent('eva@x.pe,lucia@x.pe');
    expect(input).toHaveValue('');
  });

  it('Escape las cierra y Enter confirma lo escrito tal cual', async () => {
    const user = userEvent.setup();
    render(<Suggesting suggestions={SUGGESTIONS} />);
    const input = screen.getByRole('combobox', { name: 'para' });

    await user.type(input, 'luis@otro.pe');
    await user.keyboard('{Escape}');
    expect(input).toHaveAttribute('aria-expanded', 'false');
    await user.keyboard('{Enter}');
    expect(screen.getByTestId('valores')).toHaveTextContent('eva@x.pe,luis@otro.pe');
  });
});
