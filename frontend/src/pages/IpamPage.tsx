/**
 * IpamPage is the page /ipam (IPM-19, UI-14, WP-915): the IPAM sections of
 * the organization (GET /api/v1/ipam-sections), the subnet tree of the
 * chosen section with a usage bar per subnet and the filters of WP-909
 * (VRF, client, site, text and contains). contains opens the chain of the
 * subnets that hold the address and marks the narrowest one. The section
 * dialog creates, edits and deletes sections and keeps their rights per
 * team; without ipam:write every write action is hidden. A new subnet is
 * created in the SubnetFormModal and opens its detail page (WP-916).
 * Beside the subnet tree the tabs VLANs (with domains), VRFs (RD/RT) and
 * nameserver sets (WP-918); the tab stays in the query parameter tab.
 * The action Import/Export (WP-922) opens IpamImportDialog with the current
 * filters of the page.
 */
import { ArrowDownUp, Network, Plus, Settings2 } from 'lucide-react';
import { useMemo, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useSearchParams } from 'react-router';
import type { IPAMSection } from '../api/cmdb';
import { useClients } from '../api/cmdbHooks';
import { useHasPermission, useSites } from '../api/hooks';
import { useIPAMSections, useOrgWideWriter, useSubnetChain, useVrfs } from '../api/ipamHooks';
import { IpamImportDialog, useIpamIORights } from '../components/ipam/IpamImportDialog';
import { IpamTabList, IPAM_TABS, type IpamTab } from '../components/ipam/IpamTabList';
import { NameserverTab } from '../components/ipam/NameserverTab';
import { VlanTab } from '../components/ipam/VlanTab';
import { VrfTab } from '../components/ipam/VrfTab';
import { SectionDialog } from '../components/ipam/SectionDialog';
import { SubnetFormModal } from '../components/ipam/SubnetFormModal';
import { SubnetTree } from '../components/ipam/SubnetTree';
import { Badge } from '../components/ui/Badge';
import { Button } from '../components/ui/Button';
import { Card } from '../components/ui/Card';
import { ErrorState } from '../components/ui/ErrorState';
import { Input } from '../components/ui/Input';
import { PageHeader } from '../components/ui/PageHeader';
import { Select } from '../components/ui/Select';

