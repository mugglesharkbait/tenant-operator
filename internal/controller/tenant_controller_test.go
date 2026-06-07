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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	platformv1alpha1 "github.com/your-github-username/tenant-operator/api/v1alpha1"
)

var _ = Describe("Tenant Controller", func() {
	Context("When reconciling a resource", func() {
		// Tenant is cluster-scoped, so the namespaced name carries no namespace.
		// The provisioned namespace is named after the Tenant.
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{Name: resourceName}
		tenant := &platformv1alpha1.Tenant{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind Tenant")
			err := k8sClient.Get(ctx, typeNamespacedName, tenant)
			if err != nil && errors.IsNotFound(err) {
				resource := &platformv1alpha1.Tenant{
					ObjectMeta: metav1.ObjectMeta{Name: resourceName},
					Spec: platformv1alpha1.TenantSpec{
						DisplayName: "Test Tenant",
						Owners:      []string{"user:alice@example.io", "group:test-team"},
						ResourceQuota: platformv1alpha1.TenantResourceQuota{
							CPU:    "4",
							Memory: "8Gi",
							Pods:   20,
						},
						NetworkIsolation: true,
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &platformv1alpha1.Tenant{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance Tenant")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			// envtest has no garbage collector, so remove the provisioned namespace directly.
			ns := &corev1.Namespace{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: resourceName}, ns); err == nil {
				Expect(k8sClient.Delete(ctx, ns)).To(Succeed())
			}
		})

		It("should provision the namespace, quota, network policy, role binding, and Ready status", func() {
			By("Reconciling the created resource")
			controllerReconciler := &TenantReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("creating the tenant namespace with the tenant label")
			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: resourceName}, ns)).To(Succeed())
			Expect(ns.Labels).To(HaveKeyWithValue("platform.example.io/tenant", resourceName))

			By("creating the resource quota")
			rq := &corev1.ResourceQuota{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "tenant-quota", Namespace: resourceName}, rq)).To(Succeed())
			Expect(rq.Spec.Hard).To(HaveKey(corev1.ResourcePods))

			By("creating the default-deny network policy (networkIsolation is true)")
			np := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "tenant-default-deny", Namespace: resourceName}, np)).To(Succeed())

			By("creating the owners role binding bound to the edit ClusterRole")
			rb := &rbacv1.RoleBinding{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "tenant-owners", Namespace: resourceName}, rb)).To(Succeed())
			Expect(rb.RoleRef.Name).To(Equal("edit"))
			Expect(rb.Subjects).To(HaveLen(2))

			By("setting the Ready status condition and namespace")
			updated := &platformv1alpha1.Tenant{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Namespace).To(Equal(resourceName))
			Expect(meta.IsStatusConditionTrue(updated.Status.Conditions, "Ready")).To(BeTrue())
		})
	})
})
