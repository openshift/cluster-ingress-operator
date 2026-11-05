# Permanent UDPRoute schema regression fixture

`udproute-v1.6.2-status-array-schema.json` preserves the status object type,
required `parents` property and its array type from the v1 UDPRoute CRD in
Gateway API v1.6.2, as retained in CIO commit
`3c2276b43ea90e70cbcbcccfac5f56db86b47bed` at
`pkg/manifests/assets/gateway-api/gateway.networking.k8s.io_udproutes.yaml`.
The regression tests validate these exact constraints after sole-owned-parent
cleanup, independently of the temporary runtime CRD staging asset.
This fixture neither installs a CRD nor changes the operator's managed inventory.
