// Copyright Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package install

import (
	"context"
	"fmt"
	"io/fs"
	"strings"

	v1 "github.com/istio-ecosystem/sail-operator/api/v1"
	"github.com/istio-ecosystem/sail-operator/chart/crds"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// OverwriteOLMManagedCRDFunc is called when a CRD is detected with OLM ownership
// labels. The CRD object is provided so the callback can inspect OLM
// annotations/labels to determine whether the owning subscription still exists.
// Return true to overwrite the CRD (take ownership), false to leave it alone.
type OverwriteOLMManagedCRDFunc func(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) bool

// CRD ownership labels and annotations.
const (
	// labelManagedByCIO indicates the CRD is managed by the Cluster Ingress Operator.
	labelManagedByCIO = "ingress.operator.openshift.io/owned"

	// labelOLMManaged indicates the CRD is managed by OLM (OSSM subscription).
	labelOLMManaged = "olm.managed"

	// annotationHelmKeep prevents Helm from deleting the CRD during uninstall.
	annotationHelmKeep = "helm.sh/resource-policy"
)

// CRDManagementState represents the aggregate ownership state of Istio CRDs on the cluster.
type CRDManagementState string

const (
	// CRDManagedByCIO means all target CRDs are owned by the Cluster Ingress Operator.
	// CRDs will be installed or updated.
	CRDManagedByCIO CRDManagementState = "ManagedByCIO"

	// CRDManagedByOLM means all target CRDs are owned by an OSSM subscription via OLM.
	// CRDs are left alone; Helm install still proceeds.
	CRDManagedByOLM CRDManagementState = "ManagedByOLM"

	// CRDUnknownManagement means one or more CRDs exist but are not owned by CIO or OLM.
	// CRDs are left alone; Helm install still proceeds; Status.Error is set.
	CRDUnknownManagement CRDManagementState = "UnknownManagement"

	// CRDMixedOwnership means CRDs have inconsistent ownership (some CIO, some OLM, some unknown, or some missing).
	// CRDs are left alone; Helm install still proceeds; Status.Error is set.
	CRDMixedOwnership CRDManagementState = "MixedOwnership"

	// CRDNoneExist means no target CRDs exist on the cluster yet.
	// CRDs will be installed with CIO ownership labels.
	CRDNoneExist CRDManagementState = "NoneExist"
)

// CRDInfo describes the state of a single CRD on the cluster.
type CRDInfo struct {
	// Name is the CRD name, e.g. "wasmplugins.extensions.istio.io"
	Name string

	// State is the ownership state of this specific CRD.
	// Only meaningful when Found is true.
	State CRDManagementState

	// Found indicates whether this CRD exists on the cluster.
	Found bool
}

// crdResult bundles the outcome of CRD reconciliation.
type crdResult struct {
	// State is the aggregate ownership state of the target Istio CRDs.
	State CRDManagementState

	// CRDs contains per-CRD detail (name, ownership, found on cluster).
	CRDs []CRDInfo

	// Message is a human-readable description of the CRD state.
	Message string

	// Error is non-nil if CRD management encountered a problem.
	Error error
}

// crdManager encapsulates all CRD classification, installation, and update logic.
// It provides a single entry point (Reconcile) and keeps a client dependency
// that can be swapped out in tests.
type crdManager struct {
	cl client.Client
}

// newCRDManager creates a crdManager with the given Kubernetes client.
func newCRDManager(cl client.Client) *crdManager {
	return &crdManager{cl: cl}
}

// classifyCRD checks a single CRD on the cluster and returns its ownership state.
// If overwriteOLM is non-nil and the CRD has OLM labels, it is called to decide
// whether to reclassify the CRD as CIO-managed (allowing adoption).
func (m *crdManager) classifyCRD(ctx context.Context, crdName string, overwriteOLM OverwriteOLMManagedCRDFunc) CRDInfo {
	existing := &apiextensionsv1.CustomResourceDefinition{}
	err := m.cl.Get(ctx, client.ObjectKey{Name: crdName}, existing)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return CRDInfo{Name: crdName, Found: false}
		}
		// Treat API errors as unknown management (we can't determine ownership)
		return CRDInfo{Name: crdName, Found: true, State: CRDUnknownManagement}
	}

	labels := existing.GetLabels()

	// Check CIO ownership
	if _, ok := labels[labelManagedByCIO]; ok {
		return CRDInfo{Name: crdName, Found: true, State: CRDManagedByCIO}
	}

	// Check OLM ownership
	if val, ok := labels[labelOLMManaged]; ok && val == "true" {
		if overwriteOLM != nil && overwriteOLM(ctx, existing) {
			return CRDInfo{Name: crdName, Found: true, State: CRDManagedByCIO}
		}
		return CRDInfo{Name: crdName, Found: true, State: CRDManagedByOLM}
	}

	// No recognized ownership labels
	return CRDInfo{Name: crdName, Found: true, State: CRDUnknownManagement}
}

