import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { ApiError, type AssignAssetRequest } from '../../api/client';
import { useCreateAssignment, useReturnAssignment, useTransferAssignment } from '../../api/hooks';
import { useUndoableSubmit } from '../../hooks/useUndoableSubmit';
import { ObjectPicker, accessErrorMessage, type PickedObject } from '../pickers/ObjectPicker';
import { UserTeamPicker, type PickedHolder } from '../pickers/UserTeamPicker';
import { Button } from '../ui/Button';
import { Input } from '../ui/Input';
import { Modal } from '../ui/Modal';

/** PROBLEM_HOLDER_MISMATCH is the 409 of a holder that changed meanwhile (WP-275). */
const PROBLEM_HOLDER_MISMATCH = 'https://reticora.io/problems/holder-mismatch';

/**
 * HeldAsset is an asset with its current holder, as a row of the
 * assignment list names it: the target of return and transfer.
 */
export interface HeldAsset {
  assetId: string;
  /** assetLabel is what the dialog shows of the asset (name and inventory number). */
  assetLabel: string;
  /** holder is the id of the current user or team, sent as expected_holder. */
  holder: string;
  holderLabel: string;
}

/** dueAt turns the chosen day into the end of that day in local time. */
export function dueAt(day: string): string | undefined {
  if (!day) return undefined;
  const end = new Date(`${day}T23:59:59`);
  return Number.isNaN(end.getTime()) ? undefined : end.toISOString();
}

/** holderFields are the request members of a new holder. */
function holderFields(holder: PickedHolder) {
  return holder.kind === 'team' ? { assigned_team_id: holder.id } : { assigned_user_id: holder.id };
}

/**
 * useFlowErrorMessage words a failed write: a holder that changed
 * meanwhile, a plan without the feature or limit, a missing right, else
 * the detail of the problem (a configuration item of AST-05, a reserved
 * asset).
 */
function useFlowErrorMessage() {
  const { t } = useTranslation();
  return (error: unknown) =>
    error instanceof ApiError && error.status === 409 && error.type === PROBLEM_HOLDER_MISMATCH
      ? t('assignment.conflict')
      : accessErrorMessage(error, t);
}

function assetLabel(asset: PickedObject) {
  return asset.code ? `${asset.name} (${asset.code})` : asset.name;
}

interface DialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

/**
 * AssignDialog assigns an asset to a person or a team (AST-02, UI-15,
 * WP-318): asset by ObjectPicker, recipient by UserTeamPicker, due date and
 * note. The dialog closes at once; POST /assets/{id}/assign goes out after
 * the undo window (useUndoableSubmit), a failure then shows as error toast.
 * Keep the dialog mounted with the page: leaving the page sends a waiting
 * assignment at once.
 */
