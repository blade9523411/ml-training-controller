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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mlv1 "github.com/jayanthlalukota/ml-training-controller/api/v1"
)

// TrainingJobReconciler reconciles a TrainingJob object.
type TrainingJobReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=ml.jay.dev,resources=trainingjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ml.jay.dev,resources=trainingjobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ml.jay.dev,resources=trainingjobs/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

func (r *TrainingJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var tj mlv1.TrainingJob
	if err := r.Get(ctx, req.NamespacedName, &tj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Nothing to do once we've reached a terminal state.
	if tj.Status.Phase == mlv1.PhaseSucceeded || tj.Status.Phase == mlv1.PhaseFailed {
		return ctrl.Result{}, nil
	}

	jobName := jobNameForAttempt(tj.Name, 0)

	// Ensure the child Job exists; create it if missing.
	var childJob batchv1.Job
	err := r.Get(ctx, client.ObjectKey{Name: jobName, Namespace: tj.Namespace}, &childJob)
	if apierrors.IsNotFound(err) {
		job := buildJob(&tj, jobName)
		if err := ctrl.SetControllerReference(&tj, job, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("setting owner reference: %w", err)
		}
		if err := r.Create(ctx, job); err != nil {
			return ctrl.Result{}, fmt.Errorf("creating Job %s: %w", jobName, err)
		}
		logger.Info("Created child Job", "job", jobName)
	} else if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting Job %s: %w", jobName, err)
	}

	// Set initial status once the Job name is known. Subsequent phases will
	// overwrite Phase as the Job progresses.
	if tj.Status.JobName == "" {
		original := tj.DeepCopy()
		tj.Status.JobName = jobName
		tj.Status.Phase = mlv1.PhasePending
		tj.Status.Message = "Child Job created, waiting to start"
		if err := r.Status().Patch(ctx, &tj, client.MergeFrom(original)); err != nil {
			return ctrl.Result{}, fmt.Errorf("patching status: %w", err)
		}
	}

	return ctrl.Result{}, nil
}

// jobNameForAttempt returns a deterministic child Job name for a given attempt number.
func jobNameForAttempt(trainingJobName string, attempt int32) string {
	return fmt.Sprintf("%s-run-%d", trainingJobName, attempt)
}

// buildJob constructs a child Kubernetes Job from the TrainingJob spec.
func buildJob(tj *mlv1.TrainingJob, jobName string) *batchv1.Job {
	backoffLimit := int32(0) // controller handles retries, not Kubernetes
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "ml-training-controller",
		"ml.jay.dev/training-job":      tj.Name,
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: tj.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "trainer",
							Image:   tj.Spec.Image,
							Command: tj.Spec.Command,
							Args:    tj.Spec.Args,
							Env:     tj.Spec.Env,
						},
					},
				},
			},
		},
	}
}

// SetupWithManager registers the controller with the manager and declares
// that it owns child Jobs so Job state changes re-trigger Reconcile.
func (r *TrainingJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mlv1.TrainingJob{}).
		Owns(&batchv1.Job{}).
		Named("trainingjob").
		Complete(r)
}
