---
name: "OSSM Version Backport Strategy"
classification: "backport-strategy"
scope: "release"
status: "draft"
last_updated: "2026-08-14"
description: |
  Comprehensive policy for when and how to bump OSSM and Istio versions
  across OCP z-streams, accounting for EUS lifecycle constraints and risk mitigation.
---

# OSSM Version Backport Strategy

## Overview

This document defines the strategy for managing OSSM (OpenShift Service Mesh) and Istio version bumps across supported OCP z-streams. Today, there is no written policy for when or how we bump these dependencies, creating risk around:

- Carrying an EOL OSSM version in a supported OCP release
- Deferring Istio minor bumps into the EUS window where they are harder to test and riskier to ship
- Inconsistent approaches between OLM-based releases (4.19–4.21) and Sail Library releases (4.22+)

**Scope:** This strategy applies to all OCP versions that ship with OSSM (currently 4.19+), with special attention to EUS releases (4.20, 4.22).

**Intended Audience:** Network Edge team, OSSM stakeholders, Product Management, Release Engineering.

## Decision Framework

### When to Bump OSSM Z-Stream Versions

An OSSM z-stream version bump (e.g., 3.1.5 → 3.1.6) should be considered when:

- **Critical Security Updates:** CVE fixes deemed important or critical
- **Bug Fixes for Customer Issues:** Documented customer impact or production blockers
- **EOL Mitigation:** Preventing an OCP release from carrying an OSSM version past its EOL

Z-stream bumps are lower risk and strongly preferred over minor version bumps.

### When to Bump OSSM Minor Versions in OCP Z-Streams

An OSSM minor version bump (e.g., 3.1 → 3.2) should be considered when:

- **Strategic Business Driver:** Aligned with product roadmap or customer adoption needs
- **EUS Support Window:** Required to ensure an EUS release doesn't outlive the OSSM support window
- **Risk Acceptance:** Reviewed and approved by both Network Edge and OSSM stakeholders
- **Testing Investment:** Adequate testing and validation plan in place

Minor version bumps carry higher risk and require explicit approval from Product Management.

### Istio Z-Stream vs Minor Version Policy

- **Istio Z-Stream Bumps:** Follow same criteria as OSSM z-stream bumps
- **Istio Minor Version Bumps:** Tightly coupled to OSSM minor version bumps; do not bump Istio minor independently

We do not pin to a specific Istio z-stream version within an OSSM release; instead, we let it float with the OSSM version to receive ongoing maintenance.

## Risk Analysis

### OLM-Based Releases (4.19–4.21)

- **Deployment Model:** OSSM installed via OLM operator
- **Version Update Path:** Customer-initiated; requires operator subscription update
- **Testing Scope:** Must validate istiod deployment and Gateway API functionality
- **Rollback Complexity:** Moderate; requires subscription change or manual intervention

### Sail Library Releases (4.22+)

- **Deployment Model:** OSSM compiled into operator binary (Sail Library)
- **Version Update Path:** Tied to operator deployment; no separate subscription
- **Testing Scope:** Broader; changes to operator itself, tighter integration testing needed
- **Rollback Complexity:** Higher; requires operator rollback or manual CRD management
- **Flexibility:** More control over version, but version changes more tightly coupled to operator releases

### EUS Lifecycle Implications

EUS releases have extended support lifecycles that may outlive the OSSM support window:

- Example: OCP 4.20 EUS (released Q3 2024) may have support extending into 2026, while OSSM 3.1 (shipped with 4.20) reaches EOL in 2025
- **Risk:** Carrying an unsupported OSSM version in a supported OCP release
- **Mitigation Strategy:** Plan minor version bumps during full-support window (z-stream phase) rather than deferring to EUS window

## Implementation Guide

### Before Backporting: Approval & Planning

1. **Assess Business Driver:** Why are we bumping? Document CVE severity, customer impact, or strategic value
2. **Stakeholder Alignment:** For minor version bumps, gain approval from:
   - OSSM team (compatibility, support implications)
   - Product Management (business justification)
   - Release Engineering / ART (advisory planning)
3. **Create Epic/Story:** Track in Jira (NE project or OCPSTRAT)
   - Epic: Backport strategy and overall plan
   - Individual OCPBUGS: One per target OCP release

### Testing & Validation

1. **Istiod Deployment:** Verify istiod pods deploy correctly on all supported worker node types
2. **Gateway API Functionality:** Test that Gateway API resources work correctly with bumped OSSM version
3. **Downstream Dependencies:** Validate any layered products relying on these OSSM/Gateway API CRDs
4. **Regression Testing:** Run existing ingress/networking e2e tests to ensure no regressions

