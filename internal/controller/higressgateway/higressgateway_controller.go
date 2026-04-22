/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package higressgateway

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	apiv1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	operatorv1alpha1 "github.com/alibaba/higress/higress-operator/api/v1alpha1"
	. "github.com/alibaba/higress/higress-operator/internal/controller"
)

const (
	finalizer = "higressgateway.higress.io/finalizer"

	conditionTypeReady              = "Ready"
	conditionTypeConfigurationValid = "ConfigurationValid"
	conditionTypeAutoscalingReady   = "AutoscalingReady"
	conditionTypeDependencyReady    = "DependencyReady"

	reasonValidationSucceeded   = "ValidationSucceeded"
	reasonResourcesReady        = "ResourcesReady"
	reasonConfigurationInvalid  = "ConfigurationInvalid"
	reasonAutoscalingDisabled   = "AutoscalingDisabled"
	reasonAutoscalingReady      = "AutoscalingReady"
	reasonDependencyReady       = "DependencyReady"
	reasonDependencyNotRequired = "NoExternalDependency"
	reasonDependencyMissing     = "DependencyMissing"
)

// HigressGatewayReconciler reconciles a HigressGateway object
type HigressGatewayReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Recorder   record.EventRecorder
	RESTMapper meta.RESTMapper
}

type validationError struct {
	reason  string
	message string
}

func (e *validationError) Error() string {
	return e.message
}

//+kubebuilder:rbac:groups=operator.higress.io,resources=higressgateways,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=operator.higress.io,resources=higressgateways/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=operator.higress.io,resources=higressgateways/finalizers,verbs=update
//+kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=keda.sh,resources=scaledobjects,verbs=get;list;watch;create;update;patch;delete

//+kubebuilder:rbac:groups="",resources=pods;services;services/finalizers;endpoints;persistentvolumeclaims;events;configmaps;secrets;serviceaccounts;namespaces,verbs=create;update;get;list;watch;patch;delete