// classifyCRDs checks all target CRDs on the cluster and returns the aggregate state.
func (m *crdManager) classifyCRDs(ctx context.Context, targets []string, overwriteOLM OverwriteOLMManagedCRDFunc) (CRDManagementState, []CRDInfo) {
	if len(targets) == 0 {
		return CRDNoneExist, nil
	}

	infos := make([]CRDInfo, len(targets))
	for i, target := range targets {
		infos[i] = m.classifyCRD(ctx, target, overwriteOLM)
	}

	return aggregateCRDState(infos), infos
}

// aggregateCRDState derives the batch state from individual CRD states.
//
// Rules:
//   - All not found → CRDNoneExist
//   - All found are CIO or unknown (no OLM), with at least one CIO → CRDManagedByCIO
//     (unknown = label drift, missing = deleted; both get fixed by updateCRDs)
//   - All found OLM, all present → CRDManagedByOLM
//   - Pure unknown (no CIO, no OLM) → CRDUnknownManagement
//   - Any mix involving OLM → CRDMixedOwnership
func aggregateCRDState(infos []CRDInfo) CRDManagementState {
	if len(infos) == 0 {
		return CRDNoneExist
	}

	var foundCount, cioCount, olmCount, unknownCount int
	for _, info := range infos {
		if !info.Found {
			continue
		}
		foundCount++
		switch info.State {
		case CRDManagedByCIO:
			cioCount++
		case CRDManagedByOLM:
			olmCount++
		default:
			unknownCount++
		}
	}

	total := len(infos)

	// None exist on cluster
	if foundCount == 0 {
		return CRDNoneExist
	}

	// All found are CIO-owned (possibly with some missing or some that lost labels).
	// No OLM involvement means we can safely reclaim unknowns and reinstall missing.
	if cioCount > 0 && olmCount == 0 {
		return CRDManagedByCIO
	}

	// All found and all OLM — only if none are missing
	if foundCount == total && olmCount == total {
		return CRDManagedByOLM
	}

	// Pure unknown — no CIO, no OLM labels on any found CRD
	if unknownCount > 0 && cioCount == 0 && olmCount == 0 {
		return CRDUnknownManagement
	}

	// Anything else is mixed: CIO+OLM, OLM with missing, etc.
	return CRDMixedOwnership
}

// Reconcile classifies target CRDs and installs/updates them if we own them (or none exist).
// This is the single entry point for CRD management.
func (m *crdManager) Reconcile(ctx context.Context, values *v1.Values, includeAllCRDs bool, overwriteOLM OverwriteOLMManagedCRDFunc) crdResult {
	targets, err := targetCRDsFromValues(values, includeAllCRDs)
	if err != nil {
		return crdResult{State: CRDNoneExist, Error: fmt.Errorf("failed to determine target CRDs: %w", err)}
	}
	if len(targets) == 0 {
		return crdResult{State: CRDNoneExist, Message: "no target CRDs configured"}
	}

	state, infos := m.classifyCRDs(ctx, targets, overwriteOLM)

	switch state {
	case CRDNoneExist:
		// Install all with CIO labels
		if err := m.installCRDs(ctx, targets); err != nil {
			return crdResult{State: state, CRDs: infos, Error: fmt.Errorf("failed to install CRDs: %w", err)}
		}
		// Update infos to reflect new state
		for idx := range infos {
			infos[idx].Found = true
			infos[idx].State = CRDManagedByCIO
		}
		return crdResult{State: CRDManagedByCIO, CRDs: infos, Message: "CRDs installed by CIO"}

	case CRDManagedByCIO:
		// Update existing, reinstall missing, re-label unknowns
		missing := missingCRDNames(infos)
		unlabeled := unlabeledCRDNames(infos)
		if err := m.updateCRDs(ctx, targets); err != nil {
			return crdResult{State: state, CRDs: infos, Error: fmt.Errorf("failed to update CRDs: %w", err)}
		}
		// Update infos for any previously-missing or unlabeled CRDs
		for idx := range infos {
			if !infos[idx].Found || infos[idx].State == CRDUnknownManagement {
				infos[idx].Found = true
				infos[idx].State = CRDManagedByCIO
			}
		}
		msg := "CRDs updated by CIO"
		if len(missing) > 0 {
			msg = fmt.Sprintf("CRDs updated by CIO; reinstalled: %s", strings.Join(missing, ", "))
		}
		if len(unlabeled) > 0 {
			msg += fmt.Sprintf("; reclaimed: %s", strings.Join(unlabeled, ", "))
		}
		return crdResult{State: CRDManagedByCIO, CRDs: infos, Message: msg}

	case CRDManagedByOLM:
		return crdResult{State: CRDManagedByOLM, CRDs: infos, Message: "CRDs managed by OSSM subscription via OLM"}

	case CRDUnknownManagement:
		missing := missingCRDNames(infos)
		msg := "CRDs exist with unknown management"
		if len(missing) > 0 {
			msg += fmt.Sprintf("; missing from other owner: %s", strings.Join(missing, ", "))
		}
		return crdResult{State: CRDUnknownManagement, CRDs: infos, Message: msg,
			Error: fmt.Errorf("Istio CRDs are managed by an unknown party")}

	case CRDMixedOwnership:
		missing := missingCRDNames(infos)
		msg := "CRDs have mixed ownership"
		if len(missing) > 0 {
			msg += fmt.Sprintf("; missing: %s", strings.Join(missing, ", "))
		}
		return crdResult{State: CRDMixedOwnership, CRDs: infos, Message: msg,
			Error: fmt.Errorf("Istio CRDs have mixed ownership (CIO/OLM/other)")}

	default:
		return crdResult{State: state, CRDs: infos}
	}
}

