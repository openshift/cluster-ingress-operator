# Temporary runtime staging: remove only after review and CI evidence

The adjacent `temporary-udproute-v1.6.2-crd.yaml` is the exact upstream
Gateway API v1.6.2 UDPRoute asset preserved in CIO commit
`3c2276b43ea90e70cbcbcccfac5f56db86b47bed` at
`pkg/manifests/assets/gateway-api/gateway.networking.k8s.io_udproutes.yaml`.
It is only embedded by `test/e2e/temporary_udproute_crd_test.go`.
That e2e-only file installs the CRD if absent when the real
`TestGatewayAPI/testUDPRouteNotAccepted` consumer runs. It does not overwrite
an existing CRD, add production CRD loaders or change managed inventory.
It never deletes the CRD or existing cluster resources as cleanup.

Keep this asset, this note, and `temporary_udproute_crd_test.go` isolated in a
later test-staging commit. Do not remove them before human review and
per-revision CI proof. The permanent test in `udp_route_test.go` requires an
externally provided compatible UDPRoute CRD after staging removal; it does not
skip the safeguard proof. Keep the independent permanent unit/schema fixtures.
