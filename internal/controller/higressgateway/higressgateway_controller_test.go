package higressgateway

import (
	"testing"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/alibaba/higress/higress-operator/api/v1alpha1"
)

func TestGetEffectiveScaleMode(t *testing.T) {
	instance := newGatewayForTest()

	if mode := getEffectiveScaleMode(instance); mode != v1alpha1.AutoScalingModeNativeHPA {
		t.Fatalf("expected mode %q, got %q", v1alpha1.AutoScalingModeNativeHPA, mode)
	}

	instance.Spec.AutoScaling = nil
	if mode := getEffectiveScaleMode(instance); mode != v1alpha1.AutoScalingModeDisabled {
		t.Fatalf("expected mode %q, got %q", v1alpha1.AutoScalingModeDisabled, mode)
	}

	instance = newGatewayForTest()
	instance.Spec.AutoScaling.Mode = v1alpha1.AutoScalingModeKEDA
	instance.Spec.AutoScaling.KEDA = &v1alpha1.KEDAScalingConfig{
		Triggers: []v1alpha1.KEDAScaleTrigger{{Type: "prometheus"}},
	}
	if mode := getEffectiveScaleMode(instance); mode != v1alpha1.AutoScalingModeKEDA {
		t.Fatalf("expected mode %q, got %q", v1alpha1.AutoScalingModeKEDA, mode)
	}

	instance = newGatewayForTest()
	instance.Spec.AutoScaling.KEDA = &v1alpha1.KEDAScalingConfig{
		Triggers: []v1alpha1.KEDAScaleTrigger{{Type: "prometheus"}},
	}
	if mode := getEffectiveScaleMode(instance); mode != v1alpha1.AutoScalingModeKEDA {
		t.Fatalf("expected inferred mode %q, got %q", v1alpha1.AutoScalingModeKEDA, mode)
	}
}

