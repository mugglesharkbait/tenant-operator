/*
Copyright 2026.

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

package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/your-github-username/tenant-operator/api/v1alpha1"
)

// tenantLabel marks every resource the operator provisions for a tenant.
const tenantLabel = "platform.example.io/tenant"

// TenantReconciler reconciles a Tenant object
type TenantReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.example.io,resources=tenants,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.example.io,resources=tenants/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.example.io,resources=tenants/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=resourcequotas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives the actual cluster state toward the desired state declared by a Tenant.
// It is level-triggered and idempotent: it re-derives the full desired state every run, so
// running it once or many times converges to the same result and it self-heals drift.
func (r *TenantReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the Tenant. If it is gone, its owner references garbage-collect everything else.
	var tenant platformv1alpha1.Tenant
	if err := r.Get(ctx, req.NamespacedName, &tenant); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// The tenant's namespace is named after the tenant itself.
	nsName := tenant.Name

	// 1. Namespace, owned by the Tenant so deleting the Tenant cascades to everything.
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, ns, func() error {
		if ns.Labels == nil {
			ns.Labels = map[string]string{}
		}
		ns.Labels[tenantLabel] = tenant.Name
		return controllerutil.SetControllerReference(&tenant, ns, r.Scheme)
	}); err != nil {
		return r.markFailed(ctx, &tenant, "NamespaceError", err)
	}

	// 2. ResourceQuota enforcing the requested cpu / memory / pods limits.
	hard, err := buildQuota(tenant.Spec.ResourceQuota)
	if err != nil {
		return r.markFailed(ctx, &tenant, "InvalidResourceQuota", err)
	}
	rq := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "tenant-quota", Namespace: nsName}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, rq, func() error {
		rq.Spec.Hard = hard
		return controllerutil.SetControllerReference(&tenant, rq, r.Scheme)
	}); err != nil {
		return r.markFailed(ctx, &tenant, "ResourceQuotaError", err)
	}

	// 3. NetworkPolicy: default-deny plus allow-DNS, only when isolation is requested.
	np := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "tenant-default-deny", Namespace: nsName}}
	if tenant.Spec.NetworkIsolation {
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, np, func() error {
			np.Spec = defaultDenyWithDNS()
			return controllerutil.SetControllerReference(&tenant, np, r.Scheme)
		}); err != nil {
			return r.markFailed(ctx, &tenant, "NetworkPolicyError", err)
		}
	} else {
		// Isolation disabled: remove the policy if a previous reconcile created it.
		if err := r.Delete(ctx, np); err != nil && !apierrors.IsNotFound(err) {
			return r.markFailed(ctx, &tenant, "NetworkPolicyError", err)
		}
	}

	// 4. RoleBinding granting the built-in "edit" ClusterRole to the owners, scoped to the namespace.
	rb := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "tenant-owners", Namespace: nsName}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, rb, func() error {
		rb.Subjects = ownerSubjects(tenant.Spec.Owners)
		rb.RoleRef = rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     "edit",
		}
		return controllerutil.SetControllerReference(&tenant, rb, r.Scheme)
	}); err != nil {
		return r.markFailed(ctx, &tenant, "RoleBindingError", err)
	}

	// 5. Status: report success.
	tenant.Status.Namespace = nsName
	tenant.Status.ObservedGeneration = tenant.Generation
	meta.SetStatusCondition(&tenant.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Provisioned",
		Message:            "Tenant namespace and policies are in place",
		ObservedGeneration: tenant.Generation,
	})
	if err := r.Status().Update(ctx, &tenant); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("reconciled tenant", "tenant", tenant.Name, "namespace", nsName)
	return ctrl.Result{}, nil
}

// markFailed records a Ready=False condition with the cause, then returns the error so the
// work item is requeued with backoff.
func (r *TenantReconciler) markFailed(ctx context.Context, tenant *platformv1alpha1.Tenant, reason string, cause error) (ctrl.Result, error) {
	meta.SetStatusCondition(&tenant.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            cause.Error(),
		ObservedGeneration: tenant.Generation,
	})
	// Best-effort status write; surface the original error regardless.
	_ = r.Status().Update(ctx, tenant)
	return ctrl.Result{}, cause
}

// buildQuota converts the Tenant's requested limits into a ResourceQuota hard list.
func buildQuota(q platformv1alpha1.TenantResourceQuota) (corev1.ResourceList, error) {
	hard := corev1.ResourceList{}
	if q.CPU != "" {
		cpu, err := resource.ParseQuantity(q.CPU)
		if err != nil {
			return nil, fmt.Errorf("invalid cpu quota %q: %w", q.CPU, err)
		}
		hard[corev1.ResourceCPU] = cpu
	}
	if q.Memory != "" {
		mem, err := resource.ParseQuantity(q.Memory)
		if err != nil {
			return nil, fmt.Errorf("invalid memory quota %q: %w", q.Memory, err)
		}
		hard[corev1.ResourceMemory] = mem
	}
	if q.Pods > 0 {
		hard[corev1.ResourcePods] = *resource.NewQuantity(int64(q.Pods), resource.DecimalSI)
	}
	return hard, nil
}

// defaultDenyWithDNS denies all ingress and all egress except DNS (UDP/TCP 53), so pods in
// the namespace are isolated by default but can still resolve names.
func defaultDenyWithDNS() networkingv1.NetworkPolicySpec {
	tcp := corev1.ProtocolTCP
	udp := corev1.ProtocolUDP
	dnsPort := intstr.FromInt32(53)
	return networkingv1.NetworkPolicySpec{
		// Empty selector = applies to every pod in the namespace.
		PodSelector: metav1.LabelSelector{},
		PolicyTypes: []networkingv1.PolicyType{
			networkingv1.PolicyTypeIngress,
			networkingv1.PolicyTypeEgress,
		},
		// No ingress rules = deny all inbound traffic.
		Ingress: []networkingv1.NetworkPolicyIngressRule{},
		// Only egress allowed is DNS.
		Egress: []networkingv1.NetworkPolicyEgressRule{
			{
				Ports: []networkingv1.NetworkPolicyPort{
					{Protocol: &udp, Port: &dnsPort},
					{Protocol: &tcp, Port: &dnsPort},
				},
			},
		},
	}
}

// ownerSubjects turns "user:" / "group:" prefixed owner strings into RBAC subjects.
// An unprefixed value is treated as a User.
func ownerSubjects(owners []string) []rbacv1.Subject {
	subjects := make([]rbacv1.Subject, 0, len(owners))
	for _, owner := range owners {
		subject := rbacv1.Subject{APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: owner}
		switch {
		case strings.HasPrefix(owner, "user:"):
			subject.Kind = rbacv1.UserKind
			subject.Name = strings.TrimPrefix(owner, "user:")
		case strings.HasPrefix(owner, "group:"):
			subject.Kind = rbacv1.GroupKind
			subject.Name = strings.TrimPrefix(owner, "group:")
		}
		subjects = append(subjects, subject)
	}
	return subjects
}

// SetupWithManager sets up the controller with the Manager. Owns(...) makes the controller
// re-reconcile when a managed resource changes, giving the self-healing behaviour.
func (r *TenantReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.Tenant{}).
		Owns(&corev1.Namespace{}).
		Owns(&corev1.ResourceQuota{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Owns(&rbacv1.RoleBinding{}).
		Named("tenant").
		Complete(r)
}
