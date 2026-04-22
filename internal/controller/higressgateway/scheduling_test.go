package higressgateway

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"

	"github.com/alibaba/higress/higress-operator/api/v1alpha1"
)

func TestUpdateDeploymentSpecAppliesSchedulingPolicyToPodSpec(t *testing.T) {
	instance := newGatewayForTest()
	scheduling := schedulingPolicyForTest()
	instance.Spec.Scheduling = &scheduling

	deploy := initDeployment(&appsv1.Deployment{}, instance)

	if got := deploy.Spec.Template.Spec.SchedulerName; got != instance.Spec.Scheduling.SchedulerName {
		t.Fatalf("expected schedulerName %q, got %q", instance.Spec.Scheduling.SchedulerName, got)
	}
	if got := deploy.Spec.Template.Spec.PriorityClassName; got != instance.Spec.Scheduling.PriorityClassName {
		t.Fatalf("expected priorityClassName %q, got %q", instance.Spec.Scheduling.PriorityClassName, got)
	}
}

func TestUpdateDeploymentSpecAppliesSchedulingMetadataToPodTemplate(t *testing.T) {
	instance := newGatewayForTest()
	scheduling := schedulingPolicyForTest()
	instance.Spec.Scheduling = &scheduling

	deploy := initDeployment(&appsv1.Deployment{}, instance)

	if got := deploy.Spec.Template.Labels["workload-class"]; got != "online-gateway" {
		t.Fatalf("expected scheduling label to be propagated, got %q", got)
	}
	if got := deploy.Spec.Template.Annotations["volcano.sh/queue-name"]; got != "gateway-online" {
		t.Fatalf("expected scheduling annotation to be propagated, got %q", got)
	}
	if got := deploy.Spec.Template.Labels["app"]; got != instance.Spec.SelectorLabels["app"] {
		t.Fatalf("expected selector label to be preserved, got %q", got)
	}
}

func TestUpdateDeploymentSpecKeepsSelectorLabelsWhenSchedulingLabelsConflict(t *testing.T) {
	instance := newGatewayForTest()
	scheduling := schedulingPolicyForTest()
	instance.Spec.Scheduling = &scheduling
	instance.Spec.Scheduling.Labels["app"] = "should-not-override-selector"

	deploy := initDeployment(&appsv1.Deployment{}, instance)

	if got := deploy.Spec.Template.Labels["app"]; got != instance.Spec.SelectorLabels["app"] {
		t.Fatalf("expected selector label %q to win, got %q", instance.Spec.SelectorLabels["app"], got)
	}
}

func schedulingPolicyForTest() v1alpha1.SchedulingPolicy {
	return v1alpha1.SchedulingPolicy{
		SchedulerName:     "volcano",
		PriorityClassName: "higress-online-high",
		Labels: map[string]string{
			"workload-class": "online-gateway",
		},
		Annotations: map[string]string{
			"volcano.sh/queue-name": "gateway-online",
		},
	}
}