export function AssignDialog({ open, onOpenChange }: DialogProps) {
  const { t } = useTranslation();
  const assign = useCreateAssignment();
  const { scheduleSubmit } = useUndoableSubmit();
  const errorMessage = useFlowErrorMessage();
  const [asset, setAsset] = useState<PickedObject[]>([]);
  const [holder, setHolder] = useState<PickedHolder | null>(null);
  const [due, setDue] = useState('');
  const [notes, setNotes] = useState('');

  const reset = () => {
    setAsset([]);
    setHolder(null);
    setDue('');
    setNotes('');
  };

  const chosen = asset[0];
  const submit = () => {
    if (!chosen || !holder) return;
    const label = assetLabel(chosen);
    const data: AssignAssetRequest = {
      ...holderFields(holder),
      due_at: dueAt(due),
      notes: notes || undefined,
    };
    scheduleSubmit({
      id: `assign:${chosen.id}`,
      message: t('assignment.assignPending', { asset: label, holder: holder.name }),
      submitFn: () => assign.mutateAsync({ assetId: chosen.id, data }),
      failedMessage: (error) =>
        t('assignment.assignFailed', { asset: label, message: errorMessage(error) }),
    });
    reset();
    onOpenChange(false);
  };

  return (
    <Modal
      open={open}
      onOpenChange={(next) => {
        if (!next) reset();
        onOpenChange(next);
      }}
      title={t('assignment.assign')}
    >
      <div className="space-y-4">
        <ObjectPicker
          label={t('assignment.asset')}
          kinds={['asset']}
          value={asset}
          onChange={setAsset}
          required
        />
        <UserTeamPicker
          label={t('assignment.recipient')}
          value={holder}
          onChange={setHolder}
          required
        />
        <Input
          type="date"
          label={t('assignment.dueAt')}
          value={due}
          onChange={(event) => setDue(event.target.value)}
        />
        <Input
          label={t('common.notes')}
          value={notes}
          onChange={(event) => setNotes(event.target.value)}
        />
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          <Button onClick={submit} disabled={!chosen || !holder}>
            {t('assignment.assign')}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

interface HeldDialogProps extends DialogProps {
  /** target is the asset with its holder; the dialog is closed without one. */
  target: HeldAsset | null;
}

/** DialogError shows the problem of a failed write inside the dialog. */
function DialogError({ message }: { message?: string }) {
  if (!message) return null;
  return (
    <p role="alert" className="text-sm text-red-600 dark:text-red-400">
      {message}
    </p>
  );
}

/**
 * useConflictRefresh loads the assignment list again after a 409: the rows
 * then show the current holder.
 */
function useConflictRefresh() {
  const queryClient = useQueryClient();
  return (error: unknown) => {
    if (error instanceof ApiError && error.status === 409) {
      void queryClient.invalidateQueries({ queryKey: ['assignments'] });
    }
  };
}

/**
 * ReturnDialog takes an asset back from its holder: POST
 * /assets/{id}/return with the holder as expected_holder (WP-275). A
 * failure (409, 403) shows in the dialog.
 */
export function ReturnDialog({ open, onOpenChange, target }: HeldDialogProps) {
  const { t } = useTranslation();
  const returnAsset = useReturnAssignment();
  const errorMessage = useFlowErrorMessage();
  const refresh = useConflictRefresh();
  const [condition, setCondition] = useState('');
  const [notes, setNotes] = useState('');
  const [error, setError] = useState<string>();

  const close = (next: boolean) => {
    if (!next) {
      setCondition('');
      setNotes('');
      setError(undefined);
    }
    onOpenChange(next);
  };

  const submit = () => {
    if (!target) return;
    setError(undefined);
    returnAsset.mutate(
      {
        assetId: target.assetId,
        data: {
          expected_holder: target.holder,
          return_condition: condition || undefined,
          notes: notes || undefined,
        },
      },
      {
        onSuccess: () => close(false),
        onError: (failure) => {
          setError(errorMessage(failure));
          refresh(failure);
        },
      },
    );
  };

  return (
    <Modal
      open={open && !!target}
      onOpenChange={close}
      title={t('assignment.returnTitle')}
      description={
        target
          ? t('assignment.returnDescription', {
              asset: target.assetLabel,
              holder: target.holderLabel,
            })
          : undefined
      }
    >
      <div className="space-y-4">
        <Input
          label={t('assignment.returnCondition')}
          value={condition}
          onChange={(event) => setCondition(event.target.value)}
        />
        <Input
          label={t('common.notes')}
          value={notes}
          onChange={(event) => setNotes(event.target.value)}
        />
        <DialogError message={error} />
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={() => close(false)}>
            {t('common.cancel')}
          </Button>
          <Button onClick={submit} disabled={returnAsset.isPending}>
            {t('assignment.return')}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

/**
 * TransferDialog hands an asset from its holder to a new person or team:
 * POST /assets/{id}/transfer with expected_holder (WP-275). A failure
 * (409, 403) shows in the dialog.
 */
export function TransferDialog({ open, onOpenChange, target }: HeldDialogProps) {
  const { t } = useTranslation();
  const transfer = useTransferAssignment();
  const errorMessage = useFlowErrorMessage();
  const refresh = useConflictRefresh();
  const [holder, setHolder] = useState<PickedHolder | null>(null);
  const [due, setDue] = useState('');
  const [notes, setNotes] = useState('');
  const [error, setError] = useState<string>();

  const close = (next: boolean) => {
    if (!next) {
      setHolder(null);
      setDue('');
      setNotes('');
      setError(undefined);
    }
    onOpenChange(next);
  };

  const submit = () => {
    if (!target || !holder) return;
    setError(undefined);
    transfer.mutate(
      {
        assetId: target.assetId,
        data: {
          expected_holder: target.holder,
          ...holderFields(holder),
          due_at: dueAt(due),
          notes: notes || undefined,
        },
      },
      {
        onSuccess: () => close(false),
        onError: (failure) => {
          setError(errorMessage(failure));
          refresh(failure);
        },
      },
    );
  };

  return (
    <Modal
      open={open && !!target}
      onOpenChange={close}
      title={t('assignment.transferTitle')}
      description={
        target
          ? t('assignment.transferDescription', {
              asset: target.assetLabel,
              holder: target.holderLabel,
            })
          : undefined
      }
    >
      <div className="space-y-4">
        <UserTeamPicker
          label={t('assignment.newRecipient')}
          value={holder}
          onChange={setHolder}
          required
        />
        <Input
          type="date"
          label={t('assignment.dueAt')}
          value={due}
          onChange={(event) => setDue(event.target.value)}
        />
        <Input
          label={t('common.notes')}
          value={notes}
          onChange={(event) => setNotes(event.target.value)}
        />
        <DialogError message={error} />
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={() => close(false)}>
            {t('common.cancel')}
          </Button>
          <Button onClick={submit} disabled={!holder || transfer.isPending}>
            {t('assignment.transfer')}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