func TestValidateAutoscalingConfig(t *testing.T) {
	testCases := []struct {
		name           string
		mutate         func(*v1alpha1.HigressGateway)
		expectedReason string
	}{
		{
			name: "disabled autoscaling is valid",
			mutate: func(instance *v1alpha1.HigressGateway) {
				instance.Spec.AutoScaling = nil
			},
		},
		{
			name: "local mode blocks autoscaling",
			mutate: func(instance *v1alpha1.HigressGateway) {
				instance.Spec.Local = true
			},
			expectedReason: "LocalModeUnsupported",
		},
		{
			name: "max replicas must be at least min replicas",
			mutate: func(instance *v1alpha1.HigressGateway) {
				minReplicas := int32(3)
				instance.Spec.AutoScaling.MinReplicas = &minReplicas
				instance.Spec.AutoScaling.MaxReplicas = 2
			},
			expectedReason: "InvalidReplicaBounds",
		},
		{
			name: "cpu target is required",
			mutate: func(instance *v1alpha1.HigressGateway) {
				instance.Spec.AutoScaling.TargetCPUUtilizationPercentage = nil
			},
			expectedReason: "MissingCPUTarget",
		},
		{
			name: "cpu request is required",
			mutate: func(instance *v1alpha1.HigressGateway) {
				instance.Spec.Resources = nil
			},
			expectedReason: "MissingCPURequest",
		},
		{
			name: "cpu request entry is required",
			mutate: func(instance *v1alpha1.HigressGateway) {
				instance.Spec.Resources.Requests = apiv1.ResourceList{}
			},
			expectedReason: "MissingCPURequest",
		},
		{
			name: "keda mode requires keda config",
			mutate: func(instance *v1alpha1.HigressGateway) {
				instance.Spec.AutoScaling.Mode = v1alpha1.AutoScalingModeKEDA
				instance.Spec.AutoScaling.TargetCPUUtilizationPercentage = nil
				instance.Spec.AutoScaling.KEDA = nil
			},
			expectedReason: "MissingKEDAConfig",
		},
		{
			name: "keda mode requires triggers",
			mutate: func(instance *v1alpha1.HigressGateway) {
				instance.Spec.AutoScaling.Mode = v1alpha1.AutoScalingModeKEDA
				instance.Spec.AutoScaling.TargetCPUUtilizationPercentage = nil
				instance.Spec.AutoScaling.KEDA = &v1alpha1.KEDAScalingConfig{}
			},
			expectedReason: "MissingKEDATriggers",
		},
		{
			name: "keda trigger type must not be empty",
			mutate: func(instance *v1alpha1.HigressGateway) {
				instance.Spec.AutoScaling.Mode = v1alpha1.AutoScalingModeKEDA
				instance.Spec.AutoScaling.TargetCPUUtilizationPercentage = nil
				instance.Spec.AutoScaling.KEDA = &v1alpha1.KEDAScalingConfig{
					Triggers: []v1alpha1.KEDAScaleTrigger{{}},
				}
			},
			expectedReason: "InvalidKEDATrigger",
		},
		{
			name:   "valid autoscaling config passes",
			mutate: func(instance *v1alpha1.HigressGateway) {},
		},
		{
			name: "valid keda config passes",
			mutate: func(instance *v1alpha1.HigressGateway) {
				instance.Spec.AutoScaling.Mode = v1alpha1.AutoScalingModeKEDA
				instance.Spec.AutoScaling.TargetCPUUtilizationPercentage = nil
				instance.Spec.AutoScaling.KEDA = &v1alpha1.KEDAScalingConfig{
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
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			instance := newGatewayForTest()
			tc.mutate(instance)

			err := validateAutoscalingConfig(instance)
			if tc.expectedReason == "" {
				if err != nil {
					t.Fatalf("expected validation to pass, got error: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("expected validation error with reason %q", tc.expectedReason)
			}
			if err.reason != tc.expectedReason {
				t.Fatalf("expected reason %q, got %q", tc.expectedReason, err.reason)
			}
		})
	}
}

func TestMarkConfigurationInvalid(t *testing.T) {
	status := &v1alpha1.HigressGatewayStatus{}
	validationErr := &validationError{
		reason:  "MissingCPUTarget",
		message: "spec.autoScaling.targetCPUUtilizationPercentage is required when autoscaling is enabled",
	}

	markConfigurationInvalid(status, 7, v1alpha1.AutoScalingModeNativeHPA, validationErr)

	if status.Deployed {
		t.Fatalf("expected deployed to be false")
	}
	if status.ObservedGeneration != 7 {
		t.Fatalf("expected observed generation 7, got %d", status.ObservedGeneration)
	}
	if status.EffectiveScaleMode != v1alpha1.AutoScalingModeNativeHPA {
		t.Fatalf("expected effective scale mode %q, got %q", v1alpha1.AutoScalingModeNativeHPA, status.EffectiveScaleMode)
	}

	configCondition := findCondition(status.Conditions, conditionTypeConfigurationValid)
	if configCondition == nil || configCondition.Status != metav1.ConditionFalse || configCondition.Reason != validationErr.reason {
		t.Fatalf("expected invalid configuration condition, got %#v", configCondition)
	}

	readyCondition := findCondition(status.Conditions, conditionTypeReady)
	if readyCondition == nil || readyCondition.Status != metav1.ConditionFalse || readyCondition.Reason != reasonConfigurationInvalid {
		t.Fatalf("expected ready=false condition, got %#v", readyCondition)
	}

	autoscalingCondition := findCondition(status.Conditions, conditionTypeAutoscalingReady)
	if autoscalingCondition == nil || autoscalingCondition.Status != metav1.ConditionFalse || autoscalingCondition.Reason != reasonConfigurationInvalid {
		t.Fatalf("expected autoscaling=false condition, got %#v", autoscalingCondition)
	}

	dependencyCondition := findCondition(status.Conditions, conditionTypeDependencyReady)
	if dependencyCondition == nil || dependencyCondition.Status != metav1.ConditionUnknown || dependencyCondition.Reason != reasonConfigurationInvalid {
		t.Fatalf("expected dependency unknown condition, got %#v", dependencyCondition)
	}
}

func TestMarkResourcesReady(t *testing.T) {
	status := &v1alpha1.HigressGatewayStatus{}

	markResourcesReady(status, 9, v1alpha1.AutoScalingModeDisabled)
	setAutoscalingCondition(status, 9, metav1.ConditionTrue, reasonAutoscalingDisabled, "Autoscaling is disabled")
	setDependencyCondition(status, 9, metav1.ConditionTrue, reasonDependencyNotRequired, "No external autoscaling dependency is required")

	if !status.Deployed {
		t.Fatalf("expected deployed to be true")
	}
	if status.ObservedGeneration != 9 {
		t.Fatalf("expected observed generation 9, got %d", status.ObservedGeneration)
	}
	if status.EffectiveScaleMode != v1alpha1.AutoScalingModeDisabled {
		t.Fatalf("expected effective scale mode %q, got %q", v1alpha1.AutoScalingModeDisabled, status.EffectiveScaleMode)
	}

	configCondition := findCondition(status.Conditions, conditionTypeConfigurationValid)
	if configCondition == nil || configCondition.Status != metav1.ConditionTrue || configCondition.Reason != reasonValidationSucceeded {
		t.Fatalf("expected valid configuration condition, got %#v", configCondition)
	}

	readyCondition := findCondition(status.Conditions, conditionTypeReady)
	if readyCondition == nil || readyCondition.Status != metav1.ConditionTrue || readyCondition.Reason != reasonResourcesReady {
		t.Fatalf("expected ready=true condition, got %#v", readyCondition)
	}

	autoscalingCondition := findCondition(status.Conditions, conditionTypeAutoscalingReady)
	if autoscalingCondition == nil || autoscalingCondition.Status != metav1.ConditionTrue || autoscalingCondition.Reason != reasonAutoscalingDisabled {
		t.Fatalf("expected autoscaling disabled condition, got %#v", autoscalingCondition)
	}

	dependencyCondition := findCondition(status.Conditions, conditionTypeDependencyReady)
	if dependencyCondition == nil || dependencyCondition.Status != metav1.ConditionTrue || dependencyCondition.Reason != reasonDependencyNotRequired {
		t.Fatalf("expected dependency not required condition, got %#v", dependencyCondition)
	}
}

func TestMarkResourcesReadyForNativeHPA(t *testing.T) {
	status := &v1alpha1.HigressGatewayStatus{}

	markResourcesReady(status, 11, v1alpha1.AutoScalingModeNativeHPA)
	setAutoscalingCondition(status, 11, metav1.ConditionTrue, reasonAutoscalingReady, "Native HPA reconciled successfully")

	autoscalingCondition := findCondition(status.Conditions, conditionTypeAutoscalingReady)
	if autoscalingCondition == nil || autoscalingCondition.Status != metav1.ConditionTrue || autoscalingCondition.Reason != reasonAutoscalingReady {
		t.Fatalf("expected autoscaling ready condition, got %#v", autoscalingCondition)
	}
}

func TestSetDependencyCondition(t *testing.T) {
	status := &v1alpha1.HigressGatewayStatus{}

	setDependencyCondition(status, 13, metav1.ConditionFalse, reasonDependencyMissing, "KEDA CRD is not installed in the cluster")

	dependencyCondition := findCondition(status.Conditions, conditionTypeDependencyReady)
	if dependencyCondition == nil || dependencyCondition.Status != metav1.ConditionFalse || dependencyCondition.Reason != reasonDependencyMissing {
		t.Fatalf("expected dependency missing condition, got %#v", dependencyCondition)
	}
}

func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}

	return nil
}
