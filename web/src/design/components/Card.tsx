import type { ReactNode } from 'react';

export interface CardProps {
  title?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  flush?: boolean;
  className?: string;
  children: ReactNode;
}

export function Card({
  title,
  description,
  actions,
  flush = false,
  className,
  children,
}: CardProps) {
  return (
    <section className={['cf-card', className ?? ''].filter(Boolean).join(' ')}>
      {title || actions ? (
        <header className="cf-card__header">
          <div>
            {title ? <h2 className="cf-card__title">{title}</h2> : null}
            {description ? <p className="cf-card__description">{description}</p> : null}
          </div>
          {actions ? <div className="cf-page-header__actions">{actions}</div> : null}
        </header>
      ) : null}
      <div
        className={['cf-card__body', flush ? 'cf-card__body--flush' : ''].filter(Boolean).join(' ')}
      >
        {children}
      </div>
    </section>
  );
}
