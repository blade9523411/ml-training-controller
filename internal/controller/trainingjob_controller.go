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

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mlv1 "github.com/jayanthlalukota/ml-training-controller/api/v1"
)

// TrainingJobReconciler reconciles a TrainingJob object.
type TrainingJobReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=ml.jay.dev,resources=trainingjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ml.jay.dev,resources=trainingjobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ml.jay.dev,resources=trainingjobs/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives a TrainingJob toward its desired state.
//
// Flow:
//  1. Fetch TrainingJob; return early if terminal or not found.
//  2. If checkpointPath is set, ensure the checkpoint PVC exists.
//  3. Compute child Job name from retry count (run-0, run-1, …).
//  4. Create the child Job if it is missing.
//  5. Read the child Job's conditions to derive the desired phase.
//  6. If failed and retries remain, call startRetry; otherwise fall through to Failed.
//  7. Patch TrainingJob status if anything changed.
func (r *TrainingJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var tj mlv1.TrainingJob
	if err := r.Get(ctx, req.NamespacedName, &tj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if isTerminal(tj.Status.Phase) {
		return ctrl.Result{}, nil
	}

	if tj.Spec.CheckpointPath != "" {
		if err := r.ensureCheckpointPVC(ctx, &tj, logger); err != nil {
			return ctrl.Result{}, err
		}
	}

	jobName := jobNameForAttempt(tj.Name, tj.Status.Retries)

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
		logger.Info("Created child Job", "job", jobName, "attempt", tj.Status.Retries)
		r.Recorder.Eventf(&tj, corev1.EventTypeNormal, "JobCreated", "Created child Job %s", jobName)
		return ctrl.Result{}, nil
	} else if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting Job %s: %w", jobName, err)
	}

	desiredPhase, desiredMessage := jobPhase(&childJob)

	if desiredPhase == mlv1.PhaseFailed {
		if tj.Status.Retries < tj.Spec.MaxRetries {
			return r.startRetry(ctx, &tj, logger)
		}
		logger.Info("Retries exhausted, marking Failed",
			"retries", tj.Status.Retries, "maxRetries", tj.Spec.MaxRetries)
		r.Recorder.Eventf(&tj, corev1.EventTypeWarning, "RetriesExhausted",
			"All %d retries exhausted", tj.Spec.MaxRetries)
	}

	now := metav1.Now()
	original := tj.DeepCopy()

	tj.Status.JobName = jobName
	tj.Status.Phase = desiredPhase
	tj.Status.Message = desiredMessage
	if tj.Status.StartTime == nil {
		tj.Status.StartTime = &now
	}
	if isTerminal(desiredPhase) && tj.Status.CompletionTime == nil {
		tj.Status.CompletionTime = &now
	}

	if statusChanged(original, &tj) {
		logger.Info("Status transition", "phase", desiredPhase, "job", jobName)
		if err := r.Status().Patch(ctx, &tj, client.MergeFrom(original)); err != nil {
			return ctrl.Result{}, fmt.Errorf("patching status: %w", err)
		}
		r.emitPhaseEvent(&tj, original.Status.Phase, desiredPhase, desiredMessage)
	}

	return ctrl.Result{}, nil
}

// emitPhaseEvent fires a Kubernetes event when the phase transitions to a
// noteworthy state. Avoids duplicate events by only acting on changes.
func (r *TrainingJobReconciler) emitPhaseEvent(tj *mlv1.TrainingJob, oldPhase, newPhase mlv1.TrainingJobPhase, message string) {
	if oldPhase == newPhase {
		return
	}
	switch newPhase {
	case mlv1.PhaseRunning:
		r.Recorder.Event(tj, corev1.EventTypeNormal, "Running", message)
	case mlv1.PhaseSucceeded:
		r.Recorder.Event(tj, corev1.EventTypeNormal, "Succeeded", message)
	case mlv1.PhaseFailed:
		r.Recorder.Event(tj, corev1.EventTypeWarning, "Failed", message)
	}
}

// startRetry creates the next child Job and advances the retry counter in status.
// Idempotent: if the next Job already exists (prior status patch failed), creation
// is skipped and only the status patch is re-attempted.
func (r *TrainingJobReconciler) startRetry(ctx context.Context, tj *mlv1.TrainingJob, logger logr.Logger) (ctrl.Result, error) {
	nextAttempt := tj.Status.Retries + 1
	nextJobName := jobNameForAttempt(tj.Name, nextAttempt)

	var existing batchv1.Job
	err := r.Get(ctx, client.ObjectKey{Name: nextJobName, Namespace: tj.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		job := buildJob(tj, nextJobName)
		if err := ctrl.SetControllerReference(tj, job, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("setting owner reference for retry Job: %w", err)
		}
		if err := r.Create(ctx, job); err != nil {
			return ctrl.Result{}, fmt.Errorf("creating retry Job %s: %w", nextJobName, err)
		}
		logger.Info("Created retry Job", "job", nextJobName, "attempt", nextAttempt, "maxRetries", tj.Spec.MaxRetries)
	} else if err != nil {
		return ctrl.Result{}, fmt.Errorf("checking for retry Job %s: %w", nextJobName, err)
	}

	original := tj.DeepCopy()
	tj.Status.Retries = nextAttempt
	tj.Status.JobName = nextJobName
	tj.Status.Phase = mlv1.PhaseRetrying
	tj.Status.Message = fmt.Sprintf("Attempt %d failed, retrying (%d/%d)", nextAttempt-1, nextAttempt, tj.Spec.MaxRetries)
	if tj.Spec.CheckpointPath != "" {
		tj.Status.LastCheckpoint = tj.Spec.CheckpointPath + "/checkpoint.json"
	}
	if err := r.Status().Patch(ctx, tj, client.MergeFrom(original)); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching status for retry: %w", err)
	}
	r.Recorder.Eventf(tj, corev1.EventTypeWarning, "Retrying",
		"Attempt %d failed, retrying (%d/%d)", nextAttempt-1, nextAttempt, tj.Spec.MaxRetries)

	return ctrl.Result{}, nil
}

