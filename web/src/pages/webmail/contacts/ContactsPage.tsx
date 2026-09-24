import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { webmailApi, type ContactImportResult } from '@/api/webmail';
import { useResource } from '@/hooks/useResource';
import { formatBytes } from '@/lib/quota';
import { davLimits, davMeta } from '@/webmail/catalogs';
import { davErrorMessage } from '../davErrors';
import { useAction } from '@/hooks/useAction';
import { useQuery } from '@/hooks/useQuery';
import {
  Alert,
  Button,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Input,
  Pagination,
  Skeleton,
  useToast,
} from '@/design/components';
import {
  IconChevronLeft,
  IconDownload,
  IconEdit,
  IconMail,
  IconPlus,
  IconTrash,
  IconUpload,
  IconUsers,
} from '@/design/icons';
import { saveBlob } from '@/lib/download';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { parsePositiveInt } from '../format';
import { ContactFormDialog } from './ContactFormDialog';
import { contactInitials, contactName, formatBirthday } from './contacts';

const SEARCH_DELAY_MS = 300;

function contactsHref(query: { id?: string; q?: string; page?: number }): string {
  const params = new URLSearchParams();
  if (query.q) params.set('q', query.q);
  if (query.page && query.page > 1) params.set('page', String(query.page));
  if (query.id) params.set('id', query.id);
  const qs = params.toString();
  return qs ? `${paths.webmailContacts}?${qs}` : paths.webmailContacts;
}

