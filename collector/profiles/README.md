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

## Classification (spec §5.4)

Every JSON file in `data/` is embedded into the collector binary. The SNMP
plugin resolves a device's sysObjectID via `Registry.BySysObjectID` (longest
prefix wins, so a model profile beats a generic vendor profile) and merges the
profile's vendor/model/attributes into the result. `Registry.VendorByMAC`
resolves a vendor from a MAC OUI when only a MAC is known.

Adding support for a new device family means dropping a JSON file into
`data/` — no code change required. `TestEmbeddedProfileData` validates every
shipped file in CI.

Currently shipped: Cisco, Juniper, Arista, Fortinet, HPE (ProCurve/iLO), Dell
(OS10/iDRAC), MikroTik, Ubiquiti, Synology, NetApp, APC, Supermicro, Lenovo
plus generic SNMP/SSH fallbacks.
