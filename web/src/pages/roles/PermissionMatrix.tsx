import { useMemo } from 'react';
import type { Permission } from '@/api/access';
import { Checkbox } from '@/design/components';
import { t } from '@/i18n';

export interface PermissionMatrixProps {
  catalog: Permission[];
  selected: Set<string>;
  disabled?: boolean;
  onChange: (next: Set<string>) => void;
}

interface ResourceRow {
  resource: string;
  permissions: Permission[];
}

interface ModuleGroup {
  module: string;
  resources: ResourceRow[];
  ids: string[];
}

function groupCatalog(catalog: Permission[]): ModuleGroup[] {
  const byModule = new Map<string, Map<string, Permission[]>>();
  for (const p of catalog) {
    const resources = byModule.get(p.module) ?? new Map<string, Permission[]>();
    const list = resources.get(p.resource) ?? [];
    list.push(p);
    resources.set(p.resource, list);
    byModule.set(p.module, resources);
  }
  return [...byModule.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([module, resources]) => {
      const rows = [...resources.entries()]
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([resource, permissions]) => ({
          resource,
          permissions: [...permissions].sort((a, b) => a.action.localeCompare(b.action)),
        }));
      return { module, resources: rows, ids: rows.flatMap((r) => r.permissions.map((p) => p.id)) };
    });
}

/** Matriz modulo -> recurso -> acciones sobre el catalogo real de GET /permissions. */
export function PermissionMatrix({
  catalog,
  selected,
  disabled = false,
  onChange,
}: PermissionMatrixProps) {
  const groups = useMemo(() => groupCatalog(catalog), [catalog]);

  const toggle = (id: string, on: boolean) => {
    const next = new Set(selected);
    if (on) next.add(id);
    else next.delete(id);
    onChange(next);
  };

  const toggleModule = (group: ModuleGroup, on: boolean) => {
    const next = new Set(selected);
    for (const id of group.ids) {
      if (on) next.add(id);
      else next.delete(id);
    }
    onChange(next);
  };

  return (
    <div className="cf-table-wrap">
      <table className="cf-matrix">
        <thead>
          <tr>
            <th scope="col">{t('roles.permissions.module')}</th>
            <th scope="col">{t('roles.permissions.resource')}</th>
            <th scope="col">{t('common.actions')}</th>
          </tr>
        </thead>
        <tbody>
          {groups.map((group) => {
            const allOn = group.ids.every((id) => selected.has(id));
            return [
              <tr key={`${group.module}-head`} className="cf-matrix__module">
                <td colSpan={3}>
                  <Checkbox
                    label={
                      <>
                        <strong>{group.module}</strong>
                        <span className="cf-text-muted cf-text-sm" style={{ marginLeft: 8 }}>
                          {t('roles.permissions.selectModule')}
                        </span>
                      </>
                    }
                    checked={allOn}
                    disabled={disabled}
                    onChange={(e) => toggleModule(group, e.target.checked)}
                  />
                </td>
              </tr>,
              ...group.resources.map((row) => (
                <tr key={`${group.module}-${row.resource}`}>
                  <td className="cf-text-muted">{group.module}</td>
                  <td>
                    <span className="cf-mono">{row.resource}</span>
                  </td>
                  <td>
                    <div className="cf-matrix__actions">
                      {row.permissions.map((p) => (
                        <Checkbox
                          key={p.id}
                          label={<span title={p.description}>{p.action}</span>}
                          checked={selected.has(p.id)}
                          disabled={disabled}
                          onChange={(e) => toggle(p.id, e.target.checked)}
                        />
                      ))}
                    </div>
                  </td>
                </tr>
              )),
            ];
          })}
        </tbody>
      </table>
    </div>
  );
}
