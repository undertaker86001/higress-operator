package higressgateway

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/alibaba/higress/higress-operator/api/v1alpha1"
)

func TestBuildScaledObject(t *testing.T) {
	instance := newKEDAGatewayForTest()

	obj := buildScaledObject(instance)

	if obj.GetAPIVersion() != "keda.sh/v1alpha1" || obj.GetKind() != "ScaledObject" {
		t.Fatalf("unexpected GVK %s %s", obj.GetAPIVersion(), obj.GetKind())
	}
	if obj.GetName() != instance.Name || obj.GetNamespace() != instance.Namespace {
		t.Fatalf("unexpected object metadata: %s/%s", obj.GetNamespace(), obj.GetName())
	}

	maxReplicaCount, found, err := unstructured.NestedInt64(obj.Object, "spec", "maxReplicaCount")
	if err != nil || !found || maxReplicaCount != int64(instance.Spec.AutoScaling.MaxReplicas) {
		t.Fatalf("expected maxReplicaCount %d, got value=%d found=%v err=%v", instance.Spec.AutoScaling.MaxReplicas, maxReplicaCount, found, err)
	}

	triggers, found, err := unstructured.NestedSlice(obj.Object, "spec", "triggers")
	if err != nil || !found || len(triggers) != 1 {
		t.Fatalf("expected one trigger, got found=%v len=%d err=%v", found, len(triggers), err)
	}
}

func TestIsKEDACRDInstalled(t *testing.T) {
	reconciler := &HigressGatewayReconciler{
		RESTMapper: newKEDARESTMapper(true),
	}

	installed, err := reconciler.isKEDACRDInstalled(context.Background())
	if err != nil {
		t.Fatalf("isKEDACRDInstalled returned error: %v", err)
	}
	if !installed {
		t.Fatalf("expected KEDA CRD to be reported as installed")
	}

	reconciler.RESTMapper = newKEDARESTMapper(false)
	installed, err = reconciler.isKEDACRDInstalled(context.Background())
	if err != nil {
		t.Fatalf("isKEDACRDInstalled returned error: %v", err)
	}
	if installed {
		t.Fatalf("expected KEDA CRD to be reported as missing")
	}
}

func TestReconcileKEDAScaledObjectCreatesAndUpdates(t *testing.T) {
	scheme := newGatewayKEDATestScheme(t)
	instance := newKEDAGatewayForTest()

	reconciler := &HigressGatewayReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		Scheme: scheme,
	}

	if err := reconciler.reconcileKEDAScaledObject(context.Background(), instance, logr.Discard()); err != nil {
		t.Fatalf("reconcileKEDAScaledObject returned error: %v", err)
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(kedaScaledObjectGVK)
	key := types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}
	if err := reconciler.Get(context.Background(), key, obj); err != nil {
		t.Fatalf("expected ScaledObject to be created: %v", err)
	}

	if len(obj.GetOwnerReferences()) != 1 || obj.GetOwnerReferences()[0].Name != instance.Name {
		t.Fatalf("expected owner reference to be preserved, got %#v", obj.GetOwnerReferences())
	}

	instance.Spec.AutoScaling.MaxReplicas = 7
	instance.Spec.AutoScaling.KEDA.Triggers[0].Metadata["threshold"] = "20"
	if err := reconciler.reconcileKEDAScaledObject(context.Background(), instance, logr.Discard()); err != nil {
		t.Fatalf("reconcileKEDAScaledObject update returned error: %v", err)
	}

	if err := reconciler.Get(context.Background(), key, obj); err != nil {
		t.Fatalf("expected ScaledObject to exist after update: %v", err)
	}
	maxReplicaCount, found, err := unstructured.NestedInt64(obj.Object, "spec", "maxReplicaCount")
	if err != nil || !found || maxReplicaCount != 7 {
		t.Fatalf("expected updated maxReplicaCount 7, got value=%d found=%v err=%v", maxReplicaCount, found, err)
	}
}

func TestDeleteKEDAScaledObjectIfExists(t *testing.T) {
	scheme := newGatewayKEDATestScheme(t)
	instance := newKEDAGatewayForTest()
	existing := buildScaledObject(instance)
	controller := true
	blockOwnerDeletion := true
	existing.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion:         "operator.higress.io/v1alpha1",
		Kind:               "HigressGateway",
		Name:               instance.Name,
		UID:                instance.UID,
		Controller:         &controller,
		BlockOwnerDeletion: &blockOwnerDeletion,
	}})

	reconciler := &HigressGatewayReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build(),
		Scheme: scheme,
	}

	if err := reconciler.deleteKEDAScaledObjectIfExists(context.Background(), instance, logr.Discard()); err != nil {
		t.Fatalf("deleteKEDAScaledObjectIfExists returned error: %v", err)
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(kedaScaledObjectGVK)
	key := types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}
	if err := reconciler.Get(context.Background(), key, obj); err == nil {
		t.Fatalf("expected ScaledObject to be deleted")
	}
}

func newGatewayKEDATestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add operator scheme: %v", err)
	}
	return scheme
}

func newKEDARESTMapper(installed bool) meta.RESTMapper {
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{kedaScaledObjectGVK.GroupVersion()})
	if installed {
		mapper.AddSpecific(
			kedaScaledObjectGVK,
			schema.GroupVersionResource{Group: kedaScaledObjectGVK.Group, Version: kedaScaledObjectGVK.Version, Resource: "scaledobjects"},
			schema.GroupVersionResource{Group: kedaScaledObjectGVK.Group, Version: kedaScaledObjectGVK.Version, Resource: "scaledobject"},
			meta.RESTScopeNamespace,
		)
	}
	return mapper
}

func newKEDAGatewayForTest() *v1alpha1.HigressGateway {
	instance := newGatewayForTest()
	instance.Spec.AutoScaling.Mode = v1alpha1.AutoScalingModeKEDA
	instance.Spec.AutoScaling.TargetCPUUtilizationPercentage = nil

	polling := int32(30)
	cooldown := int32(300)
	instance.Spec.AutoScaling.KEDA = &v1alpha1.KEDAScalingConfig{
		PollingIntervalSeconds: &polling,
		CooldownPeriodSeconds:  &cooldown,
		Triggers: []v1alpha1.KEDAScaleTrigger{
			{
				Type: "prometheus",
				Metadata: map[string]string{
					"serverAddress": "http://prometheus.monitoring.svc:9090",
					"metricName":    "gateway_qps",
					"query":         "sum(rate(http_requests_total[1m]))",
					"threshold":     "10",
				},
			},
		},
	}

	return instance
}
