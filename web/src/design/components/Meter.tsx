export interface MeterProps {
  /** Proporcion entre 0 y 1. */
  ratio: number;
  label: string;
}

const WARNING_RATIO = 0.75;
const DANGER_RATIO = 0.9;

export function Meter({ ratio, label }: MeterProps) {
  const safe = Number.isFinite(ratio) ? Math.min(Math.max(ratio, 0), 1) : 0;
  const percent = Math.round(safe * 100);
  const tone = safe >= DANGER_RATIO ? 'danger' : safe >= WARNING_RATIO ? 'warning' : '';
  return (
    <div
      className="cf-meter"
      role="meter"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={percent}
    >
      <div
        className={['cf-meter__fill', tone ? `cf-meter__fill--${tone}` : '']
          .filter(Boolean)
          .join(' ')}
        style={{ width: `${percent}%` }}
      />
    </div>
  );
}
