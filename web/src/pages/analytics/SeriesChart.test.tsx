import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { niceMax, SeriesChart } from './SeriesChart';

describe('grafico de la serie diaria', () => {
  it('redondea el techo del eje a 1, 2 o 5 por potencia de diez', () => {
    expect(niceMax(0)).toBe(1);
    expect(niceMax(7)).toBe(10);
    expect(niceMax(120)).toBe(200);
    expect(niceMax(4500)).toBe(5000);
    expect(niceMax(Number.NaN)).toBe(1);
  });

  it('dibuja una linea por serie con un punto por dia y ofrece los datos como tabla', () => {
    const days = ['2026-03-01', '2026-03-02', '2026-03-03'];
    const { container } = render(
      <SeriesChart
        title="Envios"
        days={days}
        series={[
          { key: 'sent', label: 'Enviados', values: [10, 20, 5] },
          { key: 'delivered', label: 'Entregados', values: [9, 18, 5] },
        ]}
      />,
    );
    const lines = container.querySelectorAll('polyline');
    expect(lines).toHaveLength(2);
    expect(lines[0]?.getAttribute('points')?.split(' ')).toHaveLength(3);
    expect(screen.getByRole('img', { name: /Envios/ })).toBeInTheDocument();
    expect(container.querySelectorAll('tbody tr')).toHaveLength(3);
  });
});
