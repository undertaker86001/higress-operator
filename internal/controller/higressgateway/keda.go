package higressgateway

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/alibaba/higress/higress-operator/api/v1alpha1"
	. "github.com/alibaba/higress/higress-operator/internal/controller"
)

var kedaScaledObjectGVK = schema.GroupVersionKind{
	Group:   "keda.sh",
	Version: "v1alpha1",
	Kind:    "ScaledObject",
}

func buildScaledObject(instance *v1alpha1.HigressGateway) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(kedaScaledObjectGVK)
	obj.SetName(instance.Name)
	obj.SetNamespace(instance.Namespace)
	obj.SetLabels(instance.Labels)
	obj.SetAnnotations(instance.Annotations)
	updateScaledObjectSpec(obj, instance)
	return obj
}

func updateScaledObjectSpec(obj *unstructured.Unstructured, instance *v1alpha1.HigressGateway) {
	spec := map[string]any{
		"scaleTargetRef": map[string]any{
			"name":       instance.Name,
			"kind":       "Deployment",
			"apiVersion": "apps/v1",
		},
		"maxReplicaCount": int64(instance.Spec.AutoScaling.MaxReplicas),
		"triggers":        buildKEDATriggers(instance.Spec.AutoScaling.KEDA),
	}

	if instance.Spec.AutoScaling.MinReplicas != nil {
		spec["minReplicaCount"] = int64(*instance.Spec.AutoScaling.MinReplicas)
	}

	if cfg := instance.Spec.AutoScaling.KEDA; cfg != nil {
		if cfg.PollingIntervalSeconds != nil {
			spec["pollingInterval"] = int64(*cfg.PollingIntervalSeconds)
		}
		if cfg.CooldownPeriodSeconds != nil {
			spec["cooldownPeriod"] = int64(*cfg.CooldownPeriodSeconds)
		}
		if cfg.IdleReplicaCount != nil {
			spec["idleReplicaCount"] = int64(*cfg.IdleReplicaCount)
		}
	}

	obj.Object["spec"] = spec
}

func buildKEDATriggers(cfg *v1alpha1.KEDAScalingConfig) []any {
	if cfg == nil {
		return nil
	}

	triggers := make([]any, 0, len(cfg.Triggers))
	for _, trigger := range cfg.Triggers {
		item := map[string]any{
			"type":     trigger.Type,
			"metadata": buildTriggerMetadata(trigger.Metadata),
		}
		if trigger.AuthenticationRef != nil {
			item["authenticationRef"] = map[string]any{
				"name": *trigger.AuthenticationRef,
			}
		}
		triggers = append(triggers, item)
	}

	return triggers
}

func buildTriggerMetadata(metadata map[string]string) map[string]any {
	if len(metadata) == 0 {
		return nil
	}

	result := make(map[string]any, len(metadata))
	for key, value := range metadata {
		result[key] = value
	}

	return result
}

func muteScaledObject(obj *unstructured.Unstructured, instance *v1alpha1.HigressGateway) controllerutil.MutateFn {
	return func() error {
		obj.SetLabels(instance.Labels)
		obj.SetAnnotations(instance.Annotations)
		updateScaledObjectSpec(obj, instance)
		return nil
	}
}

func (r *HigressGatewayReconciler) reconcileKEDAScaledObject(
	ctx context.Context,
	instance *v1alpha1.HigressGateway,
	logger logr.Logger,
) error {
	obj := buildScaledObject(instance)
	if err := ctrl.SetControllerReference(instance, obj, r.Scheme); err != nil {
		return err
	}

	return CreateOrUpdate(ctx, r.Client, "ScaledObject", obj, muteScaledObject(obj, instance), logger)
}

func (r *HigressGatewayReconciler) deleteKEDAScaledObjectIfExists(
	ctx context.Context,
	instance *v1alpha1.HigressGateway,
	logger logr.Logger,
) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(kedaScaledObjectGVK)

	key := types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}
	if err := r.Get(ctx, key, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	logger.Info(fmt.Sprintf("delete ScaledObject for HigressGateway(%v)", instance.Name))
	if r.Recorder != nil {
		r.Recorder.Event(instance, "Normal", "KEDAScaledObjectDeleted", "Deleted KEDA ScaledObject")
	}

	return nil
}

func (r *HigressGatewayReconciler) isKEDACRDInstalled(ctx context.Context) (bool, error) {
	if r.RESTMapper == nil {
		return false, fmt.Errorf("RESTMapper is not configured")
	}

	_, err := r.RESTMapper.RESTMapping(kedaScaledObjectGVK.GroupKind(), kedaScaledObjectGVK.Version)
	if err != nil {
		if meta.IsNoMatchError(err) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}
