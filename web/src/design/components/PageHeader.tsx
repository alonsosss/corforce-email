import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { IconChevronLeft } from '../icons';

export interface PageHeaderProps {
  title: string;
  description?: ReactNode;
  actions?: ReactNode;
  back?: { to: string; label: string };
}

export function PageHeader({ title, description, actions, back }: PageHeaderProps) {
  return (
    <div className="cf-page-header">
      <div>
        {back ? (
          <Link to={back.to} className="cf-page-header__back">
            <IconChevronLeft size={16} />
            {back.label}
          </Link>
        ) : null}
        <h1 className="cf-page-header__title">{title}</h1>
        {description ? <p className="cf-page-header__description">{description}</p> : null}
      </div>
      {actions ? <div className="cf-page-header__actions">{actions}</div> : null}
    </div>
  );
}