/** Agenda personal del buzon (CardDAV en mail-dav): lo que se cambia aqui aparece en el movil. */
export default function ContactsPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const q = params.get('q') ?? '';
  const page = parsePositiveInt(params.get('page')) ?? 1;
  const selectedId = params.get('id');
  const [text, setText] = useState(q);
  const [creating, setCreating] = useState(false);
  const [imported, setImported] = useState<ContactImportResult | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  const list = useQuery(
    (signal) => webmailApi.contacts({ q: q || undefined, page }, signal),
    [q, page],
  );

  useEffect(() => {
    const term = text.trim();
    if (term === q) return;
    const timer = window.setTimeout(
      () => navigate(contactsHref({ q: term }), { replace: true }),
      SEARCH_DELAY_MS,
    );
    return () => window.clearTimeout(timer);
  }, [text, q, navigate]);

  const meta = useResource(davMeta);
  const [importProblem, setImportProblem] = useState<string | null>(null);
  const importFile = useAction(async (file: File) => {
    const max = davLimits(meta.data).maxImportBytes;
    setImportProblem(null);
    if (max !== null && file.size > max) {
      setImportProblem(
        t('webmail.contacts.importTooLarge', {
          size: formatBytes(file.size),
          max: formatBytes(max),
        }),
      );
      return;
    }
    const result = await webmailApi.importContacts(file);
    setImported(result);
    list.reload();
  });

  const exportAll = useAction(async () => {
    const file = await webmailApi.exportContacts();
    saveBlob(file.blob, file.filename ?? t('webmail.contacts.exportFilename'));
  });

  const data = list.data;
  const items = data?.items ?? [];
  const actionError = importFile.error ?? exportAll.error;

  return (
    <div className={['cf-wm-contacts', selectedId ? 'cf-wm-contacts--reading' : ''].join(' ')}>
      <section className="cf-wm-contacts__list" aria-labelledby="wm-contacts-title">
        <div className="cf-wm-listbar">
          <div className="cf-wm-listbar__title">
            <h1 id="wm-contacts-title" className="cf-wm-listbar__heading">
              {t('webmail.contacts.title')}
            </h1>
            <div className="cf-wm-listbar__tools">
              <Button
                size="sm"
                variant="primary"
                iconOnly
                icon={<IconPlus size={16} />}
                onClick={() => setCreating(true)}
              >
                {t('webmail.contacts.new')}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                iconOnly
                icon={<IconUpload size={16} />}
                loading={importFile.busy}
                onClick={() => fileRef.current?.click()}
              >
                {t('webmail.contacts.import')}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                iconOnly
                icon={<IconDownload size={16} />}
                loading={exportAll.busy}
                onClick={() => void exportAll.run()}
              >
                {t('webmail.contacts.export')}
              </Button>
              <input
                ref={fileRef}
                type="file"
                accept=".vcf,text/vcard,text/x-vcard"
                name="vcard-import"
                hidden
                aria-hidden="true"
                aria-label={t('webmail.contacts.importFile')}
                tabIndex={-1}
                onChange={(e) => {
                  const file = e.target.files?.[0];
                  e.target.value = '';
                  if (file) void importFile.run(file);
                }}
              />
            </div>
          </div>
          <div className="cf-wm-search">
            <label htmlFor="wm-contacts-search" className="cf-visually-hidden">
              {t('webmail.contacts.search')}
            </label>
            <Input
              id="wm-contacts-search"
              type="search"
              value={text}
              placeholder={t('webmail.contacts.search')}
              onChange={(e) => setText(e.target.value)}
            />
          </div>
          {imported ? (
            <Alert tone={imported.skipped.length ? 'warning' : 'success'}>
              <p>
                {t('webmail.contacts.importDone', {
                  imported: imported.imported,
                  updated: imported.updated,
                  skipped: imported.skipped.length,
                })}
              </p>
              {imported.skipped.length ? (
                <ul className="cf-wm-skipped">
                  {imported.skipped.map((s) => (
                    <li key={s.index}>
                      {t('webmail.contacts.importSkipped', { n: s.index + 1, reason: s.reason })}
                    </li>
                  ))}
                </ul>
              ) : null}
              <Button size="sm" variant="ghost" onClick={() => setImported(null)}>
                {t('common.close')}
              </Button>
            </Alert>
          ) : null}
          {importProblem || actionError ? (
            <div className="cf-form__error" role="alert">
              {importProblem ?? davErrorMessage(actionError)}
            </div>
          ) : null}
        </div>
        <div className="cf-wm-listbody" aria-busy={list.loading || undefined}>
          {list.error && !data ? (
            <ErrorState error={list.error} onRetry={list.reload} />
          ) : !data ? (
            <div className="cf-wm-pad">
              <Skeleton lines={6} />
            </div>
          ) : items.length === 0 ? (
            <EmptyState
              icon={<IconUsers size={32} />}
              title={t(q ? 'webmail.contacts.noResults' : 'webmail.contacts.empty')}
              description={q ? undefined : t('webmail.contacts.emptyHint')}
            />
          ) : (
            <ul className="cf-wm-contact-list" aria-label={t('webmail.contacts.title')}>
              {items.map((contact) => (
                <li key={contact.id}>
                  <Link
                    to={contactsHref({ q, page, id: contact.id })}
                    className="cf-wm-contact-item"
                    aria-current={contact.id === selectedId ? 'true' : undefined}
                  >
                    <span className="cf-wm-avatar" aria-hidden="true">
                      {contactInitials(contact)}
                    </span>
                    <span className="cf-wm-contact-item__text">
                      <span className="cf-wm-contact-item__name">{contactName(contact)}</span>
                      {contact.emails[0] ? (
                        <span className="cf-wm-contact-item__detail">
                          {contact.emails[0].value}
                        </span>
                      ) : null}
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </div>
        {data && data.totalPages > 1 ? (
          <Pagination
            page={data.page}
            perPage={data.perPage}
            total={data.total}
            totalPages={data.totalPages}
            onPageChange={(next) => navigate(contactsHref({ q, page: next }))}
          />
        ) : null}
      </section>
      <section className="cf-wm-contacts__detail" aria-label={t('webmail.contacts.detail')}>
        {selectedId ? (
          <ContactDetail
            key={selectedId}
            id={selectedId}
            backHref={contactsHref({ q, page })}
            onChanged={list.reload}
            onDeleted={() => {
              list.reload();
              navigate(contactsHref({ q, page }), { replace: true });
            }}
          />
        ) : (
          <EmptyState
            icon={<IconUsers size={32} />}
            title={t('webmail.contacts.none')}
            description={t('webmail.contacts.noneHint')}
          />
        )}
      </section>
      {creating ? (
        <ContactFormDialog
          onClose={() => setCreating(false)}
          onSaved={(contact) => {
            setCreating(false);
            list.reload();
            navigate(contactsHref({ q, page, id: contact.id }));
          }}
        />
      ) : null}
    </div>
  );
}

function ContactDetail({
  id,
  backHref,
  onChanged,
  onDeleted,
}: {
  id: string;
  backHref: string;
  onChanged: () => void;
  onDeleted: () => void;
}) {
  const toast = useToast();
  const navigate = useNavigate();
  const contact = useQuery((signal) => webmailApi.contact(id, signal), [id]);
  const [editing, setEditing] = useState(false);
  const [removing, setRemoving] = useState(false);
  const data = contact.data;

  const back = (
    <Link to={backHref} className="cf-btn cf-btn--ghost cf-btn--sm cf-wm-reader__back">
      <IconChevronLeft size={16} />
      {t('webmail.reader.back')}
    </Link>
  );

  if (!data) {
    return (
      <div className="cf-wm-reader">
        {back}
        {contact.error ? (
          <ErrorState error={contact.error} onRetry={contact.reload} />
        ) : (
          <Skeleton lines={6} />
        )}
      </div>
    );
  }

  const name = contactName(data);
  return (
    <article className="cf-wm-reader" aria-labelledby="wm-contact-name">
      <div className="cf-wm-reader__toolbar">
        {back}
        <span className="cf-wm-reader__spacer" />
        <Button size="sm" icon={<IconEdit size={16} />} onClick={() => setEditing(true)}>
          {t('common.edit')}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          icon={<IconTrash size={16} />}
          onClick={() => setRemoving(true)}
        >
          {t('common.delete')}
        </Button>
      </div>
      <header className="cf-wm-contact-head">
        <span className="cf-wm-avatar cf-wm-avatar--lg" aria-hidden="true">
          {contactInitials(data)}
        </span>
        <div>
          <h2 id="wm-contact-name" className="cf-wm-reader__subject">
            {name}
          </h2>
          {data.title || data.organization ? (
            <p className="cf-text-secondary">
              {[data.title, data.organization].filter(Boolean).join(' - ')}
            </p>
          ) : null}
        </div>
      </header>
      <dl className="cf-dl">
        {data.emails.map((email) => (
          <Row key={`e:${email.value}`} label={t(`webmail.contacts.type.${email.type}`)}>
            <span className="cf-wm-contact-value">
              <span>{email.value}</span>
              <Button
                size="sm"
                variant="ghost"
                icon={<IconMail size={14} />}
                onClick={() => navigate(paths.webmailComposeTo(email.value))}
              >
                {t('webmail.contacts.write')}
              </Button>
            </span>
          </Row>
        ))}
        {data.phones.map((phone) => (
          <Row key={`p:${phone.value}`} label={t(`webmail.contacts.type.${phone.type}`)}>
            <a href={`tel:${phone.value.replace(/[^\d+]/g, '')}`}>{phone.value}</a>
          </Row>
        ))}
        {data.birthday ? (
          <Row label={t('webmail.contacts.birthday')}>{formatBirthday(data.birthday)}</Row>
        ) : null}
        {data.notes ? (
          <Row label={t('webmail.contacts.notes')}>
            <span className="cf-wm-contact-notes">{data.notes}</span>
          </Row>
        ) : null}
      </dl>
      {editing ? (
        <ContactFormDialog
          contact={data}
          onClose={() => setEditing(false)}
          onSaved={(saved) => {
            contact.setData(saved);
            setEditing(false);
            onChanged();
          }}
        />
      ) : null}
      <ConfirmDialog
        open={removing}
        title={t('webmail.contacts.removeTitle')}
        message={t('webmail.contacts.removeConfirm', { name })}
        confirmLabel={t('common.delete')}
        danger
        onCancel={() => setRemoving(false)}
        onConfirm={async () => {
          await webmailApi.deleteContact(data.id);
          toast.success(t('webmail.contacts.removed'));
          setRemoving(false);
          onDeleted();
        }}
      />
    </article>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt>{label}</dt>
      <dd>{children}</dd>
    </>
  );
}
