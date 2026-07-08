package retry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/armadaproject/armada/pkg/api"
)

func TestConvertPolicy_RoundTripAllMatchTypes(t *testing.T) {
	proto := &api.RetryPolicy{
		Name:          "policy-1",
		RetryLimit:    5,
		DefaultAction: api.RetryAction_RETRY_ACTION_FAIL,
		Rules: []*api.RetryRule{
			{
				Action:        api.RetryAction_RETRY_ACTION_RETRY,
				OnCategory:    "transient",
				OnSubcategory: "node-failure",
			},
		},
	}

	policy, err := ConvertPolicy(proto)
	require.NoError(t, err)
	require.NotNil(t, policy)

	assert.Equal(t, "policy-1", policy.Name)
	assert.Equal(t, uint32(5), policy.RetryLimit)
	assert.Equal(t, ActionFail, policy.DefaultAction)
	require.Len(t, policy.Rules, 1)

	// Category rule.
	assert.Equal(t, ActionRetry, policy.Rules[0].Action)
	assert.Equal(t, "transient", policy.Rules[0].OnCategory)
	assert.Equal(t, "node-failure", policy.Rules[0].OnSubcategory)
}

func TestConvertPolicy_EmptyFieldsRemainEmpty(t *testing.T) {
	// Smoke test guarding against a common nil-vs-empty bug: proto3 omits
	// scalar zero values, so an unset on_subcategory deserialises as "" and
	// must remain "" on the engine Rule (not nil, not "<nil>").
	proto := &api.RetryPolicy{
		Name:          "empty-fields",
		RetryLimit:    1,
		DefaultAction: api.RetryAction_RETRY_ACTION_RETRY,
		Rules: []*api.RetryRule{
			{
				Action:     api.RetryAction_RETRY_ACTION_RETRY,
				OnCategory: "kubernetes",
				// OnSubcategory and other fields intentionally left zero.
			},
		},
	}

	policy, err := ConvertPolicy(proto)
	require.NoError(t, err)
	require.Len(t, policy.Rules, 1)
	rule := policy.Rules[0]
	assert.Equal(t, "kubernetes", rule.OnCategory)
	assert.Equal(t, "", rule.OnSubcategory)
}

func TestConvertPolicy_UnknownAction(t *testing.T) {
	proto := &api.RetryPolicy{
		Name:          "unspecified",
		RetryLimit:    1,
		DefaultAction: api.RetryAction_RETRY_ACTION_UNSPECIFIED,
		Rules:         nil,
	}
	_, err := ConvertPolicy(proto)
	require.Error(t, err)
	// We refuse RETRY_ACTION_UNSPECIFIED rather than treating it as a default,
	// otherwise a truncated proto could silently retry every error.
	assert.Contains(t, err.Error(), "unknown action")
}

func TestConvertPolicy_NilProto(t *testing.T) {
	policy, err := ConvertPolicy(nil)
	assert.Nil(t, policy)
	require.Error(t, err)
}