//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings;roles;rolebindings,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the HigressGateway object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.4/pkg/reconcile
func (r *HigressGatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	instance := &operatorv1alpha1.HigressGateway{}
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		if errors.IsNotFound(err) {
			logger.Info(fmt.Sprintf("HigressGateway(%v) resource not found", req.NamespacedName))
			return ctrl.Result{}, nil
		}

		logger.Error(err, "Failed to get resource HigressGateway(%v)", req.NamespacedName)
		return ctrl.Result{}, err
	}

	r.setDefaultValues(instance)

	// if deletionTimeStamp is not nil, it means is marked to be deleted
	if instance.GetDeletionTimestamp() != nil {
		if controllerutil.ContainsFinalizer(instance, finalizer) {
			if err := r.finalizeHigressGateway(instance, logger); err != nil {
				return ctrl.Result{}, err
			}

			controllerutil.RemoveFinalizer(instance, finalizer)

			if err := r.Update(ctx, instance); err != nil {
				return ctrl.Result{}, err
			}
		}

		return ctrl.Result{}, nil
	}

	// check if the namespace still exists during the reconciling
	ns, nn := &apiv1.Namespace{}, types.NamespacedName{Name: instance.Namespace, Namespace: apiv1.NamespaceAll}
	err := r.Get(ctx, nn, ns)
	if (err != nil && errors.IsNotFound(err)) || (ns.Status.Phase == apiv1.NamespaceTerminating) {
		logger.Info(fmt.Sprintf("The namespace (%s) doesn't exist or is in Terminating status, canceling Reconciling", instance.Namespace))
		return ctrl.Result{}, nil
	} else if err != nil {
		logger.Error(err, "Failed to check if namespace exists")
		return ctrl.Result{}, nil
	}

	// add finalizer for this CR
	if controllerutil.AddFinalizer(instance, finalizer) {
		if err = r.Update(ctx, instance); err != nil {
			return ctrl.Result{}, err
		}
		if err = r.Get(ctx, req.NamespacedName, instance); err != nil {
			return ctrl.Result{}, err
		}
		r.setDefaultValues(instance)
	}

	mode := getEffectiveScaleMode(instance)
	if validationErr := validateAutoscalingConfig(instance); validationErr != nil {
		logger.Error(validationErr, "Invalid HigressGateway autoscaling configuration", "name", req.NamespacedName)
		if err = r.deleteNativeHPAIfExists(ctx, instance, logger); err != nil {
			return ctrl.Result{}, err
		}
		if err = r.deleteKEDAScaledObjectIfExists(ctx, instance, logger); err != nil && !meta.IsNoMatchError(err) {
			return ctrl.Result{}, err
		}
		r.recordValidationFailure(instance, validationErr)
		if err = r.patchStatus(ctx, instance, func(status *operatorv1alpha1.HigressGatewayStatus) {
			markConfigurationInvalid(status, instance.Generation, mode, validationErr)
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if err = r.createServiceAccount(ctx, instance, logger); err != nil {
		return ctrl.Result{}, err
	}

	if err = r.createRBAC(ctx, instance, logger); err != nil {
		return ctrl.Result{}, err
	}

	if err = r.createConfigMap(ctx, instance, logger); err != nil {
		return ctrl.Result{}, err
	}

	if err = r.createDeployment(ctx, instance, logger); err != nil {
		return ctrl.Result{}, err
	}

	autoscalingConditionStatus := metav1.ConditionTrue
	autoscalingConditionReason := reasonAutoscalingDisabled
	autoscalingConditionMessage := "Autoscaling is disabled"
	dependencyConditionStatus := metav1.ConditionTrue
	dependencyConditionReason := reasonDependencyNotRequired
	dependencyConditionMessage := "No external autoscaling dependency is required"

	switch mode {
	case operatorv1alpha1.AutoScalingModeDisabled:
		if err = r.deleteNativeHPAIfExists(ctx, instance, logger); err != nil {
			return ctrl.Result{}, err
		}
		if err = r.deleteKEDAScaledObjectIfExists(ctx, instance, logger); err != nil && !meta.IsNoMatchError(err) {
			return ctrl.Result{}, err
		}
	case operatorv1alpha1.AutoScalingModeNativeHPA:
		if err = r.deleteKEDAScaledObjectIfExists(ctx, instance, logger); err != nil && !meta.IsNoMatchError(err) {
			return ctrl.Result{}, err
		}
		if err = r.reconcileNativeHPA(ctx, instance, logger); err != nil {
			return ctrl.Result{}, err
		}
		autoscalingConditionReason = reasonAutoscalingReady
		autoscalingConditionMessage = "Native HPA reconciled successfully"
	case operatorv1alpha1.AutoScalingModeKEDA:
		if err = r.deleteNativeHPAIfExists(ctx, instance, logger); err != nil {
			return ctrl.Result{}, err
		}
		installed, mappingErr := r.isKEDACRDInstalled(ctx)
		if mappingErr != nil {
			return ctrl.Result{}, mappingErr
		}
		if !installed {
			autoscalingConditionStatus = metav1.ConditionFalse
			autoscalingConditionReason = reasonDependencyMissing
			autoscalingConditionMessage = "KEDA ScaledObject cannot be reconciled because the KEDA CRD is not installed"
			dependencyConditionStatus = metav1.ConditionFalse
			dependencyConditionReason = reasonDependencyMissing
			dependencyConditionMessage = "KEDA CRD is not installed in the cluster"
		} else {
			if err = r.reconcileKEDAScaledObject(ctx, instance, logger); err != nil {
				return ctrl.Result{}, err
			}
			autoscalingConditionReason = reasonAutoscalingReady
			autoscalingConditionMessage = "KEDA ScaledObject reconciled successfully"
			dependencyConditionReason = reasonDependencyReady
			dependencyConditionMessage = "KEDA CRD is available"
		}
	}

	if err = r.createService(ctx, instance, logger); err != nil {
		return ctrl.Result{}, err
	}

	if err = r.patchStatus(ctx, instance, func(status *operatorv1alpha1.HigressGatewayStatus) {
		markResourcesReady(status, instance.Generation, mode)
		setAutoscalingCondition(
			status,
			instance.Generation,
			autoscalingConditionStatus,
			autoscalingConditionReason,
			autoscalingConditionMessage,
		)
		setDependencyCondition(
			status,
			instance.Generation,
			dependencyConditionStatus,
			dependencyConditionReason,
			dependencyConditionMessage,
		)
	}); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *HigressGatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&operatorv1alpha1.HigressGateway{}).
		Owns(&appsv1.Deployment{}).
		Owns(&autoscalingv2.HorizontalPodAutoscaler{}).
		Owns(&apiv1.Service{}).
		Owns(&apiv1.ConfigMap{}).
		Owns(&apiv1.ServiceAccount{}).
		Complete(r)
}

func (r *HigressGatewayReconciler) createServiceAccount(ctx context.Context, instance *operatorv1alpha1.HigressGateway, logger logr.Logger) error {
	sa := initServiceAccount(&apiv1.ServiceAccount{}, instance)
	if err := ctrl.SetControllerReference(instance, sa, r.Scheme); err != nil {
		return err
	}

	exists, err := CreateIfNotExits(ctx, r.Client, sa)
	if err != nil {
		return err
	}

	if !exists {
		logger.Info(fmt.Sprintf("create serviceAccount for HigressGateway(%v)", instance.Name))
	}

	return nil
}

func (r *HigressGatewayReconciler) createRBAC(ctx context.Context, instance *operatorv1alpha1.HigressGateway, logger logr.Logger) error {
	if !instance.Spec.RBAC.Enable || !instance.Spec.ServiceAccount.Enable {
		return nil
	}

	var (
		role = &rbacv1.Role{}
		rb   = &rbacv1.RoleBinding{}
		cr   = &rbacv1.ClusterRole{}
		crb  = &rbacv1.ClusterRoleBinding{}
		err  error
	)
	// reconcile clusterRole
	cr = initClusterRole(cr, instance)
	if err = CreateOrUpdate(ctx, r.Client, "clusterrole", cr, muteClusterRole(cr, instance), logger); err != nil {
		return err
	}

	// reconcile clusterRoleBinding
	initClusterRoleBinding(crb, instance)
	if err = CreateOrUpdate(ctx, r.Client, "clusterRoleBinding", crb,
		muteClusterRoleBinding(crb, instance), logger); err != nil {
		return err
	}

	initRole(role, instance)
	if err = CreateOrUpdate(ctx, r.Client, "role", role, muteRole(role, instance), logger); err != nil {
		return err
	}

	initRoleBinding(rb, instance)
	if err = CreateOrUpdate(ctx, r.Client, "roleBinding", rb,
		muteRoleBinding(rb, instance), logger); err != nil {
		return err
	}

	return nil
}

func (r *HigressGatewayReconciler) createDeployment(ctx context.Context, instance *operatorv1alpha1.HigressGateway, logger logr.Logger) error {
	deploy := initDeployment(&appsv1.Deployment{}, instance)
	if err := ctrl.SetControllerReference(instance, deploy, r.Scheme); err != nil {
		return err
	}

	return CreateOrUpdate(ctx, r.Client, "Deployment", deploy, muteDeployment(deploy, instance), logger)
}

func (r *HigressGatewayReconciler) createService(ctx context.Context, instance *operatorv1alpha1.HigressGateway, logger logr.Logger) error {
	svc := initService(&apiv1.Service{}, instance)
	if err := ctrl.SetControllerReference(instance, svc, r.Scheme); err != nil {
		return err
	}

	return CreateOrUpdate(ctx, r.Client, "Service", svc, muteService(svc, instance), logger)
}

func (r *HigressGatewayReconciler) finalizeHigressGateway(instance *operatorv1alpha1.HigressGateway, logger logr.Logger) error {
	var (
		ctx = context.TODO()
		crb = &rbacv1.ClusterRoleBinding{}
	)

	name := getServiceAccount(instance)
	nn := types.NamespacedName{Name: name, Namespace: apiv1.NamespaceAll}
	if err := r.Get(ctx, nn, crb); err != nil {
		return err
	}

	var subjects []rbacv1.Subject
	for _, subject := range crb.Subjects {
		if subject.Name != name || subject.Namespace != instance.Namespace {
			subjects = append(subjects, subject)
		}
	}
	crb.Subjects = subjects
	if err := r.Update(ctx, crb); err != nil {
		return err
	}

	return nil
}

func (r *HigressGatewayReconciler) createConfigMap(ctx context.Context, instance *operatorv1alpha1.HigressGateway, logger logr.Logger) error {
	gatewayConfigMap, err := initGatewayConfigMap(&apiv1.ConfigMap{}, instance)
	if err != nil {
		return err
	}
	if err = CreateOrUpdate(ctx, r.Client, "gatewayConfigMap", gatewayConfigMap,
		muteConfigMap(gatewayConfigMap, instance, updateGatewayConfigMapSpec), logger); err != nil {
		return err
	}

	if instance.Spec.Skywalking.Enable {
		skywalkingConfigMap, err := initSkywalkingConfigMap(&apiv1.ConfigMap{}, instance)
		if err != nil {
			return err
		}

		if err = ctrl.SetControllerReference(instance, skywalkingConfigMap, r.Scheme); err != nil {
			return err
		}

		if err = CreateOrUpdate(ctx, r.Client, "skywalkingConfigMap", skywalkingConfigMap,
			muteConfigMap(skywalkingConfigMap, instance, updateSkywalkingConfigMap), logger); err != nil {
			return err
		}
	}

	return nil
}

func (r *HigressGatewayReconciler) setDefaultValues(instance *operatorv1alpha1.HigressGateway) {
	if instance.Spec.RBAC == nil {
		instance.Spec.RBAC = &operatorv1alpha1.RBAC{Enable: true}
	}
	// serviceAccount
	if instance.Spec.ServiceAccount == nil {
		instance.Spec.ServiceAccount = &operatorv1alpha1.ServiceAccount{Enable: true, Name: "higress-gateway"}
	}
	// replicas
	if instance.Spec.Replicas == nil {
		replicas := int32(1)
		instance.Spec.Replicas = &replicas
	}
	// selectorLabels
	if len(instance.Spec.SelectorLabels) == 0 {
		instance.Spec.SelectorLabels = map[string]string{
			"app":     "higress-gateway",
			"higress": "higress-system-higress-gateway",
		}
	}
	// service
	if instance.Spec.Service == nil {
		instance.Spec.Service = &operatorv1alpha1.Service{
			Type: "LoadBalancer",
			Ports: []apiv1.ServicePort{
				{
					Name:       "http2",
					Port:       80,
					Protocol:   "TCP",
					TargetPort: intstr.FromInt(80),
				},
				{
					Name:       "https",
					Port:       443,
					Protocol:   "TCP",
					TargetPort: intstr.FromInt(443),
				},
			},
		}
	}
	// skywalking
	if instance.Spec.Skywalking == nil {
		instance.Spec.Skywalking = &operatorv1alpha1.Skywalking{Enable: false}
	}
}

func shouldPreserveReplicas(instance *operatorv1alpha1.HigressGateway) bool {
	return getEffectiveScaleMode(instance) != operatorv1alpha1.AutoScalingModeDisabled
}

func getEffectiveScaleMode(instance *operatorv1alpha1.HigressGateway) operatorv1alpha1.AutoScalingMode {
	if instance.Spec.AutoScaling == nil || !instance.Spec.AutoScaling.Enable {
		return operatorv1alpha1.AutoScalingModeDisabled
	}

	if instance.Spec.AutoScaling.Mode != "" {
		return instance.Spec.AutoScaling.Mode
	}
	if instance.Spec.AutoScaling.KEDA != nil {
		return operatorv1alpha1.AutoScalingModeKEDA
	}

	return operatorv1alpha1.AutoScalingModeNativeHPA
}

func validateAutoscalingConfig(instance *operatorv1alpha1.HigressGateway) *validationError {
	if getEffectiveScaleMode(instance) == operatorv1alpha1.AutoScalingModeDisabled {
		return nil
	}

	if instance.Spec.Local {
		return &validationError{
			reason:  "LocalModeUnsupported",
			message: "autoscaling cannot be enabled when spec.local=true",
		}
	}

	as := instance.Spec.AutoScaling
	if as.MinReplicas != nil && as.MaxReplicas < *as.MinReplicas {
		return &validationError{
			reason:  "InvalidReplicaBounds",
			message: "spec.autoScaling.maxReplicas must be greater than or equal to spec.autoScaling.minReplicas",
		}
	}

	switch getEffectiveScaleMode(instance) {
	case operatorv1alpha1.AutoScalingModeNativeHPA:
		if as.TargetCPUUtilizationPercentage == nil {
			return &validationError{
				reason:  "MissingCPUTarget",
				message: "spec.autoScaling.targetCPUUtilizationPercentage is required when autoscaling mode is NativeHPA",
			}
		}

		if instance.Spec.Resources == nil || instance.Spec.Resources.Requests == nil {
			return &validationError{
				reason:  "MissingCPURequest",
				message: "spec.resources.requests.cpu is required when autoscaling mode is NativeHPA",
			}
		}

		cpuRequest, ok := instance.Spec.Resources.Requests[apiv1.ResourceCPU]
		if !ok || cpuRequest.IsZero() {
			return &validationError{
				reason:  "MissingCPURequest",
				message: "spec.resources.requests.cpu is required when autoscaling mode is NativeHPA",
			}
		}
	case operatorv1alpha1.AutoScalingModeKEDA:
		if as.KEDA == nil {
			return &validationError{
				reason:  "MissingKEDAConfig",
				message: "spec.autoScaling.keda is required when autoscaling mode is KEDA",
			}
		}
		if len(as.KEDA.Triggers) == 0 {
			return &validationError{
				reason:  "MissingKEDATriggers",
				message: "spec.autoScaling.keda.triggers must contain at least one trigger when autoscaling mode is KEDA",
			}
		}
		for _, trigger := range as.KEDA.Triggers {
			if trigger.Type == "" {
				return &validationError{
					reason:  "InvalidKEDATrigger",
					message: "spec.autoScaling.keda.triggers.type must not be empty",
				}
			}
		}
	default:
		return &validationError{
			reason:  "UnsupportedAutoscalingMode",
			message: fmt.Sprintf("unsupported autoscaling mode %q", getEffectiveScaleMode(instance)),
		}
	}

	return nil
}

func markConfigurationInvalid(
	status *operatorv1alpha1.HigressGatewayStatus,
	generation int64,
	mode operatorv1alpha1.AutoScalingMode,
	validationErr *validationError,
) {
	status.Deployed = false
	status.ObservedGeneration = generation
	status.EffectiveScaleMode = mode
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionTypeConfigurationValid,
		Status:             metav1.ConditionFalse,
		Reason:             validationErr.reason,
		Message:            validationErr.message,
		ObservedGeneration: generation,
	})
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             reasonConfigurationInvalid,
		Message:            validationErr.message,
		ObservedGeneration: generation,
	})
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionTypeAutoscalingReady,
		Status:             metav1.ConditionFalse,
		Reason:             reasonConfigurationInvalid,
		Message:            validationErr.message,
		ObservedGeneration: generation,
	})
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionTypeDependencyReady,
		Status:             metav1.ConditionUnknown,
		Reason:             reasonConfigurationInvalid,
		Message:            validationErr.message,
		ObservedGeneration: generation,
	})
}

