package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gogo/protobuf/types"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/armadaproject/armada/internal/common/armadacontext"
	"github.com/armadaproject/armada/pkg/api"
)

// stubRetryPolicyClient satisfies api.RetryPolicyServiceClient for cache
// tests. Only GetRetryPolicies is exercised by the cache.
type stubRetryPolicyClient struct {
	policies []*api.RetryPolicy
	err      error
}

func (s *stubRetryPolicyClient) GetRetryPolicies(context.Context, *api.RetryPolicyListRequest, ...grpc.CallOption) (*api.RetryPolicyList, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &api.RetryPolicyList{RetryPolicies: s.policies}, nil
}

func (s *stubRetryPolicyClient) CreateRetryPolicy(context.Context, *api.RetryPolicy, ...grpc.CallOption) (*types.Empty, error) {
	return nil, errors.New("not implemented")
}

func (s *stubRetryPolicyClient) UpdateRetryPolicy(context.Context, *api.RetryPolicy, ...grpc.CallOption) (*types.Empty, error) {
	return nil, errors.New("not implemented")
}

func (s *stubRetryPolicyClient) DeleteRetryPolicy(context.Context, *api.RetryPolicyDeleteRequest, ...grpc.CallOption) (*types.Empty, error) {
	return nil, errors.New("not implemented")
}

func (s *stubRetryPolicyClient) GetRetryPolicy(context.Context, *api.RetryPolicyGetRequest, ...grpc.CallOption) (*api.RetryPolicy, error) {
	return nil, errors.New("not implemented")
}

func TestApiPolicyCache_RefreshMetrics(t *testing.T) {
	ctx := armadacontext.Background()
	client := &stubRetryPolicyClient{err: errors.New("api unavailable")}
	cache := NewApiPolicyCache(client, time.Minute)

	// The metrics are package-level (registered once in the default registry),
	// so assert on deltas rather than absolute values.
	failuresBefore := testutil.ToFloat64(cacheRefreshFailuresCounter)
	gaugeBefore := testutil.ToFloat64(cacheLastSuccessfulRefreshGauge)

	require.Error(t, cache.Initialise(ctx))
	assert.Equal(t, failuresBefore+1, testutil.ToFloat64(cacheRefreshFailuresCounter),
		"failed refresh must bump the failure counter")
	assert.Equal(t, gaugeBefore, testutil.ToFloat64(cacheLastSuccessfulRefreshGauge),
		"failed refresh must not touch the last-success gauge")

	client.err = nil
	client.policies = []*api.RetryPolicy{
		{Name: "test", DefaultAction: api.RetryAction_RETRY_ACTION_RETRY},
	}
	before := time.Now()
	require.NoError(t, cache.Initialise(ctx))
	assert.Equal(t, failuresBefore+1, testutil.ToFloat64(cacheRefreshFailuresCounter),
		"successful refresh must not bump the failure counter")
	lastSuccess := testutil.ToFloat64(cacheLastSuccessfulRefreshGauge)
	assert.GreaterOrEqual(t, lastSuccess, float64(before.Unix()),
		"successful refresh must set the last-success gauge to the current time")
	assert.LessOrEqual(t, lastSuccess, float64(time.Now().Unix()+1))

	policy, ok := cache.Get("test")
	require.True(t, ok)
	assert.Equal(t, "test", policy.Name)
}
