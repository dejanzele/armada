package job

import (
	"fmt"
	batchv1 "k8s.io/api/batch/v1"

	v1 "k8s.io/api/core/v1"

	"github.com/armadaproject/armada/internal/executor/configuration"
	util2 "github.com/armadaproject/armada/internal/executor/util"
	"github.com/armadaproject/armada/pkg/armadaevents"
	"github.com/armadaproject/armada/pkg/executorapi"
)

func CreateSubmitJobFromExecutorApiJobRunLease(
	jobRunLease *executorapi.JobRunLease,
	podDefaults *configuration.PodDefaults,
) (*SubmitJob, error) {
	var err error
	var pod *v1.Pod
	var job *batchv1.Job
	if util2.HasJobSpec(jobRunLease) {
		job, err = util2.CreateJobFromExecutorApiJob(jobRunLease, podDefaults)
		if err != nil {
			return nil, err
		}
	} else {
		pod, err = util2.CreatePodFromExecutorApiJob(jobRunLease, podDefaults)
		if err != nil {
			return nil, err
		}
	}

	jobId, err := armadaevents.UlidStringFromProtoUuid(jobRunLease.Job.JobId)
	if err != nil {
		return nil, err
	}

	runId, err := armadaevents.UuidStringFromProtoUuid(jobRunLease.JobRunId)
	if err != nil {
		return nil, err
	}

	runMeta := &RunMeta{
		JobId:  jobId,
		RunId:  runId,
		JobSet: jobRunLease.Jobset,
		Queue:  jobRunLease.Queue,
	}
	return &SubmitJob{
		Meta: SubmitJobMeta{
			RunMeta:         runMeta,
			Owner:           jobRunLease.User,
			OwnershipGroups: jobRunLease.Groups,
		},
		Pod:       pod,
		Job:       job,
		Ingresses: util2.ExtractIngresses(jobRunLease, pod, podDefaults.Ingress),
		Services:  util2.ExtractServices(jobRunLease, pod),
	}, nil
}

func ExtractJobRunMeta(pod *v1.Pod) (*RunMeta, error) {
	runId := util2.ExtractJobRunId(pod)
	if runId == "" {
		return nil, fmt.Errorf("job run id is missing")
	}
	jobId := util2.ExtractJobId(pod)
	if jobId == "" {
		return nil, fmt.Errorf("job id is missing")
	}
	queue := util2.ExtractQueue(pod)
	if queue == "" {
		return nil, fmt.Errorf("queue is missing")
	}
	jobSet := util2.ExtractJobSet(pod)
	if jobSet == "" {
		return nil, fmt.Errorf("job set is missing")
	}
	return &RunMeta{
		RunId:  runId,
		JobId:  jobId,
		JobSet: jobSet,
		Queue:  queue,
	}, nil
}
