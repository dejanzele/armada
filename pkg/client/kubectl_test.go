package client

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

func TestGetKubectlCommand(t *testing.T) {
	tests := map[string]struct {
		template  string
		cluster   string
		namespace string
		jobId     string
		podNumber int
		cmd       string
		expected  string
	}{
		"default template selects pod by labels": {
			cluster:   "cluster-1",
			namespace: "test-ns",
			jobId:     "01hqv6y6z8w9x0y1z2a3b4c5d6",
			podNumber: 0,
			cmd:       "logs",
			expected:  "kubectl --context cluster-1 -n test-ns logs -l armada_job_id=01hqv6y6z8w9x0y1z2a3b4c5d6,armada_pod_number=0",
		},
		"custom template": {
			template:  "k --context {{cluster}} -n {{namespace}} {{cmd}} {{pod}}",
			cluster:   "cluster-2",
			namespace: "other-ns",
			jobId:     "job-1",
			podNumber: 1,
			cmd:       "describe pod",
			expected:  "k --context cluster-2 -n other-ns describe pod -l armada_job_id=job-1,armada_pod_number=1",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			viper.Set("kubectlCommandTemplate", tc.template)
			defer viper.Set("kubectlCommandTemplate", "")

			command := GetKubectlCommand(tc.cluster, tc.namespace, tc.jobId, tc.podNumber, tc.cmd)
			assert.Equal(t, tc.expected, command)
		})
	}
}
