package retrypolicy

import (
	"context"
	"fmt"
	"strings"

	"github.com/gogo/protobuf/types"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/armadaproject/armada/internal/common/armadacontext"
	"github.com/armadaproject/armada/internal/common/armadaerrors"
	"github.com/armadaproject/armada/internal/common/auth"
	"github.com/armadaproject/armada/internal/server/permissions"
	"github.com/armadaproject/armada/pkg/api"
	"github.com/armadaproject/armada/pkg/client/queue"
)

// QueueLister supplies the queues checked before a retry policy is deleted.
// Satisfied by both queue.PostgresQueueRepository and queue.CachedQueueRepository.
type QueueLister interface {
	GetAllQueues(ctx *armadacontext.Context) ([]queue.Queue, error)
}

type Server struct {
	repository  RetryPolicyRepository
	queueLister QueueLister
	authorizer  auth.ActionAuthorizer
}

func NewServer(repository RetryPolicyRepository, queueLister QueueLister, authorizer auth.ActionAuthorizer) *Server {
	return &Server{
		repository:  repository,
		queueLister: queueLister,
		authorizer:  authorizer,
	}
}

func (s *Server) CreateRetryPolicy(grpcCtx context.Context, req *api.RetryPolicy) (*types.Empty, error) {
	ctx := armadacontext.FromGrpcCtx(grpcCtx)
	err := s.authorizer.AuthorizeAction(ctx, permissions.CreateRetryPolicy)
	var ep *armadaerrors.ErrUnauthorized
	if errors.As(err, &ep) {
		return nil, status.Errorf(codes.PermissionDenied, "error creating retry policy %s: %s", req.Name, ep)
	} else if err != nil {
		return nil, status.Errorf(codes.Unavailable, "error checking permissions: %s", err)
	}

	// Reject invalid policies up front. The scheduler validates policies when
	// loading them and silently drops bad ones, so without this check the
	// operator gets a 200 OK and only sees the policy fail to install later.
	if err := ValidatePolicy(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid retry policy: %s", err)
	}

	err = s.repository.CreateRetryPolicy(ctx, req)
	var ea *ErrRetryPolicyAlreadyExists
	if errors.As(err, &ea) {
		return nil, status.Errorf(codes.AlreadyExists, "error creating retry policy: %s", err)
	} else if err != nil {
		return nil, status.Errorf(codes.Unavailable, "error creating retry policy: %s", err)
	}

	return &types.Empty{}, nil
}

func (s *Server) UpdateRetryPolicy(grpcCtx context.Context, req *api.RetryPolicy) (*types.Empty, error) {
	ctx := armadacontext.FromGrpcCtx(grpcCtx)
	err := s.authorizer.AuthorizeAction(ctx, permissions.UpdateRetryPolicy)
	var ep *armadaerrors.ErrUnauthorized
	if errors.As(err, &ep) {
		return nil, status.Errorf(codes.PermissionDenied, "error updating retry policy %s: %s", req.Name, ep)
	} else if err != nil {
		return nil, status.Errorf(codes.Unavailable, "error checking permissions: %s", err)
	}

	if err := ValidatePolicy(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid retry policy: %s", err)
	}

	err = s.repository.UpdateRetryPolicy(ctx, req)
	var enf *ErrRetryPolicyNotFound
	if errors.As(err, &enf) {
		return nil, status.Errorf(codes.NotFound, "error: %s", err)
	} else if err != nil {
		return nil, status.Errorf(codes.Unavailable, "error updating retry policy %q: %s", req.Name, err)
	}

	return &types.Empty{}, nil
}

func (s *Server) DeleteRetryPolicy(grpcCtx context.Context, req *api.RetryPolicyDeleteRequest) (*types.Empty, error) {
	ctx := armadacontext.FromGrpcCtx(grpcCtx)
	err := s.authorizer.AuthorizeAction(ctx, permissions.DeleteRetryPolicy)
	var ep *armadaerrors.ErrUnauthorized
	if errors.As(err, &ep) {
		return nil, status.Errorf(codes.PermissionDenied, "error deleting retry policy %s: %s", req.Name, ep)
	} else if err != nil {
		return nil, status.Errorf(codes.Unavailable, "error checking permissions: %s", err)
	}

	if req.Name == "" {
		return nil, status.Errorf(codes.InvalidArgument, "retry policy name must not be empty")
	}

	// Deleting a policy that queues still reference would leave those queues
	// pointing at nothing, silently disabling their retries. Force the
	// operator to detach the policy from all queues first.
	referencing, err := s.queuesReferencingPolicy(ctx, req.Name)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "error checking queues referencing retry policy %s: %s", req.Name, err)
	}
	if len(referencing) > 0 {
		shown := referencing
		if len(shown) > maxReportedReferencingQueues {
			shown = shown[:maxReportedReferencingQueues]
		}
		return nil, status.Errorf(
			codes.FailedPrecondition,
			"retry policy %s is still referenced by %d queue(s), including: %s",
			req.Name, len(referencing), strings.Join(shown, ", "),
		)
	}

	err = s.repository.DeleteRetryPolicy(ctx, req.Name)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "error deleting retry policy %s: %s", req.Name, err)
	}
	return &types.Empty{}, nil
}

// Cap on queue names included in the FailedPrecondition message so a policy
// referenced by hundreds of queues does not produce an unreadable error.
const maxReportedReferencingQueues = 5

// queuesReferencingPolicy returns the names of all queues whose retry policy
// field matches the given policy name.
func (s *Server) queuesReferencingPolicy(ctx *armadacontext.Context, policyName string) ([]string, error) {
	queues, err := s.queueLister.GetAllQueues(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list queues: %w", err)
	}
	var names []string
	for _, q := range queues {
		if q.RetryPolicy == policyName {
			names = append(names, q.Name)
		}
	}
	return names, nil
}

// GetRetryPolicy returns a single retry policy by name.
// Read access is intentionally unauthenticated, consistent with GetQueue in queue_service.go.
func (s *Server) GetRetryPolicy(grpcCtx context.Context, req *api.RetryPolicyGetRequest) (*api.RetryPolicy, error) {
	ctx := armadacontext.FromGrpcCtx(grpcCtx)

	if req.Name == "" {
		return nil, status.Errorf(codes.InvalidArgument, "retry policy name must not be empty")
	}

	policy, err := s.repository.GetRetryPolicy(ctx, req.Name)
	var enf *ErrRetryPolicyNotFound
	if errors.As(err, &enf) {
		return nil, status.Errorf(codes.NotFound, "error: %s", err)
	} else if err != nil {
		return nil, status.Errorf(codes.Unavailable, "error getting retry policy %q: %s", req.Name, err)
	}
	return policy, nil
}

// GetRetryPolicies returns all retry policies.
// Read access is intentionally unauthenticated, consistent with GetQueue in queue_service.go.
func (s *Server) GetRetryPolicies(grpcCtx context.Context, _ *api.RetryPolicyListRequest) (*api.RetryPolicyList, error) {
	ctx := armadacontext.FromGrpcCtx(grpcCtx)
	policies, err := s.repository.GetAllRetryPolicies(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "error getting retry policies: %s", err)
	}
	return &api.RetryPolicyList{RetryPolicies: policies}, nil
}
