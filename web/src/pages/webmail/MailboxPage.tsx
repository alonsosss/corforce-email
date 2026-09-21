import { useCallback } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { FLAGS, FOLDER_ROLES, hasFlag, webmailApi } from '@/api/webmail';
import { useQuery } from '@/hooks/useQuery';
import { EmptyState, ErrorState, LoadingBlock } from '@/design/components';
import { IconMail } from '@/design/icons';
import { t } from '@/i18n';
import { paths } from '@/paths';
import { useWebmailStore } from '@/webmail/store';
import { defaultFolder, folderLabel } from './folders';
import { parsePositiveInt } from './format';
import { MessageList } from './MessageList';
import { MessageView } from './MessageView';
import { useWebmailOutlet } from './webmailContext';

/** Carpeta, mensaje, pagina y busqueda salen de la query: una recarga vuelve al mismo sitio. */
export default function MailboxPage() {
  const { folders } = useWebmailOutlet();
  const [params] = useSearchParams();
  const list = folders.data;
  const folderName = params.get('folder') ?? (list ? defaultFolder(list) : null);

  if (!folderName) {
    if (folders.error) {
      return (
        <div className="cf-wm-pad">
          <ErrorState error={folders.error} onRetry={folders.reload} />
        </div>
      );
    }
    if (!list) return <LoadingBlock />;
    return (
      <div className="cf-wm-pad">
        <EmptyState title={t('webmail.folders.none')} />
      </div>
    );
  }
  return <MailboxView key={folderName} folderName={folderName} />;
}

function MailboxView({ folderName }: { folderName: string }) {
  const { folders, adjustUnread, reloadFolders, inboxTick } = useWebmailOutlet();
  const refreshSession = useWebmailStore((s) => s.refresh);
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const uid = parsePositiveInt(params.get('uid'));
  const page = parsePositiveInt(params.get('page')) ?? 1;
  const search = params.get('q') ?? '';
  const folder = folders.data?.find((f) => f.name === folderName) ?? null;

  // Un aviso de la bandeja vuelve a leer la lista solo si es la bandeja de entrada.
  const liveTick = folder?.role === FOLDER_ROLES.inbox ? inboxTick : 0;
  const list = useQuery(
    (signal) => webmailApi.messages(folderName, { page, search: search || undefined }, signal),
    [folderName, page, search, liveTick],
  );
  const { setData: setList, reload: reloadList } = list;
  const rows = list.data?.items;

  const viewHref = useCallback(
    (next: { uid?: number; page?: number; q?: string }) =>
      paths.webmailView({
        folder: folderName,
        uid: next.uid,
        page: next.page && next.page > 1 ? next.page : undefined,
        q: next.q || undefined,
      }),
    [folderName],
  );
  const listHref = viewHref({ page, q: search });

  const patchRow = useCallback(
    (rowUid: number, flags: string[]) =>
      setList((current) =>
        current
          ? {
              ...current,
              items: current.items.map((row) => (row.uid === rowUid ? { ...row, flags } : row)),
            }
          : current,
      ),
    [setList],
  );

  // Abrir un mensaje lo marca como leido en el servidor: se refleja en la fila y en la
  // carpeta sin volver a pedir nada.
  const onSeen = useCallback(
    (seenUid: number) => {
      const row = rows?.find((r) => r.uid === seenUid);
      if (!row) {
        reloadFolders();
        return;
      }
      if (hasFlag(row, FLAGS.seen)) return;
      adjustUnread(folderName, -1);
      patchRow(seenUid, [...row.flags, FLAGS.seen]);
    },
    [rows, adjustUnread, folderName, patchRow, reloadFolders],
  );

  const onFlagsChanged = useCallback(
    (changedUid: number, flags: string[]) => {
      const row = rows?.find((r) => r.uid === changedUid);
      if (!row) {
        reloadFolders();
        return;
      }
      const wasSeen = hasFlag(row, FLAGS.seen);
      const isSeen = flags.includes(FLAGS.seen);
      if (wasSeen !== isSeen) adjustUnread(folderName, isSeen ? -1 : 1);
      patchRow(changedUid, flags);
    },
    [rows, adjustUnread, folderName, patchRow, reloadFolders],
  );

  const onGone = useCallback(() => {
    reloadList();
    reloadFolders();
    void refreshSession();
    navigate(listHref, { replace: true });
  }, [reloadList, reloadFolders, refreshSession, navigate, listHref]);

  return (
    <div
      className={['cf-wm-mailbox', uid ? 'cf-wm-mailbox--reading' : ''].filter(Boolean).join(' ')}
    >
      <section className="cf-wm-mailbox__list" aria-labelledby="wm-folder-title">
        <MessageList
          titleId="wm-folder-title"
          title={folder ? folderLabel(folder) : folderName}
          role={folder?.role ?? ''}
          list={list}
          selectedUid={uid}
          search={search}
          hrefFor={(rowUid) => viewHref({ uid: rowUid, page, q: search })}
          onSearch={(q) => navigate(viewHref({ q }))}
          onPage={(next) => navigate(viewHref({ page: next, q: search }))}
          onRefresh={() => {
            reloadList();
            reloadFolders();
          }}
        />
      </section>
      <section className="cf-wm-mailbox__reader" aria-label={t('webmail.reader.label')}>
        {uid ? (
          <MessageView
            key={uid}
            folderName={folderName}
            folder={folder}
            folders={folders.data ?? []}
            uid={uid}
            backHref={listHref}
            onSeen={onSeen}
            onFlagsChanged={onFlagsChanged}
            onGone={onGone}
          />
        ) : (
          <EmptyState
            icon={<IconMail size={32} />}
            title={t('webmail.reader.none')}
            description={t('webmail.reader.noneHint')}
          />
        )}
      </section>
    </div>
  );
}
