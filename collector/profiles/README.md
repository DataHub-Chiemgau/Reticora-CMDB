# Collector profiles

Vendor profiles define discovery and collection behavior for specific device families.

Typical profile data includes:
- OID-to-attribute mappings for SNMP collection
- Command sets and parsers for SSH-based collection
- Endpoint and resource mappings for Redfish or vendor APIs
- Attribute defaults or normalization rules shared across plugins
- `sysObjectIds`: SNMP sysObjectID values/prefixes identifying the device family
- `ouiPrefixes`: MAC OUI prefixes (first 3 octets) identifying the vendor

Profiles are intended to keep protocol plugins generic while capturing vendor-specific knowledge in declarative data.

## Classification (DIS-02)

Every YAML file in `data/` is embedded into the collector binary;
`RETICORA_PROFILES_DIR` adds or replaces profiles (and OUI prefixes) without
a new release. `Registry.ClassifyRecord` runs one chain over every
DeviceRecord the collector produces:

1. **SNMP** – `sysObjectIds` (longest prefix) and `sysDescrPattern`; a
   matching sysDescr pattern beats a longer OID prefix, a non-matching one
   excludes the profile.
2. **Redfish** – `redfishPattern` against "manufacturer model" of Redfish
   records.
3. **Banner** – `bannerPattern` against SSH banner, uname, OS name and the
   NAS/UPS identification; `priority` decides between matches (generic
   Linux is -10).

A match sets CI type (`ciType`, a system type), vendor, model, `plugin` and
`systemTemplate` (attributes `plugin`, `system_template`, `classified_by`).
Without a match the record becomes `generic_device` with
`classified_by: fallback`; the MAC OUI still names the vendor and the server
opens an `unclassified_device` review. Profiles without `ciType` carry
collection data only (OIDs, SSH commands, Redfish endpoints).

## System template (DIS-02)

`systemTemplate` names the system template of the device: the template of
the type catalog (`backend/internal/citype/templates.go`) whose attributes
describe the device. The profile names are kept short and readable; the
server maps them at ingest (`backend/internal/discovery/system_template.go`)
and stores the catalog key as attribute `system_template` of the CI:

| `systemTemplate` (profile) | Catalog template key |
|---|---|
| `server-bmc`, `windows-server`, `linux-server` | `physical_server` (its `server_kind` covers hardware and VMs; the OS profiles cannot tell them apart) |
| `network-switch` | `switch` |
| `router` | `router` |
| `firewall` | `firewall` |
| `access-point` | `access_point` |
| `ups` | `ups` |
| `pdu` | `pdu` |
| `hypervisor` | `hypervisor` |
| `storage-array`, `nas` | `storage` |

A value that already is a catalog key (e.g. `printer` in a profile from
`RETICORA_PROFILES_DIR`) is stored as it is. Any other value is logged as
`discovery.profile.unknown_template` and stored as no attribute; CI type and
classification are not affected. A new value in `data/` needs a row in the
mapping: `TestProfileSystemTemplatesMapToCatalog` (backend) reads every file
here and fails for a value without a catalog template.

## Profile list (CH30)

`catalog` names the entry of the confirmed CH30 profile list (E-21) a
profile covers; `TestProfileListCoversCH30` checks the list,
`TestEmbeddedProfileData` validates every shipped file.
