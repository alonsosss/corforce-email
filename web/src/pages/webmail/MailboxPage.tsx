import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import {
  FLAGS,
  FOLDER_ROLES,
  hasFlag,
  webmailApi,
  webmailRemindersApi,
  type BatchAction,
  type FlagChange,
  type WebmailFolder,
} from '@/api/webmail';
import { useQuery } from '@/hooks/useQuery';
import { useResource } from '@/hooks/useResource';
import {
  Button,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  LoadingBlock,
  useToast,
} from '@/design/components';
import { IconMail, IconTrash } from '@/design/icons';
import { t, type MessageKey } from '@/i18n';
import { errorMessage } from '@/api/messages';
import { paths } from '@/paths';
import { webmailMeta } from '@/webmail/catalogs';
import { useShortcuts } from '@/webmail/shortcuts';
import { useWebmailStore } from '@/webmail/store';
import { BatchBar } from './BatchBar';
import { applyFlagChange } from './flags';
import { defaultFolder, folderLabel, folderWithRole, isEmptiable } from './folders';
import { parsePositiveInt } from './format';
import { MessageList } from './MessageList';
import { MessageView } from './MessageView';
import { formatScheduled } from './schedule';
import { canSnooze } from './snooze';
import { criteriaToFilters, criteriaToView, readCriteria, type SearchCriteria } from './search';
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

function chunk<T>(values: readonly T[], size: number): T[][] {
  const out: T[][] = [];
  for (let i = 0; i < values.length; i += size) out.push(values.slice(i, i + size));
  return out;
}

