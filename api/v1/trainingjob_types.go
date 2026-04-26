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

package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TrainingJobPhase is the lifecycle phase of a TrainingJob.
type TrainingJobPhase string

const (
	PhasePending   TrainingJobPhase = "Pending"
	PhaseRunning   TrainingJobPhase = "Running"
	PhaseSucceeded TrainingJobPhase = "Succeeded"
	PhaseFailed    TrainingJobPhase = "Failed"
	PhaseRetrying  TrainingJobPhase = "Retrying"
)

// TrainingJobSpec defines the desired state of TrainingJob.
type TrainingJobSpec struct {
	// Image is the container image to run for training.
	Image string `json:"image"`

	// Command overrides the container entrypoint.
	// +optional
	Command []string `json:"command,omitempty"`

	// Args are passed to the container command.
	// +optional
	Args []string `json:"args,omitempty"`

	// Env sets environment variables in the training container.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// MaxRetries is the number of times the controller will retry a failed job.
	// +optional
	// +kubebuilder:default=0
	MaxRetries int32 `json:"maxRetries,omitempty"`
}

// TrainingJobStatus defines the observed state of TrainingJob.
type TrainingJobStatus struct {
	// Phase is the current lifecycle phase of the TrainingJob.
	// +optional
	Phase TrainingJobPhase `json:"phase,omitempty"`

	// JobName is the name of the active child Kubernetes Job.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// Retries is how many times the job has been retried so far.
	// +optional
	Retries int32 `json:"retries,omitempty"`

	// StartTime is when the first child Job was created.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the TrainingJob reached a terminal phase.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Message provides a human-readable status message.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Job",type="string",JSONPath=".status.jobName"
// +kubebuilder:printcolumn:name="Retries",type="integer",JSONPath=".status.retries"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// TrainingJob is the Schema for the trainingjobs API.
type TrainingJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TrainingJobSpec   `json:"spec,omitempty"`
	Status TrainingJobStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TrainingJobList contains a list of TrainingJob.
type TrainingJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TrainingJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TrainingJob{}, &TrainingJobList{})
}
