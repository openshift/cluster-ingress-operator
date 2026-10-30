//go:build e2e

package e2e

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	udproutestatuscontroller "github.com/openshift/cluster-ingress-operator/pkg/operator/controller/udproute-status"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type temporaryUDPAdmissionSimulation struct {
	client.Client
	owners              []*appsv1.Deployment
	binding             *admissionv1.ValidatingAdmissionPolicyBinding
	trace               []string
	before              func(string, client.Object) error
	pendingPods         int
	activeOwners        int
	deleteUID           types.UID
	createCount         int
	registered          []types.UID
	readDeadline        time.Time
	establishedDeadline time.Time
}

func newTemporaryUDPAdmissionSimulation() *temporaryUDPAdmissionSimulation {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, admissionv1.AddToScheme, apiextensionsv1.AddToScheme} {
		if err := add(scheme); err != nil {
			panic(err)
		}
	}
	simulation := &temporaryUDPAdmissionSimulation{activeOwners: 3}
	for index, key := range []client.ObjectKey{{Namespace: cvoNamespace, Name: cvoDeploymentName}, {Namespace: operatorNamespace, Name: "ingress-operator"}} {
		replicas := int32(index + 1)
		simulation.owners = append(simulation.owners, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, UID: types.UID(key.Name), Generation: 1},
			Spec:       appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": key.Name}}},
			Status:     appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: replicas, UpdatedReplicas: replicas, AvailableReplicas: replicas},
		})
	}
	simulation.binding = &admissionv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: gwapiCRDVAPName, UID: "original-binding", Labels: map[string]string{"preserve": "label"}, Annotations: map[string]string{"preserve": "annotation"}},
		Spec:       admissionv1.ValidatingAdmissionPolicyBindingSpec{PolicyName: gwapiCRDVAPName, ValidationActions: []admissionv1.ValidationAction{admissionv1.Deny}},
	}
	simulation.Client = fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&appsv1.Deployment{}, &apiextensionsv1.CustomResourceDefinition{}).
		WithObjects(simulation.owners[0].DeepCopy(), simulation.owners[1].DeepCopy(), simulation.binding.DeepCopy()).Build()
	return simulation
}

func (simulation *temporaryUDPAdmissionSimulation) step(action string, object client.Object) error {
	for index, owner := range simulation.owners {
		current := &appsv1.Deployment{}
		if err := simulation.Client.Get(context.Background(), client.ObjectKeyFromObject(owner), current); err != nil {
			return err
		}
		if simulation.activeOwners&(1<<index) != 0 && (current.Spec.Replicas == nil || *current.Spec.Replicas > 0) {
			binding := simulation.binding.DeepCopy()
			binding.UID = "reconciled-binding"
			binding.ResourceVersion = ""
			if err := simulation.Client.Create(context.Background(), binding); err != nil && !apierrors.IsAlreadyExists(err) {
				return err
			}
		}
	}
	simulation.trace = append(simulation.trace, action)
	if simulation.before != nil {
		return simulation.before(action, object)
	}
	return nil
}

func temporaryUDPObjectName(object client.Object) string {
	switch object.(type) {
	case *appsv1.Deployment:
		if object.GetNamespace() == cvoNamespace {
			return "cvo"
		}
		return "cio"
	case *admissionv1.ValidatingAdmissionPolicyBinding:
		return "binding"
	case *apiextensionsv1.CustomResourceDefinition:
		return "crd"
	default:
		panic(fmt.Sprintf("unexpected simulated object %T", object))
	}
}

func (simulation *temporaryUDPAdmissionSimulation) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	if _, isCRD := object.(*apiextensionsv1.CustomResourceDefinition); isCRD {
		deadline, _ := ctx.Deadline()
		if simulation.readDeadline.IsZero() {
			simulation.readDeadline = deadline
		} else if len(simulation.registered) != 0 {
			simulation.establishedDeadline = deadline
		}
	}
	object.SetName(key.Name)
	object.SetNamespace(key.Namespace)
	if err := simulation.step("get-"+temporaryUDPObjectName(object), object); err != nil {
		return err
	}
	return simulation.Client.Get(ctx, key, object, options...)
}

