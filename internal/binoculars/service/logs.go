package service

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/armadaproject/armada/internal/common/armadacontext"
	"github.com/armadaproject/armada/internal/common/auth"
	"github.com/armadaproject/armada/internal/common/cluster"
	log "github.com/armadaproject/armada/internal/common/logging"
	"github.com/armadaproject/armada/internal/executor/domain"
	"github.com/armadaproject/armada/pkg/api/binoculars"
)

type LogService interface {
	GetLogs(ctx *armadacontext.Context, params *LogParams) ([]*binoculars.LogLine, error)
}

type LogParams struct {
	Principal  auth.Principal
	Namespace  string
	JobId      string
	PodNumber  int32
	SinceTime  string
	LogOptions *v1.PodLogOptions
}

type KubernetesLogService struct {
	clientProvider cluster.KubernetesClientProvider
}

const MaxLogBytes = 2000000

func NewKubernetesLogService(clientProvider cluster.KubernetesClientProvider) *KubernetesLogService {
	return &KubernetesLogService{clientProvider: clientProvider}
}

func (l *KubernetesLogService) GetLogs(ctx *armadacontext.Context, params *LogParams) ([]*binoculars.LogLine, error) {
	client, err := l.clientProvider.ClientForUser(params.Principal.GetName(), params.Principal.GetGroupNames())
	if err != nil {
		return nil, err
	}

	since, err := time.Parse(time.RFC3339Nano, params.SinceTime)
	if err == nil {
		params.LogOptions.SinceTime = &metav1.Time{Time: since}
	} else {
		if params.SinceTime != "" {
			log.Warnf("failed to parse since time for job %s: %v", params.JobId, err)
		}
	}

	limitBytes := int64(MaxLogBytes)
	params.LogOptions.Follow = false
	params.LogOptions.Timestamps = true
	params.LogOptions.LimitBytes = &limitBytes

	if params.Namespace == "" {
		params.Namespace = "default"
	}

	podName, err := resolvePodName(ctx, client, params.Namespace, params.JobId, params.PodNumber)
	if err != nil {
		return nil, err
	}

	req := client.CoreV1().
		Pods(params.Namespace).
		GetLogs(podName, params.LogOptions)

	result := req.Do(ctx)
	if err := result.Error(); err != nil {
		if errors.IsNotFound(err) {
			return nil, status.Error(codes.NotFound, "The pod with these logs doesn't exist - this is likely because the job has finished and the pod has been cleaned up")
		}
		return nil, err
	}

	rawLog, err := result.Raw()
	if err != nil {
		return nil, err
	}

	logLines, errs := ConvertLogs(rawLog)
	for _, err := range errs {
		log.Errorf(
			"failed to parse log line for namespace: %q, pod: %q: %v",
			params.Namespace,
			podName,
			err)
	}

	return logLines, nil
}

// resolvePodName finds the pod belonging to a job via the labels the executor
// stamps on every pod. Pod names can no longer be derived from the job id
// alone: retried runs carry a run-index suffix in their name.
func resolvePodName(ctx *armadacontext.Context, client kubernetes.Interface, namespace string, jobId string, podNumber int32) (string, error) {
	selector := fmt.Sprintf("%s=%s,%s=%d", domain.JobId, jobId, domain.PodNumber, podNumber)
	pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return "", fmt.Errorf("failed to list pods for job %s: %w", jobId, err)
	}
	if len(pods.Items) == 0 {
		return "", status.Error(codes.NotFound, "The pod with these logs doesn't exist - this is likely because the job has finished and the pod has been cleaned up")
	}
	return selectLatestAttempt(pods.Items).Name, nil
}

// selectLatestAttempt picks the most recent attempt when several attempts of
// the same job coexist, e.g. while a predecessor pod is stuck terminating.
// Pods without a run-index label are first attempts (index 0). Creation
// timestamp breaks ties, since indexes are strictly increasing per job this
// only matters for pods predating the run-index label.
func selectLatestAttempt(pods []v1.Pod) *v1.Pod {
	best := &pods[0]
	bestIndex := podRunIndex(best)
	for i := 1; i < len(pods); i++ {
		candidate := &pods[i]
		candidateIndex := podRunIndex(candidate)
		if candidateIndex > bestIndex ||
			(candidateIndex == bestIndex && candidate.CreationTimestamp.After(best.CreationTimestamp.Time)) {
			best = candidate
			bestIndex = candidateIndex
		}
	}
	return best
}

func podRunIndex(pod *v1.Pod) uint64 {
	index, err := strconv.ParseUint(pod.Labels[domain.JobRunIndex], 10, 32)
	if err != nil {
		return 0
	}
	return index
}

func ConvertLogs(rawLog []byte) ([]*binoculars.LogLine, []error) {
	lines := strings.Split(string(rawLog), "\n")
	// If log is larger than MAX_PAYLOAD_SIZE, discard last lines until it is smaller or equal to MAX_PAYLOAD_SIZE
	if len(rawLog) > MaxLogBytes {
		lines = truncateLog(lines, len(rawLog))
	}

	var logLines []*binoculars.LogLine
	var errs []error
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if line == "" { // Can happen if we have a trailing newline
			continue
		}

		logLine, err := splitLine(lines[i])
		if err != nil {
			errs = append(errs, err)
			continue
		}
		logLines = append(logLines, logLine)
	}

	return logLines, errs
}

func truncateLog(lines []string, total int) []string {
	lastExclIndex := len(lines)
	for total > MaxLogBytes {
		lastLine := lines[lastExclIndex-1]
		total -= len(lastLine) + 1 // newline removed with strings.Split
		lastExclIndex--
	}
	return lines[:lastExclIndex]
}

func splitLine(rawLine string) (*binoculars.LogLine, error) {
	spaceIdx := strings.Index(rawLine, " ")
	if spaceIdx == -1 {
		return nil, fmt.Errorf("badly formatted log line: %q", rawLine)
	}

	timestamp := rawLine[:spaceIdx]
	line := rawLine[spaceIdx+1:]

	_, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return nil, fmt.Errorf("failed parse timestamp in log line: %q: %v", rawLine, err)
	}

	return &binoculars.LogLine{Timestamp: timestamp, Line: line}, nil
}
