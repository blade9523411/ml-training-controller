# ml-training-controller

A Kubernetes controller for fault-tolerant ML training workflows built with Go and Kubebuilder.

Users submit a `TrainingJob` custom resource. The controller creates and manages Kubernetes Jobs that run containerised training workloads, retrying on failure and resuming from checkpoints across attempts.

---

## Architecture

```
User applies TrainingJob YAML
           │
           ▼
  Kubernetes API Server
           │  (watch)
           ▼
  TrainingJobReconciler
  ┌─────────────────────────────────────────┐
  │  1. Ensure checkpoint PVC (if needed)   │
  │  2. Create child Job  <name>-run-<n>    │
  │  3. Read Job conditions → derive phase  │
  │  4. Retry or mark terminal              │
  │  5. Patch TrainingJob.status            │
  └─────────────────────────────────────────┘
           │
           ▼
   Kubernetes Job  ──▶  Pod  ──▶  trainer container
           │                          │
           │  (Owns: re-triggers      │  writes checkpoint.json
           │   Reconcile on change)   │  reads  checkpoint.json
           ▼                          ▼
   TrainingJob.status            PVC  <name>-checkpoint
   phase / jobName / retries
   startTime / completionTime
   lastCheckpoint
```

---

## CRD Reference

### TrainingJobSpec

| Field | Type | Required | Description |
|---|---|---|---|
| `image` | string | yes | Container image to run |
| `command` | []string | no | Override container entrypoint |
| `args` | []string | no | Arguments passed to the command |
| `env` | []EnvVar | no | Environment variables injected into the container |
| `maxRetries` | int32 | no | Max retry attempts on failure (default 0) |
| `checkpointPath` | string | no | Mount path for checkpoint PVC; enables resume-on-retry |

### TrainingJobStatus

| Field | Type | Description |
|---|---|---|
| `phase` | string | Current lifecycle phase (see below) |
| `jobName` | string | Name of the active child Job |
| `retries` | int32 | Number of retries performed so far |
| `startTime` | Time | When the first child Job was created |
| `completionTime` | Time | When the TrainingJob reached a terminal phase |
| `message` | string | Human-readable status message |
| `lastCheckpoint` | string | Path of the most recent checkpoint file (set on retry) |

### Phases

| Phase | Meaning |
|---|---|
| `Pending` | Child Job created, no pods running yet |
| `Running` | At least one active pod |
| `Retrying` | Current attempt failed; next Job created |
| `Succeeded` | Child Job completed successfully |
| `Failed` | Child Job failed and retries are exhausted |

---

## Reconcile Flow

```
Fetch TrainingJob
  └─ NotFound → ignore (object deleted)
  └─ Terminal (Succeeded / Failed) → return early

If checkpointPath set → ensure PVC <name>-checkpoint exists

jobName = <name>-run-<status.retries>

Fetch child Job <jobName>
  └─ NotFound → Create Job, emit JobCreated event, return
                (Owns() re-triggers on Job creation)

Read Job.status.conditions:
  JobComplete=True  → desiredPhase = Succeeded
  JobFailed=True    → desiredPhase = Failed
  Active > 0        → desiredPhase = Running
  otherwise         → desiredPhase = Pending

If desiredPhase == Failed:
  retries < maxRetries → startRetry:
      create <name>-run-<retries+1>
      patch status: retries++, phase=Retrying, jobName=<next>
      emit Retrying event
  retries == maxRetries → emit RetriesExhausted event, fall through

Patch status if changed:
  phase / jobName / message / startTime / completionTime
  emit Running / Succeeded / Failed event on transition
```

---

## Quick Start (kind)

### Prerequisites

