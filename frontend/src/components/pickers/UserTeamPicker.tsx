import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { X } from 'lucide-react';
import { ApiError } from '../../api/client';
import { useTeamList, useUserList } from '../../api/hooks';
import {
  PICKER_LIMIT,
  PickerCombobox,
  accessErrorMessage,
  useDebouncedValue,
} from './ObjectPicker';
import { IconButton } from '../ui/Button';

export type HolderKind = 'user' | 'team';

/**
 * PickedHolder is a chosen person or team: kind and id go to the server,
 * name is only shown. The server maps the user id onto its surrogate and
 * resolves names (Convention 16, WP-241, WP-275); the client does not.
 */
export interface PickedHolder {
  kind: HolderKind;
  id: string;
  name: string;
}

export interface UserTeamPickerProps {
  label: string;
  value: PickedHolder | null;
  onChange: (value: PickedHolder | null) => void;
  /** kinds offered; both by default. */
  kinds?: readonly HolderKind[];
  required?: boolean;
}

const isForbidden = (error: unknown) => error instanceof ApiError && error.status === 403;

/**
 * UserTeamPicker finds a person or a team by name (UI-15, WP-318): GET
 * /users and /teams with search after a typing pause, showing display_name
 * or the team name. Without user:read (403) it says so instead of showing
 * an empty list.
 */
export function UserTeamPicker({
  label,
  value,
  onChange,
  kinds = ['user', 'team'],
  required,
}: UserTeamPickerProps) {
  const { t } = useTranslation();
  const [query, setQuery] = useState('');
  const debounced = useDebouncedValue(query.trim());
  // One request per typing pause: no search while the input still moves.
  const pending = query.trim() !== debounced;
  const params = { search: debounced, limit: PICKER_LIMIT };
  const ready = debounced !== '' && !pending;
  const users = useUserList(params, { enabled: kinds.includes('user') && ready });
  const teams = useTeamList(params, { enabled: kinds.includes('team') && ready });

  const hits: PickedHolder[] = [
    ...(users.data?.data ?? []).map((user) => ({
      kind: 'user' as const,
      id: user.id,
      name: user.display_name,
    })),
    ...(teams.data?.data ?? []).map((team) => ({
      kind: 'team' as const,
      id: team.id,
      name: team.name,
    })),
  ].filter((hit) => !(value && value.kind === hit.kind && value.id === hit.id));

  const failure = [users.error, teams.error].find(Boolean);
  let notice: string | undefined;
  if (isForbidden(users.error) || isForbidden(teams.error)) {
    notice = t('pickers.errors.forbiddenPeople');
  } else if (failure) {
    notice = accessErrorMessage(failure, t);
  }

  return (
    <div className="space-y-2">
      <PickerCombobox
        label={label}
        query={query}
        onQueryChange={setQuery}
        required={required && !value}
        options={hits.map((hit) => ({
          key: `${hit.kind}:${hit.id}`,
          kindLabel: t(`pickers.holderKinds.${hit.kind}`),
          name: hit.name,
        }))}
        onSelect={(index) => {
          const hit = hits[index];
          if (!hit) return;
          onChange(hit);
          setQuery('');
        }}
        searching={pending || users.isFetching || teams.isFetching}
        searched={!pending && (users.isFetched || teams.isFetched)}
        notice={notice}
      />
      {value ? (
        <p className="flex w-fit items-center gap-2 rounded-lg border border-line bg-panel py-0.5 pl-3 pr-1 text-sm text-fg">
          <span className="text-xs text-fg-muted">{t(`pickers.holderKinds.${value.kind}`)}</span>
          <span className="font-medium">{value.name}</span>
          <IconButton
            size="sm"
            icon={<X />}
            label={t('pickers.remove', { name: value.name })}
            onClick={() => onChange(null)}
            className="h-6 w-6"
          />
        </p>
      ) : null}
    </div>
  );
}