func markResourcesReady(
	status *operatorv1alpha1.HigressGatewayStatus,
	generation int64,
	mode operatorv1alpha1.AutoScalingMode,
) {
	status.Deployed = true
	status.ObservedGeneration = generation
	status.EffectiveScaleMode = mode
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionTypeConfigurationValid,
		Status:             metav1.ConditionTrue,
		Reason:             reasonValidationSucceeded,
		Message:            "HigressGateway configuration is valid",
		ObservedGeneration: generation,
	})
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             reasonResourcesReady,
		Message:            "HigressGateway resources reconciled successfully",
		ObservedGeneration: generation,
	})
}

func setAutoscalingCondition(
	status *operatorv1alpha1.HigressGatewayStatus,
	generation int64,
	conditionStatus metav1.ConditionStatus,
	reason, message string,
) {
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionTypeAutoscalingReady,
		Status:             conditionStatus,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
}

func setDependencyCondition(
	status *operatorv1alpha1.HigressGatewayStatus,
	generation int64,
	conditionStatus metav1.ConditionStatus,
	reason, message string,
) {
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionTypeDependencyReady,
		Status:             conditionStatus,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
}

func (r *HigressGatewayReconciler) patchStatus(
	ctx context.Context,
	instance *operatorv1alpha1.HigressGateway,
	mutate func(status *operatorv1alpha1.HigressGatewayStatus),
) error {
	base := instance.DeepCopy()
	mutate(&instance.Status)
	if equality.Semantic.DeepEqual(base.Status, instance.Status) {
		return nil
	}

	return r.Status().Patch(ctx, instance, client.MergeFrom(base))
}

func (r *HigressGatewayReconciler) recordValidationFailure(
	instance *operatorv1alpha1.HigressGateway,
	validationErr *validationError,
) {
	if r.Recorder == nil {
		return
	}

	r.Recorder.Event(instance, apiv1.EventTypeWarning, validationErr.reason, validationErr.message)
}
