# OSSM Version Backport Strategy

## What

Applies to OCP 4.19+. Three components can be backported, moving independently
with one coupling:

1. **OSSM version** — the `sail-operator` library version vendored via
   [`go.mod`](../../go.mod).

2. **Istio version** — pulled as container images (unlike OSSM, which is vendored).
   - For OSSM 3.4+ (Sail Library), supported versions and image references are defined
     in [`versions.ossm.yaml`](../../vendor/github.com/istio-ecosystem/sail-operator/pkg/istioversion/versions.ossm.yaml)
     (this has changed across OSSM major versions).
   - Since we always use the latest Istio z-stream available in an OSSM release,
     bumping the Istio z-stream requires an OSSM bump.

3. **Gateway API CRDs** — managed by the operator; manifests live in
   [`pkg/manifests/assets/gateway-api/`](../../pkg/manifests/assets/gateway-api).
   - The supported CRD version is **gated by Istio**: a given Istio version supports a
     specific Gateway API version.
   - Istio *minor* bumps bring support for newer Gateway API CRD versions.

This chains for minor bumps: a Gateway API CRD minor bump generally requires an
Istio minor bump, which in turn requires an OSSM minor bump.

## When

**Default: keep every supported release current on z-streams.** Backport the latest
OSSM z-stream, Istio z-stream, and Gateway API patch as they become available. These
carry CVE and bug fixes and are low risk.

**Bump OSSM, Istio, or Gateway API minor only when:**
- an EUS/ELC release would otherwise carry a version past EOL, or
- a strategic driver (roadmap, customer adoption) justifies it.

Requires NID Team + OSSM + PM sign-off.

### EUS/ELC lifecycle planning

OCP EUS/ELC releases have support windows that can outlast the OSSM support lifecycle
for the version they shipped with. Track both:

