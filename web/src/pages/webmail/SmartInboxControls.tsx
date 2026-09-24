import { Tabs } from '@/design/components';
import { t } from '@/i18n';
import type { InboxTab } from './smartInbox';

/** Conmutador entre la vista por conversaciones y la vista por mensajes. */
export function ViewToggle({
  threads,
  onChange,
}: {
  threads: boolean;
  onChange: (threads: boolean) => void;
}) {
  return (
    <div className="cf-wm-viewtoggle" role="group" aria-label={t('webmail.view.label')}>
      <button
        type="button"
        className="cf-wm-viewtoggle__option"
        aria-pressed={threads}
        onClick={() => onChange(true)}
      >
        {t('webmail.view.threads')}
      </button>
      <button
        type="button"
        className="cf-wm-viewtoggle__option"
        aria-pressed={!threads}
        onClick={() => onChange(false)}
      >
        {t('webmail.view.messages')}
      </button>
    </div>
  );
}

/** Pestanas de la bandeja inteligente. Con una busqueda activa no filtran, y se avisa. */
export function InboxTabs({
  tabs,
  value,
  searching,
  onChange,
}: {
  tabs: readonly InboxTab[];
  value: string;
  searching: boolean;
  onChange: (tab: string) => void;
}) {
  if (!tabs.length) return null;
  return (
    <div className="cf-wm-inboxtabs">
      <Tabs
        items={tabs.map((tab) => ({ id: tab.id, label: t(tab.label) }))}
        value={value}
        onChange={onChange}
        label={t('webmail.tabs.label')}
      />
      {searching ? <p className="cf-field__hint">{t('webmail.tabs.searchAll')}</p> : null}
    </div>
  );
}
