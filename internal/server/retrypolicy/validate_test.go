package retrypolicy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/armadaproject/armada/pkg/api"
)

func TestValidatePolicy(t *testing.T) {
	validRule := func() *api.RetryRule {
		return &api.RetryRule{
			Action:       api.RetryAction_RETRY_ACTION_RETRY,
			OnConditions: []string{"OOMKilled"},
		}
	}

	tests := map[string]struct {
		policy      *api.RetryPolicy
		wantErr     string
		wantErrNone bool
	}{
		"nil policy": {
			policy:  nil,
			wantErr: "must not be nil",
		},
		"empty name": {
			policy:  &api.RetryPolicy{DefaultAction: api.RetryAction_RETRY_ACTION_FAIL},
			wantErr: "name must not be empty",
		},
		"name with uppercase characters": {
			policy: &api.RetryPolicy{
				Name:          "MyPolicy",
				DefaultAction: api.RetryAction_RETRY_ACTION_FAIL,
			},
			wantErr: "is invalid",
		},
		"name with leading dash": {
			policy: &api.RetryPolicy{
				Name:          "-policy",
				DefaultAction: api.RetryAction_RETRY_ACTION_FAIL,
			},
			wantErr: "is invalid",
		},
		"name with trailing dash": {
			policy: &api.RetryPolicy{
				Name:          "policy-",
				DefaultAction: api.RetryAction_RETRY_ACTION_FAIL,
			},
			wantErr: "is invalid",
		},
		"name too long": {
			policy: &api.RetryPolicy{
				Name:          strings.Repeat("a", maxPolicyNameLength+1),
				DefaultAction: api.RetryAction_RETRY_ACTION_FAIL,
			},
			wantErr: "at most 63 characters",
		},
		"unspecified default action rejected even with rules": {
			policy: &api.RetryPolicy{
				Name: "p1",
				Rules: []*api.RetryRule{
					{Action: api.RetryAction_RETRY_ACTION_RETRY, OnConditions: []string{"OOMKilled"}},
				},
			},
			wantErr: "must set a default action",
		},
		"nil rule": {
			policy: &api.RetryPolicy{
				Name:  "p1",
				Rules: []*api.RetryRule{nil},
			},
			wantErr: "rule must not be nil",
		},
		"rule with unspecified action": {
			policy: &api.RetryPolicy{
				Name: "p1",
				Rules: []*api.RetryRule{
					{OnConditions: []string{"OOMKilled"}},
				},
			},
			wantErr: "action must be specified",
		},
		"rule with no matchers": {
			policy: &api.RetryPolicy{
				Name: "p1",
				Rules: []*api.RetryRule{
					{Action: api.RetryAction_RETRY_ACTION_RETRY},
				},
			},
			wantErr: "at least one matcher",
		},
		"rule with only subcategory": {
			policy: &api.RetryPolicy{
				Name: "p1",
				Rules: []*api.RetryRule{
					{
						Action:        api.RetryAction_RETRY_ACTION_RETRY,
						OnSubcategory: "oom",
					},
				},
			},
			wantErr: "at least one matcher",
		},
		"subcategory without category": {
			policy: &api.RetryPolicy{
				Name: "p1",
				Rules: []*api.RetryRule{
					{
						Action:        api.RetryAction_RETRY_ACTION_RETRY,
						OnConditions:  []string{"OOMKilled"},
						OnSubcategory: "oom",
					},
				},
			},
			wantErr: "on_subcategory requires on_category",
		},
		"exit code matcher with unspecified operator": {
			policy: &api.RetryPolicy{
				Name: "p1",
				Rules: []*api.RetryRule{
					{
						Action:      api.RetryAction_RETRY_ACTION_RETRY,
						OnExitCodes: &api.RetryExitCodeMatcher{Values: []int32{1}},
					},
				},
			},
			wantErr: "operator must be specified",
		},
		"exit code matcher with no values": {
			policy: &api.RetryPolicy{
				Name: "p1",
				Rules: []*api.RetryRule{
					{
						Action: api.RetryAction_RETRY_ACTION_RETRY,
						OnExitCodes: &api.RetryExitCodeMatcher{
							Operator: api.ExitCodeOperator_EXIT_CODE_OPERATOR_IN,
						},
					},
				},
			},
			wantErr: "values must not be empty",
		},
		"invalid termination message pattern": {
			policy: &api.RetryPolicy{
				Name: "p1",
				Rules: []*api.RetryRule{
					{
						Action:                      api.RetryAction_RETRY_ACTION_RETRY,
						OnTerminationMessagePattern: "[unclosed",
					},
				},
			},
			wantErr: "not a valid regular expression",
		},
		"error names the offending rule index": {
			policy: &api.RetryPolicy{
				Name: "p1",
				Rules: []*api.RetryRule{
					validRule(),
					{Action: api.RetryAction_RETRY_ACTION_RETRY},
				},
			},
			wantErr: "rule 1",
		},
		"valid policy with default action only": {
			policy: &api.RetryPolicy{
				Name:          "p1",
				DefaultAction: api.RetryAction_RETRY_ACTION_FAIL,
			},
			wantErrNone: true,
		},
		"valid policy with every matcher type": {
			policy: &api.RetryPolicy{
				Name:          "my-policy-1",
				RetryLimit:    3,
				DefaultAction: api.RetryAction_RETRY_ACTION_FAIL,
				Rules: []*api.RetryRule{
					validRule(),
					{
						Action: api.RetryAction_RETRY_ACTION_RETRY,
						OnExitCodes: &api.RetryExitCodeMatcher{
							Operator: api.ExitCodeOperator_EXIT_CODE_OPERATOR_NOT_IN,
							Values:   []int32{0, 137},
						},
					},
					{
						Action:                      api.RetryAction_RETRY_ACTION_FAIL,
						OnTerminationMessagePattern: "disk quota exceeded.*",
					},
					{
						Action:        api.RetryAction_RETRY_ACTION_RETRY,
						OnCategory:    "infrastructure",
						OnSubcategory: "node-failure",
					},
				},
			},
			wantErrNone: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := ValidatePolicy(tc.policy)
			if tc.wantErrNone {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
