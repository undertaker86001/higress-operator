package higressgateway

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/alibaba/higress/higress-operator/api/v1alpha1"
)

func TestUpdateDeploymentSpecPreservesReplicasForAutoscaling(t *testing.T) {
	instance := newGatewayForTest()
	existingReplicas := int32(5)
	deploy := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Replicas: &existingReplicas,
		},
	}

	updateDeploymentSpec(deploy, instance, true)

	if deploy.Spec.Replicas == nil {
		t.Fatalf("expected replicas to be preserved")
	}
	if got := *deploy.Spec.Replicas; got != existingReplicas {
		t.Fatalf("expected replicas %d, got %d", existingReplicas, got)
	}
}

func TestUpdateDeploymentSpecUsesDesiredReplicasWhenAutoscalingDisabled(t *testing.T) {
	instance := newGatewayForTest()
	instance.Spec.AutoScaling = nil

	existingReplicas := int32(5)
	deploy := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Replicas: &existingReplicas,
		},
	}

	updateDeploymentSpec(deploy, instance, false)

	if deploy.Spec.Replicas == nil {
		t.Fatalf("expected desired replicas to be set")
	}
	if got := *deploy.Spec.Replicas; got != *instance.Spec.Replicas {
		t.Fatalf("expected replicas %d, got %d", *instance.Spec.Replicas, got)
	}
}

func TestUpdateDeploymentSpecSetsInitialReplicasOnCreate(t *testing.T) {
	instance := newGatewayForTest()
	deploy := &appsv1.Deployment{}

	updateDeploymentSpec(deploy, instance, true)

	if deploy.Spec.Replicas == nil {
		t.Fatalf("expected initial replicas to be set")
	}
	if got := *deploy.Spec.Replicas; got != *instance.Spec.Replicas {
		t.Fatalf("expected replicas %d, got %d", *instance.Spec.Replicas, got)
	}
}

func newGatewayForTest() *v1alpha1.HigressGateway {
	replicas := int32(2)
	minReplicas := int32(1)
	targetCPU := int32(80)

	return &v1alpha1.HigressGateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-gateway",
			Namespace:  "default",
			Generation: 3,
		},
		Spec: v1alpha1.HigressGatewaySpec{
			CRDCommonFields: v1alpha1.CRDCommonFields{
				Replicas: &replicas,
				SelectorLabels: map[string]string{
					"app": "test-gateway",
				},
				RBAC:           &v1alpha1.RBAC{Enable: true},
				ServiceAccount: &v1alpha1.ServiceAccount{Enable: true, Name: "test-gateway"},
				AutoScaling: &v1alpha1.AutoScaling{
					Enable:                         true,
					MinReplicas:                    &minReplicas,
					MaxReplicas:                    10,
					TargetCPUUtilizationPercentage: &targetCPU,
				},
				JwtPolicy: "third-party-jwt",
			},
			ContainerCommonFields: v1alpha1.ContainerCommonFields{
				Image: v1alpha1.Image{
					Repository: "example.com/higress/gateway",
					Tag:        "latest",
				},
				Resources: &apiv1.ResourceRequirements{
					Requests: apiv1.ResourceList{
						apiv1.ResourceCPU: resource.MustParse("500m"),
					},
				},
			},
			RollingMaxSurge:       intstr.FromString("100%"),
			RollingMaxUnavailable: intstr.FromString("25%"),
		},
	}
}