- [OCP release lifecycle dates](https://access.redhat.com/support/policy/updates/openshift#dates)
- [OSSM operator lifecycle](https://access.redhat.com/support/policy/updates/openshift_operators)

OSSM carries support for approximately 3 Istio minor versions at a time. When OSSM
drops an Istio minor, any OCP EUS/ELC release still on that version loses a supported
OSSM/Istio path — forcing an Istio minor-version backport into an OCP z-stream.

**Timing.** Do the minor bump an EUS/ELC release will eventually need *during its
full-support phase*, not after it enters the extended window — bumping in the extended
window is harder to test and riskier. Watch the OSSM lifecycle and initiate the bump
early.

## How

### Version equivalence rules

Standard OCP rule: **upgrading OCP must never downgrade a component or lose a CVE /
bug fix.** For every component (OSSM, Istio, Gateway API), a newer OCP release must
be at least as new as every older one — in both version *and* fix content.

- **Cascade newest → oldest.** Land a bump in master / the newest supported release
  first, then cherry-pick down. You cannot put a version or fix in an older z-stream
  that isn't already in every newer one.
- Each older backport target's Istio version must be **no newer** — in version
  string and effective fix content (OSSM build / Istio commit SHA) — than the
  next-newer release's.

**The z-stream gotcha:** Istio patch numbers are *not* chronological across minor
lines. A higher patch on an older minor line can be newer by date — and carry newer
CVE fixes — than a lower patch on a newer minor line. Example: Istio 1.28.8 is
numerically lower than 1.29.3 but is actually newer and may contain fixes 1.29.3
lacks. So semver ordering alone does **not** prove CVE parity.
Before backporting such a patch to an older release, confirm the newer releases
already contain the same fixes (bump them too if needed) — otherwise upgrading from
the older release would silently drop a fix.

**Istio z-stream drift:** An Istio version string is not a unique identifier for a
set of fixes. OSSM can ship a new OSSM z-stream that rebuilds the Istio binary with
additional CVE patches without incrementing the Istio version — e.g. Istio 1.30.3 in
OSSM 3.4.3 may carry fixes not present in Istio 1.30.3 from OSSM 3.4.2. When
verifying CVE equivalence, check the Istio commit SHA from the OSSM release notes,
not just the version string.

### Jira tracking

All backports are tracked as **OCPBUGS**.

**Z-stream bumps — OCPBUGS anchor.** File one OCPBUGS for the current OCP version in
development and link the main branch PR to it. This is the **anchor bug**; all
backport bugs for older releases flow from it. The OSSM bump and Istio z-stream bump
are a single bug — they cannot be split. Example:
[OCPBUGS-79376](https://issues.redhat.com/browse/OCPBUGS-79376).

**Minor bumps — NE Epic/Story.** OSSM minor, Istio minor, and GWAPI minor bumps are
planned scope, not bug fixes, and are tracked as a NE Epic or Story. Because an
Istio minor requires an OSSM minor, both are covered by the same work item. Example:
[NE-2842](https://issues.redhat.com/browse/NE-2842). If a minor bump also needs
backporting to older releases (unusual), create a **dummy OCPBUGS** for the current
OCP version as a backport anchor, even though the main work lives in the Epic.

**Generating backport bugs.** Run `/jira backport <release-list>`
(e.g. `release-4.22,release-4.21,release-4.20`) on the anchor PR. This creates
one OCPBUGS per target release. After creation, manually update each bug:
- Adjust OSSM, Istio, and Gateway API versions to the **version-equivalent** for that
  OCP release's OSSM/Istio minor version — older releases typically ship a different
  OSSM/Istio minor, not the version from the current development release.

### Backport process

**For minor bumps only:** get NID Team + OSSM + PM sign-off and collect layered
product feedback (e.g. RHOAI) before proceeding.

1. **Verify the OSSM release is published.** OSSM publishes z-streams on its own
   schedule; confirm the target OSSM version is available before filing bugs.
2. **File the anchor OCPBUGS** for the current OCP version in development. For minor
   bumps being backported to older releases, file a dummy OCPBUGS even if the main
   work lives in a NE Epic. Link the anchor bug to the main branch PR.
3. **Add proposed versions to the spreadsheet.** Before merging, record the target
   OSSM, Istio, and Gateway API versions for each OCP release in the
   [version mapping spreadsheet](https://docs.google.com/spreadsheets/d/1cGLPmUtC7h5GC2i0s-EQZbu5C0Qa9H71FfJ0Q_ZvSV0/edit?gid=0#gid=0)
   as "proposed." This lets reviewers assess version equivalence and CVE/SHA evidence
   before the merge.
4. **Merge the anchor PR on the main branch.** Cascade to older releases from here.
5. **Generate backport bugs.** Run `/jira backport <release-list>`
   (e.g. `release-4.22,release-4.21,release-4.20`) on the anchor PR. For each
   generated OCPBUGS, adjust the OSSM/Istio/GWAPI versions to the equivalent for
   that OCP release's OSSM/Istio minor version.
6. **Open manual cherry-pick PRs** targeting the correct `release-4.XX` branches,
   cascading newest → oldest. Avoid `/cherrypick` — go.mod conflicts are nearly
   guaranteed, and each OCP minor needs a different version equivalent anyway. Manually
   re-implement the bump for each target branch.
   - Before each PR merges: run all GWAPI conformance variants in pre-submit E2Es;
     consider extra HyperShift testing; ensure full upgrade E2E conformance coverage.
   - Before each PR merges: announce any GWAPI CRD changes to layered-product teams.
7. **Update the version mapping spreadsheet** after all PRs merge — set the status
   to "Done" and update the OCP version column:
   [Gateway API Ingress OCP to OSSM Version Mapping](https://docs.google.com/spreadsheets/d/1cGLPmUtC7h5GC2i0s-EQZbu5C0Qa9H71FfJ0Q_ZvSV0/edit?gid=0#gid=0).

## Related

- [NE-2502](https://issues.redhat.com/browse/NE-2502) — epic
- Initial backport bugs:
  [OCPBUGS-84838](https://issues.redhat.com/browse/OCPBUGS-84838) (4.19),
  [OCPBUGS-84836](https://issues.redhat.com/browse/OCPBUGS-84836) (4.20),
  [OCPBUGS-84834](https://issues.redhat.com/browse/OCPBUGS-84834) (4.21),
  [OCPBUGS-79376](https://issues.redhat.com/browse/OCPBUGS-79376) (4.22)