func (simulation *temporaryUDPAdmissionSimulation) List(ctx context.Context, list client.ObjectList, options ...client.ListOption) error {
	if err := simulation.Client.List(ctx, list, options...); err != nil {
		return err
	}
	if pods, ok := list.(*corev1.PodList); ok && simulation.pendingPods != 0 {
		pods.Items = append(pods.Items, corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "terminating-owner"}})
		if simulation.pendingPods > 0 {
			simulation.pendingPods--
		}
		simulation.trace = append(simulation.trace, "terminating-pod-observed")
	}
	return nil
}

func (simulation *temporaryUDPAdmissionSimulation) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	if err := simulation.step("create-"+temporaryUDPObjectName(object), object); err != nil {
		return err
	}
	if crd, ok := object.(*apiextensionsv1.CustomResourceDefinition); ok {
		simulation.createCount++
		binding := &admissionv1.ValidatingAdmissionPolicyBinding{}
		if err := simulation.Client.Get(ctx, client.ObjectKey{Name: gwapiCRDVAPName}, binding); err == nil {
			return apierrors.NewForbidden(schema.GroupResource{Resource: "customresourcedefinitions"}, crd.Name, errors.New("simulated enforced admission"))
		} else if !apierrors.IsNotFound(err) {
			return err
		}
		crd.UID = "created-crd"
		crd.Status.Conditions = []apiextensionsv1.CustomResourceDefinitionCondition{{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue}}
	}
	if object.GetUID() == "" {
		object.SetUID("restored-binding")
	}
	return simulation.Client.Create(ctx, object, options...)
}

func (simulation *temporaryUDPAdmissionSimulation) Update(ctx context.Context, object client.Object, options ...client.UpdateOption) error {
	if deployment, ok := object.(*appsv1.Deployment); ok && (deployment.Spec.Replicas == nil || *deployment.Spec.Replicas > 0) {
		binding := &admissionv1.ValidatingAdmissionPolicyBinding{}
		if err := simulation.Client.Get(ctx, client.ObjectKey{Name: gwapiCRDVAPName}, binding); err != nil {
			simulation.trace = append(simulation.trace, "resume-admission-missing")
		} else {
			simulation.trace = append(simulation.trace, "resume-admission-restored")
		}
	}
	if err := simulation.step("update-"+temporaryUDPObjectName(object), object); err != nil {
		return err
	}
	if err := simulation.Client.Update(ctx, object, options...); err != nil {
		return err
	}
	if deployment, ok := object.(*appsv1.Deployment); ok {
		replicas := int32(1)
		if deployment.Spec.Replicas != nil {
			replicas = *deployment.Spec.Replicas
		}
		deployment.Status = appsv1.DeploymentStatus{ObservedGeneration: deployment.Generation, Replicas: replicas, UpdatedReplicas: replicas, AvailableReplicas: replicas}
		return simulation.Client.Status().Update(ctx, deployment)
	}
	return nil
}

func (simulation *temporaryUDPAdmissionSimulation) Delete(ctx context.Context, object client.Object, options ...client.DeleteOption) error {
	if err := simulation.step("delete-"+temporaryUDPObjectName(object), object); err != nil {
		return err
	}
	if _, ok := object.(*apiextensionsv1.CustomResourceDefinition); ok {
		deleteOptions := (&client.DeleteOptions{}).ApplyOptions(options)
		if deleteOptions.Preconditions == nil || deleteOptions.Preconditions.UID == nil {
			return errors.New("cleanup omitted UID precondition")
		}
		simulation.deleteUID = *deleteOptions.Preconditions.UID
		current := &apiextensionsv1.CustomResourceDefinition{}
		if err := simulation.Client.Get(ctx, client.ObjectKeyFromObject(object), current); err != nil {
			return err
		}
		if current.UID != simulation.deleteUID {
			return apierrors.NewConflict(schema.GroupResource{Resource: "customresourcedefinitions"}, current.Name, errors.New("simulated UID precondition failed"))
		}
	}
	return simulation.Client.Delete(ctx, object, options...)
}