export function IpamPage() {
  const { t } = useTranslation();
  const canWrite = useHasPermission('ipam:write') === true;
  const canManageVRF = useHasPermission('vrf:manage') === true;
  const orgWide = useOrgWideWriter() === true;
  // The tab stays in the query (?tab=), so a reload keeps it.
  const [params, setParams] = useSearchParams();
  const requested = params.get('tab') as IpamTab | null;
  const tab: IpamTab = requested && IPAM_TABS.includes(requested) ? requested : 'subnets';
  const chooseTab = (next: IpamTab) =>
    setParams(
      (current) => {
        const copy = new URLSearchParams(current);
        if (next === 'subnets') copy.delete('tab');
        else copy.set('tab', next);
        return copy;
      },
      { replace: true },
    );
  const sections = useIPAMSections();
  const vrfs = useVrfs();
  const [chosen, setChosen] = useState<string>();
  const [vrfId, setVrfId] = useState('');
  const [clientId, setClientId] = useState('');
  const [siteId, setSiteId] = useState('');
  const [q, setQ] = useState('');
  const [containsInput, setContainsInput] = useState('');
  const [contains, setContains] = useState('');
  const [dialog, setDialog] = useState<'create' | 'edit' | 'subnet' | 'io' | undefined>();
  const ioRights = useIpamIORights();
  const canIO = !!ioRights && (ioRights.import || ioRights.export);
  const navigate = useNavigate();
  const clients = useClients();
  const sites = useSites(clientId || undefined);

  const list = sections.data?.data ?? [];
  const section: IPAMSection | undefined =
    list.find((s) => s.id === chosen) ?? list.find((s) => s.is_default) ?? list[0];

  const vrfNames = useMemo(
    () => Object.fromEntries((vrfs.data?.data ?? []).map((v) => [v.id, v.name])),
    [vrfs.data],
  );
  const query = {
    section_id: section?.id,
    vrf_id: vrfId || undefined,
    client_id: clientId || undefined,
    site_id: siteId || undefined,
    q: q.trim() || undefined,
  };
  const chain = useSubnetChain(
    { section_id: section?.id, vrf_id: vrfId || undefined, contains },
    !!section && contains !== '',
  );
  // The chain is narrowest first: the others are opened, the first marked.
  const chainKey = (chain.data?.data ?? []).map((s) => s.id).join(',');
  const chainIds = useMemo(() => (chainKey ? chainKey.split(',') : []), [chainKey]);
  const expanded = useMemo(() => new Set(chainIds.slice(1)), [chainIds]);
  const marked = chainIds[0];

  const applyContains = (event: FormEvent) => {
    event.preventDefault();
    setContains(containsInput.trim());
  };

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('ipam.title')}
        description={t('ipam.description')}
        actions={
          (canWrite || canIO) && (
            <>
              {canIO && (
                <Button variant="secondary" icon={<ArrowDownUp />} onClick={() => setDialog('io')}>
                  {t('ipam.io.action')}
                </Button>
              )}
              {canWrite && (
                <>
                  <Button variant="secondary" icon={<Plus />} onClick={() => setDialog('subnet')}>
                    {t('ipam.subnetForm.new')}
                  </Button>
                  <Button icon={<Plus />} onClick={() => setDialog('create')}>
                    {t('ipam.section.new')}
                  </Button>
                </>
              )}
            </>
          )
        }
      />

      <IpamTabList tab={tab} onChange={chooseTab} />

      {tab === 'vlans' && (
        <div role="tabpanel" id="ipam-panel-vlans" aria-labelledby="ipam-tab-vlans">
          <VlanTab canWrite={canWrite} orgWide={orgWide} />
        </div>
      )}
      {tab === 'vrfs' && (
        <div role="tabpanel" id="ipam-panel-vrfs" aria-labelledby="ipam-tab-vrfs">
          <VrfTab canManage={canManageVRF && orgWide} />
        </div>
      )}
      {tab === 'nameservers' && (
        <div role="tabpanel" id="ipam-panel-nameservers" aria-labelledby="ipam-tab-nameservers">
          <NameserverTab canWrite={canWrite && orgWide} />
        </div>
      )}

      {tab === 'subnets' && (
        <div
          role="tabpanel"
          id="ipam-panel-subnets"
          aria-labelledby="ipam-tab-subnets"
          className="space-y-6"
        >
          {sections.isError ? (
            <ErrorState title={t('ipam.loadFailed')} />
          ) : (
            <Card className="space-y-4 p-4">
              <div className="flex flex-wrap items-end gap-3">
                <Select
                  label={t('ipam.section.label')}
                  value={section?.id ?? ''}
                  onChange={(event) => setChosen(event.target.value)}
                  options={list.map((s) => ({
                    value: s.id,
                    label: s.is_default ? t('ipam.section.defaultName', { name: s.name }) : s.name,
                  }))}
                />
                {section && (
                  <Button
                    variant="secondary"
                    icon={<Settings2 />}
                    onClick={() => setDialog('edit')}
                  >
                    {canWrite ? t('ipam.section.edit') : t('ipam.section.show')}
                  </Button>
                )}
                {section?.strict_mode && <Badge>{t('ipam.section.strictMode')}</Badge>}
                {section?.acl_enabled && <Badge>{t('ipam.section.restricted')}</Badge>}
              </div>

              <div
                className="grid gap-3 sm:grid-cols-2 lg:grid-cols-5"
                role="group"
                aria-label={t('ipam.filters.label')}
              >
                <Select
                  label={t('ipam.filters.vrf')}
                  value={vrfId}
                  onChange={(event) => setVrfId(event.target.value)}
                  options={[
                    { value: '', label: t('ipam.filters.all') },
                    ...(vrfs.data?.data ?? []).map((v) => ({ value: v.id, label: v.name })),
                  ]}
                />
                <Select
                  label={t('ipam.filters.client')}
                  value={clientId}
                  onChange={(event) => {
                    setClientId(event.target.value);
                    setSiteId('');
                  }}
                  options={[
                    { value: '', label: t('ipam.filters.all') },
                    ...(clients.data?.data ?? []).map((c) => ({ value: c.id, label: c.name })),
                  ]}
                />
                <Select
                  label={t('ipam.filters.site')}
                  value={siteId}
                  onChange={(event) => setSiteId(event.target.value)}
                  options={[
                    { value: '', label: t('ipam.filters.all') },
                    ...(sites.data?.data ?? []).map((s) => ({ value: s.id, label: s.name })),
                  ]}
                />
                <Input
                  label={t('ipam.filters.q')}
                  value={q}
                  onChange={(event) => setQ(event.target.value)}
                  placeholder={t('ipam.filters.qPlaceholder')}
                />
                <form onSubmit={applyContains} className="flex items-end gap-2">
                  <Input
                    label={t('ipam.filters.contains')}
                    value={containsInput}
                    onChange={(event) => setContainsInput(event.target.value)}
                    placeholder="10.0.0.1"
                  />
                  <Button type="submit" variant="secondary">
                    {t('ipam.filters.find')}
                  </Button>
                </form>
              </div>
              {contains && chain.isError && (
                <p role="alert" className="text-sm text-red-700 dark:text-red-300">
                  {t('ipam.filters.containsInvalid')}
                </p>
              )}
              {contains && chain.isSuccess && chainIds.length === 0 && (
                <p role="status" className="text-sm text-fg-muted">
                  {t('ipam.filters.containsNone', { address: contains })}
                </p>
              )}
            </Card>
          )}

          {section && (
            <Card className="p-4">
              <h2 className="mb-3 flex items-center gap-2 text-base font-semibold text-fg">
                <Network className="h-4 w-4" aria-hidden />
                {t('ipam.tree.title', { section: section.name })}
              </h2>
              {(!contains || chain.isSuccess || chain.isError) && (
                <SubnetTree
                  key={`${section.id}|${contains}|${marked ?? ''}`}
                  query={query}
                  vrfNames={vrfNames}
                  expanded={expanded}
                  marked={marked}
                />
              )}
            </Card>
          )}
        </div>
      )}

      {dialog === 'io' && (
        <IpamImportDialog
          open
          onOpenChange={(open) => !open && setDialog(undefined)}
          filters={{ ...query, contains: contains || undefined }}
          labels={{
            section_id: section?.name,
            vrf_id: vrfId ? vrfNames[vrfId] : undefined,
            client_id: (clients.data?.data ?? []).find((c) => c.id === clientId)?.name,
            site_id: (sites.data?.data ?? []).find((s) => s.id === siteId)?.name,
          }}
        />
      )}
      {dialog === 'create' && (
        <SectionDialog
          open
          onOpenChange={(open) => !open && setDialog(undefined)}
          canWrite={canWrite}
          onCreated={(created) => setChosen(created.id)}
        />
      )}
      {dialog === 'subnet' && (
        <SubnetFormModal
          open
          onOpenChange={(open) => !open && setDialog(undefined)}
          sectionId={section?.id}
          onSaved={(created) => navigate(`/ipam/subnets/${created.id}`)}
        />
      )}
      {dialog === 'edit' && section && (
        <SectionDialog
          open
          onOpenChange={(open) => !open && setDialog(undefined)}
          section={section}
          canWrite={canWrite}
          onDeleted={() => setChosen(undefined)}
        />
      )}
    </div>
  );
}