// ensureCheckpointPVC creates the PVC backing checkpoint storage if it does not
// exist. Owner reference ensures the PVC is deleted when the TrainingJob is deleted.
func (r *TrainingJobReconciler) ensureCheckpointPVC(ctx context.Context, tj *mlv1.TrainingJob, logger logr.Logger) error {
	pvcName := tj.Name + "-checkpoint"
	var pvc corev1.PersistentVolumeClaim
	err := r.Get(ctx, client.ObjectKey{Name: pvcName, Namespace: tj.Namespace}, &pvc)
	if apierrors.IsNotFound(err) {
		newPVC := buildCheckpointPVC(tj, pvcName)
		if err := ctrl.SetControllerReference(tj, newPVC, r.Scheme); err != nil {
			return fmt.Errorf("setting owner reference on checkpoint PVC: %w", err)
		}
		if err := r.Create(ctx, newPVC); err != nil {
			return fmt.Errorf("creating checkpoint PVC %s: %w", pvcName, err)
		}
		logger.Info("Created checkpoint PVC", "pvc", pvcName)
	} else if err != nil {
		return fmt.Errorf("getting checkpoint PVC %s: %w", pvcName, err)
	}
	return nil
}

// buildCheckpointPVC constructs the PVC that holds checkpoint data across retry attempts.
func buildCheckpointPVC(tj *mlv1.TrainingJob, pvcName string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: tj.Namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}
}

// jobPhase derives the TrainingJob phase and a human-readable message from the
// observed state of the child Kubernetes Job.
func jobPhase(job *batchv1.Job) (mlv1.TrainingJobPhase, string) {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			return mlv1.PhaseSucceeded, "Job completed successfully"
		}
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			msg := c.Message
			if msg == "" {
				msg = "Job failed"
			}
			return mlv1.PhaseFailed, msg
		}
	}
	if job.Status.Active > 0 {
		return mlv1.PhaseRunning, "Job is running"
	}
	return mlv1.PhasePending, "Job created, waiting to start"
}

// isTerminal reports whether phase is a terminal state the controller stops acting on.
func isTerminal(phase mlv1.TrainingJobPhase) bool {
	return phase == mlv1.PhaseSucceeded || phase == mlv1.PhaseFailed
}

// statusChanged reports whether any observable status field changed, to avoid
// unnecessary API calls when the desired state already matches the observed state.
func statusChanged(old, new *mlv1.TrainingJob) bool {
	o, n := old.Status, new.Status
	return o.Phase != n.Phase ||
		o.JobName != n.JobName ||
		o.Message != n.Message ||
		o.LastCheckpoint != n.LastCheckpoint ||
		(o.StartTime == nil) != (n.StartTime == nil) ||
		(o.CompletionTime == nil) != (n.CompletionTime == nil)
}

// jobNameForAttempt returns a deterministic child Job name for a given attempt number.
func jobNameForAttempt(trainingJobName string, attempt int32) string {
	return fmt.Sprintf("%s-run-%d", trainingJobName, attempt)
}

// buildJob constructs a child Kubernetes Job from the TrainingJob spec.
// When CheckpointPath is set the checkpoint PVC is mounted and CHECKPOINT_DIR is injected
// ahead of any user-provided env vars so it can be referenced by the trainer.
func buildJob(tj *mlv1.TrainingJob, jobName string) *batchv1.Job {
	backoffLimit := int32(0) // controller handles retries, not Kubernetes
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "ml-training-controller",
		"ml.jay.dev/training-job":      tj.Name,
	}

	env := []corev1.EnvVar{}
	var volumes []corev1.Volume
	var volumeMounts []corev1.VolumeMount

	if tj.Spec.CheckpointPath != "" {
		pvcName := tj.Name + "-checkpoint"
		env = append(env, corev1.EnvVar{
			Name:  "CHECKPOINT_DIR",
			Value: tj.Spec.CheckpointPath,
		})
		volumes = append(volumes, corev1.Volume{
			Name: "checkpoint",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcName,
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "checkpoint",
			MountPath: tj.Spec.CheckpointPath,
		})
	}
	env = append(env, tj.Spec.Env...)

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
					Volumes:       volumes,
					Containers: []corev1.Container{
						{
							Name:            "trainer",
							Image:           tj.Spec.Image,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Command:         tj.Spec.Command,
							Args:            tj.Spec.Args,
							Env:             env,
							VolumeMounts:    volumeMounts,
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