### Release Engineering Handoff

1. **Individual OCPBUGS Bugs:** File one per OCP release (e.g., OCPBUGS-84836 for 4.20)
2. **Target Version Field:** Set to the z-stream you're targeting (e.g., `openshift-4.20.z`)
3. **ART Coordination:** Release Engineering uses these bugs to populate errata advisories
4. **CRD Tracking:** If bumping Gateway API CRDs, coordinate with gateway-api-aware teams (RHOAI, etc.)

## Examples & Case Studies

### Example 1: Z-Stream Security Update (Lower Risk)

**Scenario:** OSSM 3.1.5 → 3.1.6 in OCP 4.19

- **Driver:** Critical CVE in Envoy (OSSM's data plane)
- **Approval:** Security team approval; no PM review required
- **Testing:** Validate istiod deployment and basic Gateway API functionality
- **Timeline:** Can move quickly; 1-2 weeks from approval to z-stream release
- **Outcome:** Low-risk change; benefits all customers using OCP 4.19

### Example 2: EUS Minor Version Bump (Higher Risk, Strategic)

**Scenario:** OSSM 3.1 → 3.4 in OCP 4.20 (EUS)

- **Driver:** OSSM 3.1 reaches EOL before OCP 4.20 EUS window ends; 3.4 has extended support
- **Approval:** OSSM, Network Edge, and PM alignment required
- **Testing:** Full regression test suite; validation with RHOAI and other OSSM consumers
- **Timeline:** Requires 2-3 weeks for testing and coordination
- **Coordination:** Announce to layered products 2+ weeks before z-stream inclusion
- **Outcome:** Ensures OCP 4.20 doesn't carry EOL OSSM version; aligns with EUS support model

### Example 3: Backport Not Recommended

**Scenario:** Request to bump Istio from 1.27 → 1.28 in OCP 4.21 during EUS window

- **Issue:** Minor version bump during EUS window adds risk; would introduce new features late in support cycle
- **Decision:** Defer to next major OCP release; backport only z-stream Istio fixes to 4.21
- **Alternative:** If customer requires 1.28 features, guide them to upgrade to newer OCP version

## Trade-offs & Rationale

### Why Z-Stream Bumps Are Preferred

- **Lower Risk:** Security and bug fixes only; no new features that could introduce regressions
- **Easier Testing:** Scope is bounded; less likely to break existing configurations
- **Customer-Friendly:** Transparent update path; lower chance of unexpected behavior changes
- **Risk/Benefit Aligned:** Maintenance benefit (bug fixes, security) outweighs change risk

### Why Minor Version Bumps Are Discouraged in Z-Streams

- **Introduces New Features:** More surface area for bugs and regressions
- **EUS Risk:** Late-cycle feature introduction in long-support releases is risky
- **Testing Burden:** Requires comprehensive regression testing that z-stream timeline doesn't always allow
- **Support Implications:** New features need new support procedures; harder to document in z-stream

### Why We Couple Istio to OSSM Minor Versions

- **Semantic Coupling:** Istio and OSSM versioning are semantically coupled; each OSSM release pins specific Istio versions
- **Testing Benefit:** OSSM team has tested that pairing; we gain their testing investment
- **Avoid Drift:** Independent Istio bumps could create untested combinations

### Future Vision: Decoupled Versioning

Long-term, we may decouple OSSM/Istio from OCP release cycles, allowing users to select any supported OSSM version as the default Gateway API provider. This would reduce backport friction, but requires:

- **CRD Management:** Solving Gateway API CRD version negotiation across OSSM versions
- **Support Matrix:** Clear documentation of which OSSM versions work with which OCP versions
- **Test Coverage:** Automated testing across the combinatorial matrix

Until this is resolved, we remain coupled to OCP release cycles.

## Related Policies

- [OCP Release Process](link/to/ocp-release-policy) — How z-streams are cut and managed
- [EUS Lifecycle Policy](link/to/eus-policy) — Support timelines for EUS releases
- [OSSM Support Matrix](link/to/ossm-support) — Which OSSM versions are supported on which OCP versions
- [Gateway API Backport Policy](link/to/gateway-api-policy) — Coordinate with OSSM backports when bumping Gateway API CRDs
- [Security Advisory Process](link/to/security-policy) — Process for critical CVE backports

## Change History

| Version | Date | Changes |
|---------|------|---------|
| 1.0 (Draft) | 2026-08-14 | Initial strategy document; establishes decision framework and risk analysis |

---

**For questions or feedback on this strategy, contact the Network Edge team or open an issue in the cluster-ingress-operator repository.**
