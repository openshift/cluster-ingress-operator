# UDPRoute status schema regression fixture

`udproute-v1.6.2-status-array-schema.json` records the status object type,
required `parents` property, and array type from the v1 UDPRoute CRD in
Gateway API v1.6.2. The regression tests use these constraints to verify that
removing the last CIO-owned parent leaves a schema-valid empty `parents` array
instead of omitting or nulling the field.

This fixture is static test data. It neither installs a CRD nor changes the
operator's managed CRD inventory.
