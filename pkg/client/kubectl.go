package client

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"

	"github.com/armadaproject/armada/internal/executor/domain"
)

func GetKubectlCommand(cluster string, namespace string, jobId string, podNumber int, cmd string) string {
	t := viper.GetString("kubectlCommandTemplate")
	if t == "" {
		t = "kubectl --context {{cluster}} -n {{namespace}} {{cmd}} {{pod}}"
	}
	// Select the pod by the labels the executor stamps on it rather than
	// reconstructing its name: retried runs carry a run-index suffix in
	// their name, so names are no longer derivable from the job id alone.
	r := strings.NewReplacer(
		"{{cluster}}",
		cluster,
		"{{namespace}}",
		namespace,
		"{{cmd}}",
		cmd,
		"{{pod}}",
		fmt.Sprintf("-l %s=%s,%s=%d", domain.JobId, jobId, domain.PodNumber, podNumber),
	)
	command := r.Replace(t)

	return command
}
