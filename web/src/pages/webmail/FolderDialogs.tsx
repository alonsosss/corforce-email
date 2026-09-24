import { useState } from 'react';
import { webmailApi, type WebmailFolder } from '@/api/webmail';
import { errorMessage } from '@/api/messages';
import { useResource } from '@/hooks/useResource';
import {
  Button,
  ConfirmDialog,
  FormField,
  Input,
  Modal,
  Select,
  useToast,
} from '@/design/components';
import { IconTrash } from '@/design/icons';
import { t } from '@/i18n';
import { webmailMeta } from '@/webmail/catalogs';
import {
  folderDelimiter,
  folderLabel,
  folderNameProblem,
  folderNameTaken,
  hasChildren,
  joinFolderPath,
  orderFolders,
  parentPath,
} from './folders';

function useMaxNameBytes(): number | null {
  return useResource(webmailMeta).data?.limits.max_folder_name_bytes ?? null;
}

export function CreateFolderDialog({
  folders,
  onClose,
  onCreated,
}: {
  folders: readonly WebmailFolder[];
  onClose: () => void;
  onCreated: (folder: WebmailFolder) => void;
}) {
  const toast = useToast();
  const maxBytes = useMaxNameBytes();
  const delimiter = folderDelimiter(folders);
  const [name, setName] = useState('');
  const [parent, setParent] = useState('');
  const [problem, setProblem] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  // Sin separador el servidor no admite jerarquia: solo carpetas de primer nivel.
  const parents = delimiter ? orderFolders(folders) : [];

  const submit = async () => {
    const path = joinFolderPath(parent, name.trim(), delimiter);
    const invalid =
      folderNameProblem(name, path, delimiter, maxBytes) ??
      (folderNameTaken(folders, path) ? t('webmail.folderAdmin.nameTaken') : null);
    setProblem(invalid);
    if (invalid) return;
    setBusy(true);
    setError(null);
    try {
      const created = await webmailApi.createFolder(path);
      toast.success(t('webmail.folderAdmin.created', { name: folderLabel(created) }));
      onCreated(created);
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      title={t('webmail.folderAdmin.new')}
      onClose={() => (busy ? undefined : onClose())}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button variant="primary" loading={busy} onClick={() => void submit()}>
            {t('common.create')}
          </Button>
        </>
      }
    >
      <form
        className="cf-form"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <FormField label={t('webmail.folderAdmin.name')} htmlFor="wm-folder-name" error={problem}>
          <Input
            id="wm-folder-name"
            value={name}
            invalid={Boolean(problem)}
            onChange={(e) => {
              setName(e.target.value);
              setProblem(null);
            }}
          />
        </FormField>
        {parents.length ? (
          <FormField label={t('webmail.folderAdmin.parent')} htmlFor="wm-folder-parent">
            <Select
              id="wm-folder-parent"
              value={parent}
              placeholder={t('webmail.folderAdmin.topLevel')}
              options={parents.map((item) => ({
                value: item.folder.name,
                label: `${'  '.repeat(item.depth)}${item.label}`,
              }))}
              onChange={(e) => setParent(e.target.value)}
            />
          </FormField>
        ) : null}
        {error ? (
          <div className="cf-form__error" role="alert">
            {errorMessage(error)}
          </div>
        ) : null}
      </form>
    </Modal>
  );
}

export function EditFolderDialog({
  folder,
  folders,
  onClose,
  onRenamed,
  onDeleted,
}: {
  folder: WebmailFolder;
  folders: readonly WebmailFolder[];
  onClose: () => void;
  onRenamed: (folder: WebmailFolder) => void;
  onDeleted: () => void;
}) {
  const toast = useToast();
  const maxBytes = useMaxNameBytes();
  const current = folderLabel(folder);
  const [name, setName] = useState(current);
  const [problem, setProblem] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [confirming, setConfirming] = useState(false);
  const withChildren = hasChildren(folders, folder);

  const rename = async () => {
    const path = joinFolderPath(parentPath(folder), name.trim(), folder.delimiter);
    const invalid =
      folderNameProblem(name, path, folder.delimiter, maxBytes) ??
      (folderNameTaken(folders, path, folder.name) ? t('webmail.folderAdmin.nameTaken') : null);
    setProblem(invalid);
    if (invalid) return;
    if (path === folder.name) {
      onClose();
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const renamed = await webmailApi.renameFolder(folder.name, path);
      toast.success(t('webmail.folderAdmin.renamed', { name: folderLabel(renamed) }));
      onRenamed(renamed);
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };

  return (
    <>
      <Modal
        open={!confirming}
        title={t('webmail.folderAdmin.editTitle', { name: current })}
        onClose={() => (busy ? undefined : onClose())}
        footer={
          <>
            <Button
              variant="danger"
              icon={<IconTrash size={16} />}
              disabled={busy || withChildren}
              onClick={() => setConfirming(true)}
            >
              {t('webmail.folderAdmin.delete')}
            </Button>
            <span className="cf-wm-reader__spacer" />
            <Button onClick={onClose} disabled={busy}>
              {t('common.cancel')}
            </Button>
            <Button variant="primary" loading={busy} onClick={() => void rename()}>
              {t('webmail.folderAdmin.rename')}
            </Button>
          </>
        }
      >
        <form
          className="cf-form"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            void rename();
          }}
        >
          <FormField
            label={t('webmail.folderAdmin.name')}
            htmlFor="wm-folder-rename"
            error={problem}
          >
            <Input
              id="wm-folder-rename"
              value={name}
              invalid={Boolean(problem)}
              onChange={(e) => {
                setName(e.target.value);
                setProblem(null);
              }}
            />
          </FormField>
          {withChildren ? (
            <p className="cf-field__hint">{t('webmail.folderAdmin.hasChildren')}</p>
          ) : null}
          {error ? (
            <div className="cf-form__error" role="alert">
              {errorMessage(error)}
            </div>
          ) : null}
        </form>
      </Modal>
      <ConfirmDialog
        open={confirming}
        title={t('webmail.folderAdmin.delete')}
        message={t('webmail.folderAdmin.deleteConfirm', { name: current })}
        confirmLabel={t('webmail.folderAdmin.delete')}
        danger
        onCancel={() => setConfirming(false)}
        onConfirm={async () => {
          await webmailApi.deleteFolder(folder.name);
          toast.success(t('webmail.folderAdmin.deleted', { name: current }));
          onDeleted();
        }}
      />
    </>
  );
}