- Go 1.23+
- Docker
- [kind](https://kind.sigs.k8s.io/)
- kubectl
- kubebuilder (for regenerating manifests after type changes)

### 1. Create the kind cluster

```bash
kind create cluster --name ml-controller
kubectl cluster-info --context kind-ml-controller
```

### 2. Install CRDs

```bash
make generate
make manifests
make install

kubectl get crd | grep trainingjobs
```

### 3. Build and load the trainer image

```bash
docker build -t trainer:dev ./trainer
kind load docker-image trainer:dev --name ml-controller
```

### 4. Run the controller locally

```bash
make run
```

The controller runs against whichever cluster `~/.kube/config` points to. Leave this terminal open and use a second terminal for the demos below.

---

## Demo Scenarios

### 1. Successful training run

```bash
kubectl apply -f config/samples/ml_v1_trainingjob.yaml

# Watch phase: Pending → Running → Succeeded
kubectl get trainingjobs -w

# Stream trainer output
kubectl logs -l ml.jay.dev/training-job=sample-trainingjob -f
```

### 2. Immediate failure, no retries

```bash
kubectl apply -f config/samples/ml_v1_trainingjob_fail.yaml

kubectl get trainingjobs -w
# Phase reaches Failed after one attempt
```

### 3. Retry on failure

```bash
kubectl apply -f config/samples/ml_v1_trainingjob_retry.yaml

# Phase cycles: Pending → Running → Retrying → Running → Retrying → ... → Failed
kubectl get trainingjobs -w

# Confirm all four attempt Jobs were kept
kubectl get jobs -l ml.jay.dev/training-job=retry-trainingjob
```

### 4. Checkpoint recovery

```bash
kubectl apply -f config/samples/ml_v1_trainingjob_checkpoint.yaml

# run-0 completes epochs 1–5, writes checkpoint, then fails
# run-1 resumes from epoch 6 and completes successfully
kubectl get trainingjobs -w

# Confirm the PVC was provisioned
kubectl get pvc

# Check run-1 resumed from the checkpoint
kubectl logs -l ml.jay.dev/training-job=checkpoint-trainingjob --prefix | grep "run-1"
# Expected: [trainer] Resuming from checkpoint: epoch=5 loss=0.2000

# Inspect full status
kubectl get trainingjob checkpoint-trainingjob -o yaml
```

### Viewing events

```bash
kubectl describe trainingjob <name>
# Events section shows: JobCreated, Running, Retrying, Succeeded / Failed
```

### Cleanup

```bash
# Delete a specific TrainingJob and all its owned resources (Jobs, PVC)
kubectl delete trainingjob <name>

# Remove all samples
kubectl delete -f config/samples/

# Uninstall CRDs
make uninstall
```

---

## Project Structure

```
ml-training-controller/
├── api/v1/
│   ├── trainingjob_types.go       # CRD spec and status types
│   ├── groupversion_info.go       # Scheme registration
│   └── zz_generated.deepcopy.go  # Generated DeepCopy methods
├── internal/controller/
│   └── trainingjob_controller.go  # Reconcile loop and helpers
├── trainer/
│   ├── train.py                   # Simulated trainer with checkpoint support
│   └── Dockerfile                 # python:3.11-slim image
├── config/
│   ├── crd/                       # Generated CRD manifests
│   ├── rbac/                      # Generated RBAC manifests
│   └── samples/                   # Example TrainingJob YAMLs
│       ├── ml_v1_trainingjob.yaml            # Successful run
│       ├── ml_v1_trainingjob_fail.yaml       # Terminal failure, no retries
│       ├── ml_v1_trainingjob_retry.yaml      # Always fails, 3 retries
│       └── ml_v1_trainingjob_checkpoint.yaml # Checkpoint recovery across retry
└── cmd/main.go                    # Controller manager entrypoint
```

---

## Limitations

- **Single-pod only.** No distributed training support (no multi-worker Jobs).
- **No GPU scheduling.** Resource requests are not wired through the spec.
- **No webhook validation.** Invalid specs (e.g. missing image) are not rejected at admission time.
- **Failed Jobs are retained.** Old attempt Jobs are kept for log inspection; no TTL cleanup.
- **PVC uses default storage class.** Relies on kind's `local-path-provisioner`; not tested with other provisioners.
- **`lastCheckpoint` is inferred, not observed.** The controller records the expected checkpoint path on retry rather than confirming the file was actually written.
- **`ImagePullPolicy` is hardcoded to `IfNotPresent`.** Suitable for kind; would need a spec field for production use.

---

## Stretch Goals

- Distributed training via multi-worker Jobs or a dedicated framework (Kubeflow Training Operator pattern)
- GPU resource requests (`spec.resources`)
- Validating webhook to reject bad specs at admission time
- Prometheus metrics: active jobs, retry counts, training duration
- Configurable `imagePullPolicy` and `imagePullSecrets`
- TTL-based cleanup of completed child Jobs
- Pluggable checkpoint backends (S3, GCS) beyond local PVC
- `backoffSeconds` between retries
- Graceful shutdown: pass `SIGTERM` to the trainer and allow it to flush a checkpoint

---

## License

Copyright 2026. Licensed under the Apache License, Version 2.0.
