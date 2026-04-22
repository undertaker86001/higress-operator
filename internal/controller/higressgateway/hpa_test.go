package higressgateway

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/alibaba/higress/higress-operator/api/v1alpha1"
)

func TestReconcileNativeHPACreatesHPA(t *testing.T) {
	scheme := newGatewayHPATestScheme(t)
	instance := newGatewayForTest()

	reconciler := &HigressGatewayReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		Scheme: scheme,
	}

	if err := reconciler.reconcileNativeHPA(context.Background(), instance, logr.Discard()); err != nil {
		t.Fatalf("reconcileNativeHPA returned error: %v", err)
	}

	hpa := &autoscalingv2.HorizontalPodAutoscaler{}
	key := types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}
	if err := reconciler.Get(context.Background(), key, hpa); err != nil {
		t.Fatalf("expected HPA to be created: %v", err)
	}

	if hpa.Spec.MaxReplicas != instance.Spec.AutoScaling.MaxReplicas {
		t.Fatalf("expected maxReplicas %d, got %d", instance.Spec.AutoScaling.MaxReplicas, hpa.Spec.MaxReplicas)
	}
	if hpa.Spec.MinReplicas == nil || *hpa.Spec.MinReplicas != *instance.Spec.AutoScaling.MinReplicas {
		t.Fatalf("expected minReplicas %d, got %#v", *instance.Spec.AutoScaling.MinReplicas, hpa.Spec.MinReplicas)
	}
	if hpa.Spec.ScaleTargetRef.Kind != "Deployment" || hpa.Spec.ScaleTargetRef.Name != instance.Name {
		t.Fatalf("unexpected scale target: %#v", hpa.Spec.ScaleTargetRef)
	}
	if len(hpa.OwnerReferences) != 1 || hpa.OwnerReferences[0].Name != instance.Name {
		t.Fatalf("expected controller owner reference, got %#v", hpa.OwnerReferences)
	}
}

func TestReconcileNativeHPAUpdatesExistingDrift(t *testing.T) {
	scheme := newGatewayHPATestScheme(t)
	instance := newGatewayForTest()

	existing := initHPAv2(&autoscalingv2.HorizontalPodAutoscaler{}, instance)
	existing.Spec.MaxReplicas = 2
	existing.Spec.Metrics[0].Resource.Target.AverageUtilization = int32Ptr(50)

	reconciler := &HigressGatewayReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build(),
		Scheme: scheme,
	}

	if err := reconciler.reconcileNativeHPA(context.Background(), instance, logr.Discard()); err != nil {
		t.Fatalf("reconcileNativeHPA returned error: %v", err)
	}

	current := &autoscalingv2.HorizontalPodAutoscaler{}
	key := types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}
	if err := reconciler.Get(context.Background(), key, current); err != nil {
		t.Fatalf("expected HPA to exist after reconcile: %v", err)
	}

	if current.Spec.MaxReplicas != instance.Spec.AutoScaling.MaxReplicas {
		t.Fatalf("expected maxReplicas %d, got %d", instance.Spec.AutoScaling.MaxReplicas, current.Spec.MaxReplicas)
	}
	if got := current.Spec.Metrics[0].Resource.Target.AverageUtilization; got == nil || *got != *instance.Spec.AutoScaling.TargetCPUUtilizationPercentage {
		t.Fatalf("expected CPU target %d, got %#v", *instance.Spec.AutoScaling.TargetCPUUtilizationPercentage, got)
	}
}

func TestDeleteNativeHPAIfExists(t *testing.T) {
	scheme := newGatewayHPATestScheme(t)
	instance := newGatewayForTest()
	existing := initHPAv2(&autoscalingv2.HorizontalPodAutoscaler{}, instance)

	reconciler := &HigressGatewayReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build(),
		Scheme: scheme,
	}

	if err := reconciler.deleteNativeHPAIfExists(context.Background(), instance, logr.Discard()); err != nil {
		t.Fatalf("deleteNativeHPAIfExists returned error: %v", err)
	}

	hpa := &autoscalingv2.HorizontalPodAutoscaler{}
	key := types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}
	if err := reconciler.Get(context.Background(), key, hpa); !apierrors.IsNotFound(err) {
		t.Fatalf("expected HPA to be deleted, got err=%v", err)
	}

	if err := reconciler.deleteNativeHPAIfExists(context.Background(), instance, logr.Discard()); err != nil {
		t.Fatalf("expected deleting missing HPA to be a no-op, got %v", err)
	}
}

func newGatewayHPATestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add operator scheme: %v", err)
	}
	if err := autoscalingv2.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add autoscaling/v2 scheme: %v", err)
	}

	return scheme
}

func int32Ptr(v int32) *int32 {
	return &v
}
