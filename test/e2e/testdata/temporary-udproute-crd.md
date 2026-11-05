# Temporary runtime staging: remove only after review and CI evidence

The adjacent `temporary-udproute-v1.6.2-crd.yaml` is the exact upstream
Gateway API v1.6.2 UDPRoute asset preserved in CIO commit
`3c2276b43ea90e70cbcbcccfac5f56db86b47bed` at
`pkg/manifests/assets/gateway-api/gateway.networking.k8s.io_udproutes.yaml`.
It is only embedded by `test/e2e/temporary_udproute_crd_test.go`.
That e2e-only file installs the CRD if absent when the real
`TestGatewayAPI/testUDPRouteNotAccepted` consumer runs. It does not overwrite
an existing CRD, add production CRD loaders or change managed inventory.
For a missing CRD, the temporary helpers in
`test/e2e/temporary_udproute_admission_test.go` quiesce CVO and CIO (including
waiting for their pods to disappear), temporarily remove only the admission
binding, and restore that binding before restoring the original deployment
replica counts. This extends the existing e2e CVO-pause pattern to CIO's
Managed-mode policy reconciliation, without changing management mode or the
production policy. Operations and independent restoration attempts are bounded.
Only a CRD created by this invocation is registered for cleanup, immediately
after creation. Cleanup uses its UID as a deletion precondition and never
deletes a pre-existing or replacement CRD. An AlreadyExists race is treated
as an external installation, not ownership. All errors fail setup or cleanup;
none skip runtime validation. Restoration errors are reported even when another
operation also fails. These are disruptive, serial, test-only operations, not
production installation or a live-cluster validation claim.

Keep this asset, this note, `temporary_udproute_crd_test.go`,
`temporary_udproute_admission_test.go`, and
`temporary_udproute_admission_regression_test.go` isolated in test-staging
commits and remove them together when removal is authorized. The regression
Examples execute simulated API/controller behavior, not live admission proof.
The nil-replica case exercises fake-client preservation; the live Deployment
API defaults omitted replicas to one. UID deletion preconditions are checked
explicitly by the simulation because the baseline fake client does not enforce
them. A new real CI run remains required to prove live admission and cleanup.
Do not remove them before human review and
per-revision CI proof. The permanent test in `udp_route_test.go` requires an
externally provided compatible UDPRoute CRD after staging removal; it does not
skip the safeguard proof. Keep the independent permanent unit/schema fixtures.
