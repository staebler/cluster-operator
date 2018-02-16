/*
Copyright 2018 The Kubernetes Authors.

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
	"fmt"
	"regexp"
	"time"

	log "github.com/sirupsen/logrus"

	v1batch "k8s.io/api/batch/v1"
	kapi "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// JobSync is used by a controller to sync an object that uses a job to do
// its processing.
type JobSync interface {
	// Sync syncs the object with the specified key.
	Sync(key string) error
}

// JobSyncConditionType is the type of condition that the job sync is
// adjusting on the owner object.
type JobSyncConditionType string

const (
	// JobSyncProcessing indicates that the processing job is in progress.
	JobSyncProcessing JobSyncConditionType = "Processing"
	// JobSyncProcessed indicates that the processing job has completed
	// successfully.
	JobSyncProcessed JobSyncConditionType = "Processed"
	// JobSyncProcessingFailed indicates that the processing job has failed.
	JobSyncProcessingFailed JobSyncConditionType = "ProcessingFailed"
	// JobSyncUndoing indicates that the undoing job is in progress.
	JobSyncUndoing JobSyncConditionType = "Undoing"
	// JobSyncUndoFailed indicates that the undoing job has failed.
	JobSyncUndoFailed JobSyncConditionType = "UndoFailed"
)

const (
	// ReasonJobRunning is a condition reason used when a job is still
	// running.
	ReasonJobRunning = "JobRunning"
	// ReasonJobCompleted is a condition reason used when a job has been
	// completed successfully.
	ReasonJobCompleted = "JobCompleted"
	// ReasonJobFailed is a condition reason used when a job has failed.
	ReasonJobFailed = "JobFailed"
	// ReasonJobMissing is a condition reason used when a job that was
	// expected to exist does not exist.
	ReasonJobMissing = "JobMissing"
	// ReasonSpecChanged is a condition reason used when the spec of an
	// object changes, invalidating existing jobs.
	ReasonSpecChanged = "SpecChanged"
)

var (
	validateFinalizer = regexp.MustCompile("[A-Za-z0-9]$")
)

type jobSync struct {
	jobControl   JobControl
	strategy     JobSyncStrategy
	undoOnDelete bool
	logger       log.FieldLogger
}

// NewJobSync creates a new JobSync.
func NewJobSync(jobControl JobControl, strategy JobSyncStrategy, undoOnDelete bool, logger log.FieldLogger) JobSync {
	return &jobSync{
		jobControl:   jobControl,
		strategy:     strategy,
		undoOnDelete: undoOnDelete,
		logger:       logger,
	}
}

func (s *jobSync) Sync(key string) error {
	logger := log.FieldLogger(s.logger.WithField("key", key))
	startTime := time.Now()
	logger.Debugln("Started syncing")
	defer logger.WithField("duration", time.Since(startTime)).Debugln("Finished syncing")

	owner, err := s.strategy.GetOwner(key)
	if errors.IsNotFound(err) {
		logger.Debugln("owner has been deleted")
		s.jobControl.ObserveOwnerDeletion(key)
		return nil
	}
	if err != nil {
		return err
	}

	logger = loggerForOwner(s.logger, owner)

	deleting := false

	// Are we dealing with an owner marked for deletion
	if owner.GetDeletionTimestamp() != nil {
		if !s.undoOnDelete {
			return nil
		}
		if !s.hasFinalizer(owner) {
			return nil
		}
		logger.Debugf("Undoing job processing on delete")
		deleting = true
	}

	currentJobName := s.strategy.GetOwnerCurrentJob(owner)

	needsProcessing := deleting || s.strategy.DoesOwnerNeedProcessing(owner)

	jobFactory, err := s.strategy.GetJobFactory(owner, deleting)
	if err != nil {
		return err
	}

	jobControlResult, job, err := s.jobControl.ControlJobs(key, owner, currentJobName, needsProcessing, jobFactory)
	if err != nil {
		return err
	}

	switch jobControlResult {
	case JobControlJobWorking:
		return s.syncOwnerStatusWithJob(owner, job, deleting)
	case JobControlDeletingJobs:
		if currentJobName == "" {
			return nil
		}
		return s.setOwnerStatusForOutdatedJob(owner)
	case JobControlLostCurrentJob:
		return s.setOwnerStatusForLostJob(owner, deleting)
	case JobControlCreatingJob:
		logger.Debugf("creating job")
		if s.undoOnDelete {
			logger.Debugf("adding finalizer")
			return s.addFinalizer(owner)
		}
		return nil
	case JobControlPendingExpectations, JobControlNoWork:
		return nil
	default:
		return fmt.Errorf("unknown job control result: %v", jobControlResult)
	}
}

// setOwnerStatusForOutdatedJob updates the processing condition
// for the owner to reflect that an in-progress job is no longer processing
// due to a change in the spec of the owner.
func (s *jobSync) setOwnerStatusForOutdatedJob(original metav1.Object) error {
	owner := s.strategy.DeepCopyOwner(original)
	s.strategy.SetOwnerJobSyncCondition(
		owner,
		JobSyncProcessing,
		kapi.ConditionFalse,
		ReasonSpecChanged,
		"Spec changed. New job needed",
		UpdateConditionNever,
	)
	s.strategy.SetOwnerCurrentJob(owner, "")
	return s.strategy.UpdateOwnerStatus(original, owner)
}

// syncOwnerStatusWithJob update the status of the owner to
// reflect the current status of the job that is processing the owner.
// If the job completed successfully, the owner will be marked as
// processed.
// If the job completed with a failure, the owner will be marked as
// not processed.
// If the job is still in progress, the owner will be marked as
// processing.
func (s *jobSync) syncOwnerStatusWithJob(owner metav1.Object, job *v1batch.Job, deleting bool) error {
	if job == nil {
		return fmt.Errorf("job control result was that a job was working, but no job was returned")
	}

	jobCompleted := findJobCondition(job, v1batch.JobComplete)
	if jobCompleted != nil && jobCompleted.Status == kapi.ConditionTrue {
		return s.setOwnerStatusForCompletedJob(
			owner,
			deleting,
			ReasonJobCompleted,
			fmt.Sprintf("Job %s/%s completed at %v", job.Namespace, job.Name, jobCompleted.LastTransitionTime),
		)
	}

	jobFailed := findJobCondition(job, v1batch.JobFailed)
	if jobFailed != nil && jobFailed.Status == kapi.ConditionTrue {
		return s.setOwnerStatusForFailedJob(
			owner,
			deleting,
			ReasonJobFailed,
			fmt.Sprintf("Job %s/%s failed at %v, reason: %s", job.Namespace, job.Name, jobFailed.LastTransitionTime, jobFailed.Reason),
		)
	}

	return s.setOwnerStatusForInProgressJob(
		owner,
		job,
		deleting,
		ReasonJobRunning,
		fmt.Sprintf("Job %s/%s is running since %v. Pod completions: %d, failures: %d", job.Namespace, job.Name, job.Status.StartTime, job.Status.Succeeded, job.Status.Failed),
	)
}

func (s *jobSync) setOwnerStatusForLostJob(owner metav1.Object, deleting bool) error {
	return s.setOwnerStatusForFailedJob(owner, deleting, ReasonJobMissing, "Job not found.")
}

func (s *jobSync) setOwnerStatusForCompletedJob(original metav1.Object, deleting bool, reason, message string) error {
	owner := s.strategy.DeepCopyOwner(original)
	if deleting {
		s.strategy.SetOwnerJobSyncCondition(owner, JobSyncUndoing, kapi.ConditionFalse, reason, message, UpdateConditionNever)
		s.strategy.SetOwnerJobSyncCondition(owner, JobSyncProcessed, kapi.ConditionFalse, reason, message, UpdateConditionNever)
		s.strategy.SetOwnerJobSyncCondition(owner, JobSyncUndoFailed, kapi.ConditionFalse, reason, message, UpdateConditionNever)
	} else {
		s.strategy.SetOwnerJobSyncCondition(owner, JobSyncProcessing, kapi.ConditionFalse, reason, message, UpdateConditionNever)
		s.strategy.SetOwnerJobSyncCondition(owner, JobSyncProcessed, kapi.ConditionTrue, reason, message, UpdateConditionAlways)
		s.strategy.SetOwnerJobSyncCondition(owner, JobSyncProcessingFailed, kapi.ConditionFalse, reason, message, UpdateConditionNever)
	}
	s.strategy.SetOwnerCurrentJob(owner, "")
	if deleting {
		finalizerName := s.getFinalizerName()
		finalizers := sets.NewString(owner.GetFinalizers()...)
		finalizers.Delete(finalizerName)
		owner.SetFinalizers(finalizers.List())
	} else {
		s.strategy.OnJobCompletion(owner)
	}
	return s.strategy.UpdateOwnerStatus(original, owner)
}

func (s *jobSync) setOwnerStatusForFailedJob(original metav1.Object, deleting bool, reason, message string) error {
	owner := s.strategy.DeepCopyOwner(original)
	workingCondtion := JobSyncProcessing
	workingFailedCondtion := JobSyncProcessingFailed
	if deleting {
		workingCondtion = JobSyncUndoing
		workingFailedCondtion = JobSyncUndoFailed
	}
	s.strategy.SetOwnerJobSyncCondition(owner, workingCondtion, kapi.ConditionFalse, reason, message, UpdateConditionNever)
	s.strategy.SetOwnerJobSyncCondition(owner, workingFailedCondtion, kapi.ConditionTrue, reason, message, UpdateConditionAlways)
	s.strategy.SetOwnerCurrentJob(owner, "")
	if !deleting {
		s.strategy.OnJobFailure(owner)
	}
	return s.strategy.UpdateOwnerStatus(original, owner)
}

func (s *jobSync) setOwnerStatusForInProgressJob(original metav1.Object, job *v1batch.Job, deleting bool, reason, message string) error {
	owner := s.strategy.DeepCopyOwner(original)
	workingCondtion := JobSyncProcessing
	if deleting {
		workingCondtion = JobSyncUndoing
	}
	s.strategy.SetOwnerJobSyncCondition(
		owner,
		workingCondtion,
		kapi.ConditionTrue,
		reason,
		message,
		UpdateConditionIfReasonOrMessageChange,
	)
	s.strategy.SetOwnerCurrentJob(owner, job.Name)
	return s.strategy.UpdateOwnerStatus(original, owner)
}

func (s *jobSync) hasFinalizer(owner metav1.Object) bool {
	finalizer := s.getFinalizerName()
	for _, f := range owner.GetFinalizers() {
		if f == finalizer {
			return true
		}
	}
	return false
}

func (s *jobSync) addFinalizer(original metav1.Object) error {
	if s.hasFinalizer(original) {
		return nil
	}
	owner := s.strategy.DeepCopyOwner(original)
	finalizers := sets.NewString(owner.GetFinalizers()...)
	finalizers.Insert(s.getFinalizerName())
	owner.SetFinalizers(finalizers.List())
	return s.strategy.UpdateOwnerStatus(original, owner)
}

func (s *jobSync) getFinalizerName() string {
	finalizerName := "openshift/cluster-operator-" + s.jobControl.GetJobPrefix()
	if !validateFinalizer.MatchString(finalizerName) {
		finalizerName = finalizerName + "1"
	}
	return finalizerName
}

// findJobCondition finds in the job the condition that has the
// specified condition type. If none exists, then returns nil.
func findJobCondition(job *v1batch.Job, conditionType v1batch.JobConditionType) *v1batch.JobCondition {
	for i, condition := range job.Status.Conditions {
		if condition.Type == conditionType {
			return &job.Status.Conditions[i]
		}
	}
	return nil
}
