import { useId } from 'react';
import { formatDate } from '@/lib/format';
import { getLocale, t } from '@/i18n';

// Grafico de lineas propio en SVG: sin librerias, con los colores del tema y una tabla con
// los mismos datos para quien no puede ver el grafico.

export interface ChartSeries {
  key: string;
  label: string;
  values: number[];
}

const WIDTH = 720;
const HEIGHT = 260;
const PAD = { top: 16, right: 16, bottom: 32, left: 56 };
const TICKS = [0, 0.25, 0.5, 0.75, 1];
const MAX_POINT_MARKERS = 62;
const PALETTE_SIZE = 5;

/** Techo del eje: 1, 2 o 5 por una potencia de diez, por encima del maximo. */
export function niceMax(value: number): number {
  if (!Number.isFinite(value) || value <= 0) return 1;
  const magnitude = 10 ** Math.floor(Math.log10(value));
  const n = value / magnitude;
  const step = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10;
  return step * magnitude;
}

/** Los dias vienen como AAAA-MM-DD del servicio; se muestran sin cambiarlos de zona. */
function dayLabel(day: string): string {
  return formatDate(`${day}T12:00:00Z`);
}

export function SeriesChart({
  days,
  series,
  title,
}: {
  days: readonly string[];
  series: readonly ChartSeries[];
  title: string;
}) {
  const titleId = useId();
  const descId = useId();
  const format = new Intl.NumberFormat(getLocale(), { maximumFractionDigits: 1 });
  const innerW = WIDTH - PAD.left - PAD.right;
  const innerH = HEIGHT - PAD.top - PAD.bottom;
  const max = niceMax(Math.max(0, ...series.flatMap((s) => s.values)));
  const x = (i: number) =>
    days.length <= 1 ? PAD.left + innerW / 2 : PAD.left + (i * innerW) / (days.length - 1);
  const y = (v: number) => PAD.top + innerH - (Math.max(v, 0) / max) * innerH;
  const labelIndexes = [...new Set([0, Math.floor((days.length - 1) / 2), days.length - 1])].filter(
    (i) => i >= 0,
  );
  const first = days[0];
  const last = days[days.length - 1];

  return (
    <figure className="cf-chart">
      <svg
        className="cf-chart__svg"
        viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
        role="img"
        aria-labelledby={`${titleId} ${descId}`}
      >
        <title id={titleId}>{title}</title>
        <desc id={descId}>
          {first && last
            ? t('analytics.chart.summary', {
                from: dayLabel(first),
                to: dayLabel(last),
                series: series.map((s) => s.label).join(', '),
              })
            : t('analytics.chart.empty')}
        </desc>
        {TICKS.map((f) => (
          <g key={f}>
            <line
              className="cf-chart__grid"
              x1={PAD.left}
              x2={WIDTH - PAD.right}
              y1={y(f * max)}
              y2={y(f * max)}
            />
            <text className="cf-chart__tick" x={PAD.left - 8} y={y(f * max) + 4} textAnchor="end">
              {format.format(f * max)}
            </text>
          </g>
        ))}
        {labelIndexes.map((i) => (
          <text
            key={i}
            className="cf-chart__tick"
            x={x(i)}
            y={HEIGHT - 8}
            textAnchor={
              i === 0 && days.length > 1
                ? 'start'
                : i === days.length - 1 && days.length > 1
                  ? 'end'
                  : 'middle'
            }
          >
            {dayLabel(days[i] ?? '')}
          </text>
        ))}
        {series.map((s, index) => (
          <g key={s.key} className={`cf-chart__series cf-chart__series--${index % PALETTE_SIZE}`}>
            <polyline
              className="cf-chart__line"
              fill="none"
              points={s.values.map((v, i) => `${x(i).toFixed(1)},${y(v).toFixed(1)}`).join(' ')}
            />
            {days.length <= MAX_POINT_MARKERS
              ? s.values.map((v, i) => (
                  <circle key={i} className="cf-chart__point" cx={x(i)} cy={y(v)} r={2.5} />
                ))
              : null}
          </g>
        ))}
      </svg>
      <figcaption className="cf-chart__legend">
        {series.map((s, index) => (
          <span key={s.key} className={`cf-chart__key cf-chart__series--${index % PALETTE_SIZE}`}>
            <span className="cf-chart__swatch" aria-hidden="true" />
            {s.label}
          </span>
        ))}
      </figcaption>
      <details className="cf-chart__data">
        <summary>{t('analytics.chart.showData')}</summary>
        <div className="cf-table-wrap">
          <table className="cf-table">
            <thead>
              <tr>
                <th scope="col">{t('analytics.chart.day')}</th>
                {series.map((s) => (
                  <th key={s.key} scope="col" className="cf-table__cell--right">
                    {s.label}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {days.map((day, i) => (
                <tr key={day}>
                  <td>{dayLabel(day)}</td>
                  {series.map((s) => (
                    <td key={s.key} className="cf-table__cell--right">
                      {format.format(s.values[i] ?? 0)}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </details>
    </figure>
  );
}
