package ingress

import (
	configv1 "github.com/openshift/api/config/v1"
)

// DetermineReplicas implements the replicas choice algorithm as described in
// the documentation for the IngressController replicas parameter. Used both in
// determining the number of replicas for the default IngressController and in
// determining the number of replicas in the Deployments corresponding to
// IngressController resources in which the number of replicas is unset.
// The workerCount parameter specifies the number of schedulable worker nodes
// in the cluster and is used to avoid creating unschedulable replicas on
// HyperShift hosted clusters that have zero worker nodes.
func DetermineReplicas(ingressConfig *configv1.Ingress, infraConfig *configv1.Infrastructure, workerCount int32) int32 {
	// For External control plane topology (HyperShift) with zero
	// schedulable workers, return 0 replicas to avoid creating pods
	// that can never be scheduled.
	if infraConfig.Status.ControlPlaneTopology == configv1.ExternalTopologyMode && workerCount == 0 {
		return 0
	}

	// DefaultPlacement affects which topology field we're interested in
	topology := infraConfig.Status.InfrastructureTopology
	if ingressConfig.Status.DefaultPlacement == configv1.DefaultPlacementControlPlane {
		topology = infraConfig.Status.ControlPlaneTopology
	}

	if topology == configv1.SingleReplicaTopologyMode {
		return 1
	}

	// TODO: Set the replicas value to the number of workers.
	return 2
}