// WatchTargets computes the set of CRD names that should be watched for changes.
// Returns nil if the target set cannot be determined.
func (m *crdManager) WatchTargets(values *v1.Values, includeAllCRDs bool) map[string]struct{} {
	var targets []string
	var err error

	if includeAllCRDs {
		targets, err = allIstioCRDs()
	} else if values != nil && values.Pilot != nil && values.Pilot.Env != nil {
		targets, err = targetCRDsFromValues(values, false)
	}

	if err != nil || len(targets) == 0 {
		return nil
	}

	names := make(map[string]struct{}, len(targets))
	for _, name := range targets {
		names[name] = struct{}{}
	}
	return names
}

// missingCRDNames returns the names of CRDs that were not found on the cluster.
func missingCRDNames(infos []CRDInfo) []string {
	var missing []string
	for _, info := range infos {
		if !info.Found {
			missing = append(missing, info.Name)
		}
	}
	return missing
}

// unlabeledCRDNames returns names of CRDs that exist but have unknown management (no CIO/OLM labels).
func unlabeledCRDNames(infos []CRDInfo) []string {
	var names []string
	for _, info := range infos {
		if info.Found && info.State == CRDUnknownManagement {
			names = append(names, info.Name)
		}
	}
	return names
}

// installCRDs installs all target CRDs with CIO ownership labels and Helm keep annotation.
func (m *crdManager) installCRDs(ctx context.Context, targets []string) error {
	for _, resource := range targets {
		crd, err := loadCRD(resource)
		if err != nil {
			return err
		}
		applyCIOLabels(crd)
		if err := m.cl.Create(ctx, crd); err != nil {
			return fmt.Errorf("failed to create CRD %s: %w", crd.Name, err)
		}
	}
	return nil
}

// updateCRDs updates existing CIO-owned CRDs and creates any missing ones.
func (m *crdManager) updateCRDs(ctx context.Context, targets []string) error {
	for _, resource := range targets {
		crd, err := loadCRD(resource)
		if err != nil {
			return err
		}
		applyCIOLabels(crd)

		existing := &apiextensionsv1.CustomResourceDefinition{}
		if err := m.cl.Get(ctx, client.ObjectKey{Name: crd.Name}, existing); err != nil {
			if apierrors.IsNotFound(err) {
				// CRD was deleted — reinstall it
				if err := m.cl.Create(ctx, crd); err != nil {
					return fmt.Errorf("failed to create CRD %s: %w", crd.Name, err)
				}
				continue
			}
			return fmt.Errorf("failed to get existing CRD %s: %w", crd.Name, err)
		}
		crd.ResourceVersion = existing.ResourceVersion
		if err := m.cl.Update(ctx, crd); err != nil {
			return fmt.Errorf("failed to update CRD %s: %w", crd.Name, err)
		}
	}
	return nil
}

// loadCRD reads and unmarshals a CRD from the embedded filesystem.
func loadCRD(resource string) (*apiextensionsv1.CustomResourceDefinition, error) {
	filename := resourceToCRDFilename(resource)
	if filename == "" {
		return nil, fmt.Errorf("invalid resource name: %s", resource)
	}

	data, err := fs.ReadFile(crds.FS, filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read CRD file %s: %w", filename, err)
	}

	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := yaml.Unmarshal(data, crd); err != nil {
		return nil, fmt.Errorf("failed to unmarshal CRD %s: %w", filename, err)
	}
	return crd, nil
}

// applyCIOLabels sets the CIO ownership label and Helm keep annotation on a CRD.
func applyCIOLabels(crd *apiextensionsv1.CustomResourceDefinition) {
	labels := crd.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[labelManagedByCIO] = "true"
	crd.SetLabels(labels)

	annotations := crd.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[annotationHelmKeep] = "keep"
	crd.SetAnnotations(annotations)
}