func (simulation *temporaryUDPAdmissionSimulation) fixture() temporaryUDPFixture {
	return temporaryUDPFixture{client: simulation, timeout: 80 * time.Millisecond, interval: time.Millisecond}
}

func (simulation *temporaryUDPAdmissionSimulation) ensure(ctx context.Context) error {
	return simulation.fixture().ensure(ctx, func(uid types.UID) {
		simulation.trace = append(simulation.trace, "register-cleanup")
		simulation.registered = append(simulation.registered, uid)
	})
}

func (simulation *temporaryUDPAdmissionSimulation) assertRestored() {
	for _, original := range simulation.owners {
		current := &appsv1.Deployment{}
		if err := simulation.Client.Get(context.Background(), client.ObjectKeyFromObject(original), current); err != nil {
			panic(err)
		}
		if !reflect.DeepEqual(current.Spec, original.Spec) {
			panic(fmt.Sprintf("owner %s not restored", original.Name))
		}
	}
	current := &admissionv1.ValidatingAdmissionPolicyBinding{}
	if err := simulation.Client.Get(context.Background(), client.ObjectKeyFromObject(simulation.binding), current); err != nil {
		panic(err)
	}
	if !reflect.DeepEqual(current.Spec, simulation.binding.Spec) || !reflect.DeepEqual(current.Labels, simulation.binding.Labels) || !reflect.DeepEqual(current.Annotations, simulation.binding.Annotations) {
		panic("binding not restored")
	}
}

func temporaryUDPCheck(err error, want string) {
	if want == "" && err != nil {
		panic(err)
	}
	if want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
		panic(fmt.Sprintf("want error containing %q, got %v", want, err))
	}
}

func temporaryUDPExisting(simulation *temporaryUDPAdmissionSimulation, uid types.UID) {
	crd := &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: udproutestatuscontroller.UDPRouteCRDName, UID: uid, Labels: map[string]string{"foreign": "untouched"}}}
	crd.Status.Conditions = []apiextensionsv1.CustomResourceDefinitionCondition{{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue}}
	if err := simulation.Client.Create(context.Background(), crd); err != nil {
		panic(err)
	}
}

