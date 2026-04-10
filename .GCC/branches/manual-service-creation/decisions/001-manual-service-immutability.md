# ADR-001: Manual Service Immutability Model

## Status
Accepted

## Context
When designing e2e tests for manual service creation with Gateway API, the fundamental question is: what does Istio do (or not do) to a pre-created service?

PR #1384 assumed a hybrid model where Istio would manage ports and selectors on pre-created LoadBalancer services but not on ClusterIP services. This assumption was incorrect.

## Decision
Tests are designed around the correct behavioral model: **Istio does NOT modify pre-created services in any way**. The user owns the entire service lifecycle.

Consequences for test design:
1. Both LB and ClusterIP tests pre-create services with ALL required configuration (ports matching listeners, selector, labels).
2. The core assertion is service immutability -- verifying no fields changed after Gateway creation or listener updates.
3. The `createGatewayService` helper always sets selector and managed label (no optional toggles).
4. The helper accepts explicit ports from the caller to make test intent clear.

## Alternatives Considered
- **Hybrid model (PR #1384 approach)**: Have Istio manage ports/selectors for LB services. Rejected because this does not match actual Istio behavior.
- **Separate helpers for LB and ClusterIP**: Create different helper functions for each service type. Rejected because the only difference is `externalTrafficPolicy` and service type; a single helper with explicit ports is cleaner.