function MailboxView({ folderName }: { folderName: string }) {
  const { folders, adjustUnread, reloadFolders, inboxTick } = useWebmailOutlet();
  const refreshSession = useWebmailStore((s) => s.refresh);
  const toast = useToast();
  const maxBatch = useResource(webmailMeta).data?.limits.max_batch_uids ?? null;
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const uid = parsePositiveInt(params.get('uid'));
  const page = parsePositiveInt(params.get('page')) ?? 1;
  const criteria = useMemo(() => readCriteria(params), [params]);
  const criteriaKey = JSON.stringify(criteria);
  const folder = folders.data?.find((f) => f.name === folderName) ?? null;
  const role = folder?.role ?? '';
  const [checked, setChecked] = useState<Set<number>>(() => new Set());
  const [purging, setPurging] = useState<number[] | null>(null);
  const [emptying, setEmptying] = useState(false);
  const [searchFocus, setSearchFocus] = useState(0);

  // Un aviso de la bandeja vuelve a leer la lista solo si es la bandeja de entrada.
  const liveTick = role === FOLDER_ROLES.inbox ? inboxTick : 0;
  const list = useQuery(
    (signal) =>
      webmailApi.messages(
        folderName,
        { page, search: criteria.q || undefined, ...criteriaToFilters(criteria) },
        signal,
      ),
    // criteriaKey resume criteria: cambia solo cuando cambia la busqueda.
    [folderName, page, criteriaKey, liveTick],
  );
  const { setData: setList, reload: reloadList } = list;
  const rows = list.data?.items;

  useEffect(() => setChecked(new Set()), [page, criteriaKey]);

  const viewHref = useCallback(
    (next: { uid?: number; page?: number; criteria?: SearchCriteria }) =>
      paths.webmailView({
        folder: folderName,
        uid: next.uid,
        page: next.page && next.page > 1 ? next.page : undefined,
        ...criteriaToView(next.criteria ?? criteria),
      }),
    [folderName, criteria],
  );
  const listHref = viewHref({ page });

  const patchRows = useCallback(
    (uids: readonly number[], change: FlagChange) =>
      setList((current) =>
        current
          ? {
              ...current,
              items: current.items.map((row) =>
                uids.includes(row.uid)
                  ? { ...row, flags: applyFlagChange(row.flags, change) }
                  : row,
              ),
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
      patchRows([seenUid], { add: [FLAGS.seen] });
    },
    [rows, adjustUnread, folderName, patchRows, reloadFolders],
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
      setList((current) =>
        current
          ? {
              ...current,
              items: current.items.map((r) => (r.uid === changedUid ? { ...r, flags } : r)),
            }
          : current,
      );
    },
    [rows, adjustUnread, folderName, setList, reloadFolders],
  );

  const onGone = useCallback(() => {
    reloadList();
    reloadFolders();
    void refreshSession();
    navigate(listHref, { replace: true });
  }, [reloadList, reloadFolders, refreshSession, navigate, listHref]);

  /** Aplica una accion a varios mensajes en tandas del tope que sirve el servicio. */
  const runBatch = useCallback(
    async (uids: readonly number[], action: BatchAction) => {
      let affected = 0;
      let permanent = false;
      for (const part of chunk(uids, maxBatch ?? uids.length)) {
        const result = await webmailApi.batch(folderName, part, action);
        affected += result.affected;
        permanent ||= result.permanent;
      }
      return { affected, permanent };
    },
    [folderName, maxBatch],
  );

  const afterRemoval = useCallback(
    (uids: readonly number[]) => {
      setChecked(new Set());
      reloadList();
      reloadFolders();
      void refreshSession();
      if (uid && uids.includes(uid)) navigate(listHref, { replace: true });
    },
    [reloadList, reloadFolders, refreshSession, uid, navigate, listHref],
  );

  const batchFlags = async (uids: number[], change: FlagChange) => {
    await runBatch(uids, { action: 'flags', add: change.add, remove: change.remove });
    const seenBefore = (rows ?? []).filter((r) => uids.includes(r.uid) && hasFlag(r, FLAGS.seen));
    if (change.add?.includes(FLAGS.seen))
      adjustUnread(folderName, -(uids.length - seenBefore.length));
    if (change.remove?.includes(FLAGS.seen)) adjustUnread(folderName, seenBefore.length);
    patchRows(uids, change);
  };

  const batchMove = async (uids: number[], to: WebmailFolder, done: MessageKey) => {
    const { affected } = await runBatch(uids, { action: 'move', to: to.name });
    toast.success(t(done, { n: affected, folder: folderLabel(to) }));
    afterRemoval(uids);
  };

  // El servicio acota cada peticion a max_batch_uids, como el resto de acciones en lote.
  const batchSnooze = async (uids: number[], until: string) => {
    let snoozed = 0;
    let failed = 0;
    for (const part of chunk(uids, maxBatch ?? uids.length)) {
      const result = await webmailRemindersApi.snooze(folderName, part, until);
      snoozed += result.snoozed.length;
      failed += result.failed.length;
    }
    toast.success(t('webmail.snooze.done', { n: snoozed, when: formatScheduled(until) }));
    if (failed) toast.error(t('webmail.snooze.partial', { n: failed }));
    afterRemoval(uids);
  };

  const batchDelete = async (uids: number[]) => {
    const { affected, permanent } = await runBatch(uids, { action: 'delete' });
    toast.success(
      t(permanent ? 'webmail.batch.deleted' : 'webmail.batch.trashed', { n: affected }),
    );
    afterRemoval(uids);
  };

  const isTrash = role === FOLDER_ROLES.trash;
  const archive = folders.data ? folderWithRole(folders.data, FOLDER_ROLES.archive) : undefined;

  // Atajos sobre el mensaje abierto o, sin mensaje abierto, sobre los marcados.
  const targets = (): number[] => (uid ? [uid] : [...checked]);
  const step = (delta: 1 | -1) => {
    if (!rows?.length) return;
    const at = uid ? rows.findIndex((r) => r.uid === uid) : -1;
    const next = rows[Math.min(rows.length - 1, Math.max(0, at === -1 ? 0 : at + delta))];
    if (next && next.uid !== uid) navigate(viewHref({ uid: next.uid, page }));
  };
  const reply = (mode: string) => {
    if (uid && role !== FOLDER_ROLES.drafts) {
      navigate(paths.webmailComposeFrom(mode, folderName, uid));
    }
  };
  useShortcuts({
    j: () => step(1),
    k: () => step(-1),
    r: () => reply('reply'),
    a: () => reply('replyAll'),
    f: () => reply('forward'),
    '/': () => setSearchFocus((n) => n + 1),
    e: () => {
      const uids = targets();
      if (!archive || role === FOLDER_ROLES.archive || !uids.length) return;
      void batchMove(uids, archive, 'webmail.batch.moved').catch((err: unknown) =>
        toast.error(errorMessage(err)),
      );
    },
    '#': () => {
      const uids = targets();
      if (!uids.length) return;
      if (isTrash) setPurging(uids);
      else void batchDelete(uids).catch((err: unknown) => toast.error(errorMessage(err)));
    },
  });

  const emptyAction = isEmptiable(role) ? (
    <Button
      size="sm"
      variant="ghost"
      icon={<IconTrash size={16} />}
      disabled={!rows?.length}
      onClick={() => setEmptying(true)}
    >
      {t('webmail.empty.action')}
    </Button>
  ) : null;

  return (
    <div
      className={['cf-wm-mailbox', uid ? 'cf-wm-mailbox--reading' : ''].filter(Boolean).join(' ')}
    >
      <section className="cf-wm-mailbox__list" aria-labelledby="wm-folder-title">
        <MessageList
          titleId="wm-folder-title"
          title={folder ? folderLabel(folder) : folderName}
          role={role}
          list={list}
          selectedUid={uid}
          criteria={criteria}
          hrefFor={(rowUid) => viewHref({ uid: rowUid, page })}
          onSearch={(next) => navigate(viewHref({ criteria: next }))}
          onPage={(next) => navigate(viewHref({ page: next }))}
          onRefresh={() => {
            reloadList();
            reloadFolders();
          }}
          checked={checked}
          onCheck={(rowUid, value) =>
            setChecked((current) => {
              const next = new Set(current);
              if (value) next.add(rowUid);
              else next.delete(rowUid);
              return next;
            })
          }
          onCheckAll={(value) => setChecked(new Set(value ? (rows ?? []).map((r) => r.uid) : []))}
          folderAction={emptyAction}
          searchFocusTick={searchFocus}
          selectionBar={
            <BatchBar
              role={role}
              folderName={folderName}
              folders={folders.data ?? []}
              uids={[...checked]}
              onFlags={batchFlags}
              onMove={batchMove}
              onSnooze={canSnooze(role) ? batchSnooze : undefined}
              onDelete={async (uids) => {
                if (isTrash) setPurging(uids);
                else await batchDelete(uids);
              }}
            />
          }
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
      <ConfirmDialog
        open={purging !== null}
        title={t('webmail.reader.deleteForever')}
        message={t('webmail.batch.deleteForeverConfirm', { n: purging?.length ?? 0 })}
        confirmLabel={t('webmail.reader.deleteForever')}
        danger
        onCancel={() => setPurging(null)}
        onConfirm={async () => {
          await batchDelete(purging ?? []);
          setPurging(null);
        }}
      />
      <ConfirmDialog
        open={emptying}
        title={t('webmail.empty.title', { folder: folder ? folderLabel(folder) : folderName })}
        message={t('webmail.empty.confirm')}
        confirmLabel={t('webmail.empty.action')}
        danger
        onCancel={() => setEmptying(false)}
        onConfirm={async () => {
          const { removed } = await webmailApi.emptyFolder(folderName);
          toast.success(t('webmail.empty.done', { n: removed }));
          setEmptying(false);
          afterRemoval(rows?.map((r) => r.uid) ?? []);
        }}
      />
    </div>
  );
}