func Example_temporaryUDPRouteAdmissionLifecycle() {
	for _, activeOwners := range []int{1, 2, 3} {
		simulation := newTemporaryUDPAdmissionSimulation()
		simulation.activeOwners = activeOwners
		simulation.pendingPods = 3
		temporaryUDPCheck(simulation.ensure(context.Background()), "")
		if !reflect.DeepEqual(simulation.registered, []types.UID{"created-crd"}) {
			panic("new UID cleanup not registered")
		}
		if !simulation.establishedDeadline.After(simulation.readDeadline) {
			panic("Established wait lost its own bounded phase after restoration")
		}
		simulation.assertRestored()
		joined := strings.Join(simulation.trace, ",")
		if strings.Index(joined, "delete-binding") < strings.LastIndex(joined, "terminating-pod-observed") || strings.Index(joined, "register-cleanup") > strings.Index(joined, "create-binding") {
			panic("admission/cleanup ordering was violated")
		}
		if strings.Contains(joined, "resume-admission-missing") || !strings.Contains(joined, "resume-admission-restored") {
			panic("successful setup resumed an owner before restoring admission")
		}
		temporaryUDPCheck(simulation.fixture().removeOwned(context.Background(), simulation.registered[0]), "")
		if err := simulation.Client.Get(context.Background(), client.ObjectKey{Name: udproutestatuscontroller.UDPRouteCRDName}, &apiextensionsv1.CustomResourceDefinition{}); !apierrors.IsNotFound(err) || simulation.deleteUID != "created-crd" {
			panic("owned cleanup failed or did not use created UID")
		}
		simulation.assertRestored()
	}
	fmt.Println("CVO-only, CIO-only, both: quiesce, create, restore, owned cleanup")

	for _, race := range []bool{false, true} {
		simulation := newTemporaryUDPAdmissionSimulation()
		if !race {
			temporaryUDPExisting(simulation, "foreign-crd")
		} else {
			simulation.before = func(action string, object client.Object) error {
				if action == "create-crd" {
					temporaryUDPExisting(simulation, "foreign-crd")
					return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "customresourcedefinitions"}, object.GetName())
				}
				return nil
			}
		}
		before := &apiextensionsv1.CustomResourceDefinition{}
		if !race {
			temporaryUDPCheck(simulation.Client.Get(context.Background(), client.ObjectKey{Name: udproutestatuscontroller.UDPRouteCRDName}, before), "")
		}
		temporaryUDPCheck(simulation.ensure(context.Background()), "")
		if len(simulation.registered) != 0 {
			panic("external CRD was claimed")
		}
		current := &apiextensionsv1.CustomResourceDefinition{}
		temporaryUDPCheck(simulation.Client.Get(context.Background(), client.ObjectKey{Name: udproutestatuscontroller.UDPRouteCRDName}, current), "")
		if current.UID != "foreign-crd" || (!race && !reflect.DeepEqual(before, current)) {
			panic("external CRD changed")
		}
		simulation.assertRestored()
	}
	fmt.Println("pre-existing and AlreadyExists-race CRDs preserved without ownership")

	for _, action := range []string{"get-crd", "get-cio", "update-cio", "delete-binding", "create-crd", "create-binding"} {
		simulation := newTemporaryUDPAdmissionSimulation()
		failed := false
		simulation.before = func(current string, object client.Object) error {
			if current == action && !failed {
				failed = true
				return fmt.Errorf("injected %s failure", action)
			}
			return nil
		}
		temporaryUDPCheck(simulation.ensure(context.Background()), "injected "+action+" failure")
		if action != "create-binding" {
			simulation.assertRestored()
		} else {
			if len(simulation.registered) != 1 {
				panic("restoration failure lost cleanup registration")
			}
			simulation.assertRestored()
			temporaryUDPCheck(simulation.fixture().removeOwned(context.Background(), simulation.registered[0]), "")
		}
	}
	fmt.Println("read, snapshot, pause, binding-delete, create, binding-restore errors reported")

	for _, forever := range []bool{false, true} {
		simulation := newTemporaryUDPAdmissionSimulation()
		denied := 0
		simulation.before = func(action string, object client.Object) error {
			if action == "create-crd" && (forever || denied < 2) {
				denied++
				return apierrors.NewForbidden(schema.GroupResource{Resource: "customresourcedefinitions"}, object.GetName(), errors.New("admission-cache lag"))
			}
			return nil
		}
		err := simulation.ensure(context.Background())
		if forever {
			temporaryUDPCheck(err, "admission-cache lag")
			if !errors.Is(err, context.DeadlineExceeded) || len(simulation.registered) != 0 {
				panic("permanent denial did not fail bounded without false ownership")
			}
		} else {
			temporaryUDPCheck(err, "")
			if denied != 2 || len(simulation.registered) != 1 {
				panic("finite admission-cache lag not retried")
			}
		}
		simulation.assertRestored()
	}
	fmt.Println("finite admission-cache lag recovers; permanent denial fails bounded")

	for _, forever := range []bool{false, true} {
		simulation := newTemporaryUDPAdmissionSimulation()
		temporaryUDPExisting(simulation, "created-crd")
		denied := 0
		simulation.before = func(action string, object client.Object) error {
			if action == "delete-crd" && (forever || denied < 2) {
				denied++
				return apierrors.NewForbidden(schema.GroupResource{Resource: "customresourcedefinitions"}, object.GetName(), errors.New("delete admission-cache lag"))
			}
			return nil
		}
		err := simulation.fixture().removeOwned(context.Background(), "created-crd")
		if forever {
			temporaryUDPCheck(err, "delete admission-cache lag")
			if !errors.Is(err, context.DeadlineExceeded) {
				panic("permanent delete denial did not fail bounded")
			}
		} else {
			temporaryUDPCheck(err, "")
			if denied != 2 || simulation.deleteUID != "created-crd" {
				panic("delete retry lost UID ownership")
			}
		}
		simulation.assertRestored()
	}
	fmt.Println("DELETE admission-cache lag recovers or fails bounded with owners restored")

	for _, cancelOperation := range []bool{false, true} {
		simulation := newTemporaryUDPAdmissionSimulation()
		ctx, cancel := context.WithCancel(context.Background())
		simulation.before = func(action string, object client.Object) error {
			if action == "create-crd" && cancelOperation {
				cancel()
				return ctx.Err()
			}
			if action == "get-crd" && !cancelOperation && len(simulation.registered) != 0 {
				crd := &apiextensionsv1.CustomResourceDefinition{}
				temporaryUDPCheck(simulation.Client.Get(context.Background(), client.ObjectKeyFromObject(object), crd), "")
				crd.Status.Conditions = nil
				temporaryUDPCheck(simulation.Client.Status().Update(context.Background(), crd), "")
			}
			return nil
		}
		err := simulation.ensure(ctx)
		cancel()
		if err == nil {
			panic("cancellation or Established timeout incorrectly succeeded")
		}
		simulation.assertRestored()
		if !cancelOperation {
			if len(simulation.registered) != 1 {
				panic("Established timeout lost cleanup registration")
			}
			simulation.before = nil
			temporaryUDPCheck(simulation.fixture().removeOwned(context.Background(), simulation.registered[0]), "")
		}
	}
	fmt.Println("operation cancellation and Established timeout preserve independent restoration")

	for _, race := range []bool{false, true} {
		simulation := newTemporaryUDPAdmissionSimulation()
		temporaryUDPExisting(simulation, "created-crd")
		replace := func() {
			temporaryUDPCheck(simulation.Client.Delete(context.Background(), &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: udproutestatuscontroller.UDPRouteCRDName}}), "")
			temporaryUDPExisting(simulation, "replacement-crd")
		}
		if !race {
			replace()
		} else {
			simulation.before = func(action string, object client.Object) error {
				if action == "delete-crd" {
					replace()
				}
				return nil
			}
		}
		temporaryUDPCheck(simulation.fixture().removeOwned(context.Background(), "created-crd"), "")
		current := &apiextensionsv1.CustomResourceDefinition{}
		temporaryUDPCheck(simulation.Client.Get(context.Background(), client.ObjectKey{Name: udproutestatuscontroller.UDPRouteCRDName}, current), "")
		if current.UID != "replacement-crd" {
			panic("replacement was deleted")
		}
		simulation.assertRestored()
	}
	fmt.Println("replacement before cleanup and UID-precondition delete race preserved")

	simulation := newTemporaryUDPAdmissionSimulation()
	temporaryUDPExisting(simulation, "created-crd")
	simulation.before = func(action string, object client.Object) error {
		if action == "delete-crd" {
			return errors.New("injected cleanup transport failure")
		}
		return nil
	}
	temporaryUDPCheck(simulation.fixture().removeOwned(context.Background(), "created-crd"), "cleanup transport failure")
	simulation.assertRestored()
	temporaryUDPCheck(simulation.fixture().removeOwned(context.Background(), ""), "without UID")
	fmt.Println("cleanup errors surfaced; empty UID refused")

	simulation = newTemporaryUDPAdmissionSimulation()
	simulation.pendingPods = -1
	temporaryUDPCheck(simulation.ensure(context.Background()), "deadline exceeded")
	if strings.Contains(strings.Join(simulation.trace, ","), "delete-binding") || simulation.createCount != 0 {
		panic("bypass started before owner pods terminated")
	}
	simulation.assertRestored()
	fmt.Println("nonterminating owner pods fail before bypass and restore replicas")

	simulation = newTemporaryUDPAdmissionSimulation()
	simulation.before = func(action string, object client.Object) error {
		if action == "create-crd" {
			return errors.New("injected operation failure")
		}
		if action == "update-cio" && *object.(*appsv1.Deployment).Spec.Replicas > 0 {
			return errors.New("injected CIO restoration failure")
		}
		return nil
	}
	err := simulation.ensure(context.Background())
	temporaryUDPCheck(err, "operation failure")
	temporaryUDPCheck(err, "CIO restoration failure")
	cvo := &appsv1.Deployment{}
	temporaryUDPCheck(simulation.Client.Get(context.Background(), client.ObjectKeyFromObject(simulation.owners[0]), cvo), "")
	if !reflect.DeepEqual(cvo.Spec, simulation.owners[0].Spec) {
		panic("CIO restoration failure prevented CVO restoration")
	}
	fmt.Println("operation and CIO restoration errors both reported; CVO restoration still attempted")

	simulation = newTemporaryUDPAdmissionSimulation()
	original := simulation.owners[1]
	original.Spec.Replicas = nil
	currentOwner := original.DeepCopy()
	temporaryUDPCheck(simulation.Client.Update(context.Background(), currentOwner), "")
	currentOwner.Status.Replicas, currentOwner.Status.UpdatedReplicas, currentOwner.Status.AvailableReplicas = 1, 1, 1
	temporaryUDPCheck(simulation.Client.Status().Update(context.Background(), currentOwner), "")
	temporaryUDPCheck(simulation.ensure(context.Background()), "")
	simulation.assertRestored()
	fmt.Println("original nil replica field restored without hard-coding one")

	for _, terminating := range []bool{false, true} {
		simulation = newTemporaryUDPAdmissionSimulation()
		simulation.before = func(action string, object client.Object) error {
			if action != "create-binding" {
				return nil
			}
			foreign := simulation.binding.DeepCopy()
			foreign.UID = "foreign-binding"
			foreign.ResourceVersion = ""
			foreign.Finalizers = []string{"foreign-finalizer"}
			temporaryUDPCheck(simulation.Client.Create(context.Background(), foreign), "")
			if terminating {
				temporaryUDPCheck(simulation.Client.Delete(context.Background(), foreign), "")
			}
			return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "validatingadmissionpolicybindings"}, foreign.Name)
		}
		temporaryUDPCheck(simulation.ensure(context.Background()), "refusing overwrite")
		foreign := &admissionv1.ValidatingAdmissionPolicyBinding{}
		temporaryUDPCheck(simulation.Client.Get(context.Background(), client.ObjectKey{Name: gwapiCRDVAPName}, foreign), "")
		if foreign.UID != "foreign-binding" || !reflect.DeepEqual(foreign.Finalizers, []string{"foreign-finalizer"}) || (terminating && foreign.DeletionTimestamp == nil) {
			panic("foreign or terminating binding was overwritten")
		}
		for _, owner := range simulation.owners {
			current := &appsv1.Deployment{}
			temporaryUDPCheck(simulation.Client.Get(context.Background(), client.ObjectKeyFromObject(owner), current), "")
			if !reflect.DeepEqual(current.Spec, owner.Spec) {
				panic("binding restoration error prevented owner restoration")
			}
		}
	}
	fmt.Println("foreign lifecycle metadata and terminating binding rejected without overwrite")

	// Output:
	// CVO-only, CIO-only, both: quiesce, create, restore, owned cleanup
	// pre-existing and AlreadyExists-race CRDs preserved without ownership
	// read, snapshot, pause, binding-delete, create, binding-restore errors reported
	// finite admission-cache lag recovers; permanent denial fails bounded
	// DELETE admission-cache lag recovers or fails bounded with owners restored
	// operation cancellation and Established timeout preserve independent restoration
	// replacement before cleanup and UID-precondition delete race preserved
	// cleanup errors surfaced; empty UID refused
	// nonterminating owner pods fail before bypass and restore replicas
	// operation and CIO restoration errors both reported; CVO restoration still attempted
	// original nil replica field restored without hard-coding one
	// foreign lifecycle metadata and terminating binding rejected without overwrite
}
