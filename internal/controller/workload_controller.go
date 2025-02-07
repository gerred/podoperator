/*
Copyright 2025.

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
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	workloadsv1 "github.com/gerred/podoperator/api/v1"
)

const controllerName = "workload"

// WorkloadReconciler reconciles a Workload object
type WorkloadReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=workloads.sfcompute.com,resources=workloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=workloads.sfcompute.com,resources=workloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloads.sfcompute.com,resources=workloads/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete

func (r *WorkloadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithName(controllerName)

	// Fetch the Workload instance
	workload := &workloadsv1.Workload{}
	err := r.Get(ctx, req.NamespacedName, workload)
	if err != nil {
		if errors.IsNotFound(err) {
			// Return nil error to indicate successful reconciliation of a deleted object
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get Workload")
		return ctrl.Result{}, err
	}

	// Create or update the deployment
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workload.Name,
			Namespace: workload.Namespace,
		},
	}

	_, err = ctrl.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		// Set the workload instance as the owner
		if err := controllerutil.SetControllerReference(workload, deployment, r.Scheme); err != nil {
			return err
		}

		// Parse port mappings
		var containerPorts []corev1.ContainerPort
		for _, portMapping := range workload.Spec.Ports {
			parts := strings.Split(portMapping, ":")
			if len(parts) != 2 {
				continue
			}
			hostPort, err := strconv.Atoi(parts[0])
			if err != nil {
				continue
			}
			containerPort, err := strconv.Atoi(parts[1])
			if err != nil {
				continue
			}
			containerPorts = append(containerPorts, corev1.ContainerPort{
				ContainerPort: int32(containerPort),
				HostPort:      int32(hostPort),
			})
		}

		// Set deployment spec
		deployment.Spec = appsv1.DeploymentSpec{
			Replicas: ptr(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": workload.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": workload.Name,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "workload",
						Image: workload.Spec.Image,
						Ports: containerPorts,
					}},
				},
			},
		}

		return nil
	})
	if err != nil {
		logger.Error(err, "Failed to create or update deployment")
		return ctrl.Result{}, err
	}

	// Create or update the service
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workload.Name,
			Namespace: workload.Namespace,
		},
	}

	_, err = ctrl.CreateOrUpdate(ctx, r.Client, service, func() error {
		if err := controllerutil.SetControllerReference(workload, service, r.Scheme); err != nil {
			return err
		}

		var servicePorts []corev1.ServicePort
		for i, portMapping := range workload.Spec.Ports {
			parts := strings.Split(portMapping, ":")
			if len(parts) != 2 {
				continue
			}
			hostPort, err := strconv.Atoi(parts[0])
			if err != nil {
				continue
			}
			containerPort, err := strconv.Atoi(parts[1])
			if err != nil {
				continue
			}
			servicePorts = append(servicePorts, corev1.ServicePort{
				Name:       fmt.Sprintf("port-%d", i),
				Port:       int32(hostPort),
				TargetPort: intstr.FromInt(containerPort),
				NodePort:   int32(hostPort),
			})
		}

		service.Spec.Type = corev1.ServiceTypeNodePort
		service.Spec.Selector = map[string]string{
			"app": workload.Name,
		}
		service.Spec.Ports = servicePorts

		return nil
	})
	if err != nil {
		logger.Error(err, "Failed to create or update service")
		return ctrl.Result{}, err
	}

	// Update status
	workload.Status.ReadyReplicas = deployment.Status.ReadyReplicas
	if len(service.Spec.Ports) > 0 {
		workload.Status.ServiceIP = service.Spec.ClusterIP
	}

	if err := r.Status().Update(ctx, workload); err != nil {
		logger.Error(err, "Failed to update workload status")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully reconciled workload")
	return ctrl.Result{}, nil
}

// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=core,resources=endpoints,verbs=get;list;watch

// SetupWithManager sets up the controller with the Manager.
func (r *WorkloadReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&workloadsv1.Workload{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		WithOptions(controller.Options{}).
		Named(controllerName).
		Complete(r)
}

func ptr[T any](v T) *T {
	return &v
}
