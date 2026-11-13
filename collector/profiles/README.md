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

## Monitoring section `monitor` (PRT-20)

Vendor-dependent probe types (routing v2 tables, BFD, optics, BNG, PON) read
their SNMP tables from the profile of the device instead of a release
(E-187 option 1: SNMP with standard MIBs as the core, vendor MIBs through
this section, gNMI as an alternative source). The key below `monitor` is a
probe type of the register (`api/probe-types.json`); each type lists its
tables by logical name:

- `columns`: logical column name → column OID (numeric, a leading dot is
  allowed),
- `index`: `int`, `ipv4`, `ipv6`, `inet` (InetAddressType + InetAddress) or
  `composite:[kind,kind,…]`,
- `enums`: logical column → raw value (an integer in decimal form, without
  leading zeros or sign `+`) → `up`, `degraded`, `down` or a raw value name,
- `scale` (finite factor above 0; default 1) and `unit`,
- `threshold_columns`: logical threshold → logical column holding it,
- `patterns`: logical quantity → RE2 expression on the entity name and
  description of a row (optical: `rx`, `tx`; an invalid expression refuses
  the profile).

The shipped `cisco.yaml` maps `bfd` onto CISCO-IETF-BFD-MIB (table
`sessions` with `state`, `diag`, `addr_type`, `addr`) and `optical` onto
CISCO-ENTITY-SENSOR-MIB (table `sensors` with `type`, `scale`,
`precision`, `value`, `status`; table `thresholds` with `severity`
mapped to `degraded`/`down`, `relation`, `value`).

Example: the peers of a BGP4-V2 table with an `inet` index and the state
`established` (6) as `up`:

```yaml
- name: example-bgp4v2
  vendor: Example
  ciType: router
  sysObjectIds:
  - 1.3.6.1.4.1.99999
  monitor:
    bgp:
      tables:
        peers:
          index: inet
          columns:
            state: 1.3.6.1.4.1.99999.5.1.2.1.13
            remote_as: 1.3.6.1.4.1.99999.5.1.2.1.10
            established_time: 1.3.6.1.4.1.99999.5.1.2.1.16
          enums:
            state:
              "1": idle
              "2": connect
              "3": active
              "6": up
        prefixes:
          index: composite:[inet, int, int]
          columns:
            accepted: 1.3.6.1.4.1.99999.5.1.6.1.3
```

Every profile is validated when it is loaded, also without `ciType`: an
unknown probe type, an invalid OID, an unknown index kind, an enum or a
threshold on an unknown column, a raw enum value that is no integer, or a
scale of 0 or below, NaN or infinite refuse the profile with
its name and the path of the failing value
(`monitor.bgp.tables.peers.index`); `LoadDir` names the file.
`Registry.MonitorFor(probeType, sysObjectID, sysDescr)` picks the profile
like the classification (longest sysObjectID prefix; a matching
`sysDescrPattern` beats a longer prefix) among the profiles with a section
for the type and returns its name; without a match the type uses its
standard-MIB core. A profile in `RETICORA_PROFILES_DIR` with the name of a
shipped profile replaces it, its `monitor` section included, without a
collector release.
