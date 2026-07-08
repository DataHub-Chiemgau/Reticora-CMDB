# Collector profiles

Vendor profiles define discovery and collection behavior for specific device families.

Typical profile data includes:
- OID-to-attribute mappings for SNMP collection
- Command sets and parsers for SSH-based collection
- Endpoint and resource mappings for Redfish or vendor APIs
- Attribute defaults or normalization rules shared across plugins

Profiles are intended to keep protocol plugins generic while capturing vendor-specific knowledge in declarative data.
