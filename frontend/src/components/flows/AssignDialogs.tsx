import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { ApiError, type AssignAssetRequest } from '../../api/client';
import type { components } from '../../api/generated/schema';
import { useCreateAssignment, useReturnAssignment, useTransferAssignment } from '../../api/hooks';
import { useEntitlements } from '../../hooks/useEntitlements';
import { useUndoableSubmit } from '../../hooks/useUndoableSubmit';
import { ScanButton, WithScan } from './ScanDialog';
import { SignaturePad, signatureTooLarge } from './SignaturePad';
import { ObjectPicker, accessErrorMessage, type PickedObject } from '../pickers/ObjectPicker';
import { UserTeamPicker, type PickedHolder } from '../pickers/UserTeamPicker';
import { Button } from '../ui/Button';
import { ErrorState } from '../ui/ErrorState';
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

type SignatureInput = components['schemas']['SignatureInput'];

/**
 * useSignature holds the signature of a handover or return (AST-02, E-51,
 * WP-807), offered only with the feature signature (from standard on,
 * E-44). Without the feature there is no field and no signature is sent.
 */
function useSignature() {
  const { isEnabled } = useEntitlements();
  const offered = isEnabled('signature');
  const [png, setPng] = useState<string | null>(null);
  const [signer, setSigner] = useState('');
  const input: SignatureInput | undefined =
    offered && png
      ? {
          image_png_base64: png,
          signer: signer.trim() ? { name: signer.trim() } : undefined,
        }
      : undefined;
  const reset = () => {
    setPng(null);
    setSigner('');
  };
  return { offered, png, setPng, signer, setSigner, input, reset };
}

/**
 * SignatureField is the pad of a dialog and, once signed, the optional name
 * of the person who signs; without it the holder of the movement signs (a
 * team holder needs a name, WP-276).
 */
function SignatureField({
  signature,
  label,
}: {
  signature: ReturnType<typeof useSignature>;
  label: string;
}) {
  const { t } = useTranslation();
  if (!signature.offered) return null;
  return (
    <div className="space-y-2">
      <SignaturePad label={label} value={signature.png} onChange={signature.setPng} />
      {signature.png ? (
        <Input
          label={t('signature.signer')}
          value={signature.signer}
          onChange={(event) => signature.setSigner(event.target.value)}
        />
      ) : null}
    </div>
  );
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
 * assignment at once. With the feature barcode the asset can be scanned,
 * with the feature signature the handover is signed (WP-807): the PNG goes
 * in the field signature, "Undo" drops it with the request.
 */
export function AssignDialog({
  open,
  onOpenChange,
  initialAsset,
}: DialogProps & {
  /** initialAsset is the asset of a scan (WP-807); the dialog opens with it. */
  initialAsset?: PickedObject;
}) {
  const { t } = useTranslation();
  const assign = useCreateAssignment();
  const { scheduleSubmit } = useUndoableSubmit();
  const errorMessage = useFlowErrorMessage();
  const signature = useSignature();
  const [asset, setAsset] = useState<PickedObject[]>([]);
  const [holder, setHolder] = useState<PickedHolder | null>(null);
  const [due, setDue] = useState('');
  const [notes, setNotes] = useState('');
  const [error, setError] = useState<string>();

  useEffect(() => {
    if (open && initialAsset) setAsset([initialAsset]);
  }, [open, initialAsset]);

  const reset = () => {
    setAsset([]);
    setHolder(null);
    setDue('');
    setNotes('');
    setError(undefined);
    signature.reset();
  };

  const chosen = asset[0];
  const submit = () => {
    if (!chosen || !holder) return;
    const label = assetLabel(chosen);
    const data: AssignAssetRequest = {
      ...holderFields(holder),
      due_at: dueAt(due),
      notes: notes || undefined,
      signature: signature.input,
    };
    // Above the limit of the server or of the proxy nothing is sent.
    if (signature.input && signatureTooLarge(signature.input.image_png_base64, data)) {
      setError(t('signature.tooLarge'));
      return;
    }
    // "Undo" in the window drops the handover with its image, no server call.
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
        <WithScan
          scan={
            <ScanButton
              field={t('assignment.asset')}
              accept={['asset']}
              onObject={(object) => setAsset([object])}
            />
          }
        >
          <ObjectPicker
            label={t('assignment.asset')}
            kinds={['asset']}
            value={asset}
            onChange={setAsset}
            required
          />
        </WithScan>
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
        <SignatureField signature={signature} label={t('signature.handover')} />
        <DialogError message={error} />
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
  return <ErrorState title={message} />;
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
  const signature = useSignature();
  const [condition, setCondition] = useState('');
  const [notes, setNotes] = useState('');
  const [error, setError] = useState<string>();

  const close = (next: boolean) => {
    if (!next) {
      setCondition('');
      setNotes('');
      setError(undefined);
      signature.reset();
    }
    onOpenChange(next);
  };

  const submit = () => {
    if (!target) return;
    setError(undefined);
    const data = {
      expected_holder: target.holder,
      return_condition: condition || undefined,
      notes: notes || undefined,
      signature: signature.input,
    };
    if (signature.input && signatureTooLarge(signature.input.image_png_base64, data)) {
      setError(t('signature.tooLarge'));
      return;
    }
    returnAsset.mutate(
      { assetId: target.assetId, data },
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
        <SignatureField signature={signature} label={t('signature.return')} />
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
  const signature = useSignature();
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
      signature.reset();
    }
    onOpenChange(next);
  };

  const submit = () => {
    if (!target || !holder) return;
    setError(undefined);
    // The new holder signs the handover (signature, WP-276).
    const data = {
      expected_holder: target.holder,
      ...holderFields(holder),
      due_at: dueAt(due),
      notes: notes || undefined,
      signature: signature.input,
    };
    if (signature.input && signatureTooLarge(signature.input.image_png_base64, data)) {
      setError(t('signature.tooLarge'));
      return;
    }
    transfer.mutate(
      { assetId: target.assetId, data },
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
        <SignatureField signature={signature} label={t('signature.handover')} />
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
