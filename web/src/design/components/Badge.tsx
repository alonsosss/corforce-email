import type { ReactNode } from 'react';

export type BadgeTone = 'neutral' | 'success' | 'warning' | 'danger' | 'info' | 'accent';

export interface BadgeProps {
  tone?: BadgeTone;
  children: ReactNode;
}

export function Badge({ tone = 'neutral', children }: BadgeProps) {
  const classes = ['cf-badge', tone !== 'neutral' ? `cf-badge--${tone}` : '']
    .filter(Boolean)
    .join(' ');
  return <span className={classes}>{children}</span>;
}
