package internaltypes

import (
	"fmt"
	"sync/atomic"

	v1 "k8s.io/api/core/v1"

	"github.com/armadaproject/armada/internal/common/types"
	"github.com/armadaproject/armada/internal/common/util"
	"github.com/armadaproject/armada/internal/scheduler/schedulerobjects"
)

type NodeFactory struct {
	// Taint keys that to create indexes for.
	// Should include taints frequently used for scheduling.
	// Since the NodeDb can efficiently sort out nodes with taints not tolerated
	// by a pod when looking for a node a pod can be scheduled on.
	//
	// If not set, all taints are indexed.
	indexedTaints map[string]bool

	// Node labels to create indexes for.
	// Should include node labels frequently used for scheduling.
	// Since the NodeDb can efficiently sort out nodes for which these labels
	// do not match pod node selectors when looking for a node a pod can be scheduled on.
	//
	// If not set, no labels are indexed.
	indexedNodeLabels map[string]bool

	// Allowed priorities, includes home and away (from config)
	allowedPriorities []int32

	// Factory for internaltypes.ResourceList
	resourceListFactory *ResourceListFactory

	// Used for assigning node index
	nodeIndexCounter atomic.Uint64
}

func NewNodeFactory(
	indexedTaints []string,
	indexedNodeLabels []string,
	priorityClasses map[string]types.PriorityClass,
	resourceListFactory *ResourceListFactory,
) *NodeFactory {
	return &NodeFactory{
		indexedTaints:       util.StringListToSet(indexedTaints),
		indexedNodeLabels:   util.StringListToSet(indexedNodeLabels),
		allowedPriorities:   types.AllowedPriorities(priorityClasses),
		resourceListFactory: resourceListFactory,
		nodeIndexCounter:    atomic.Uint64{},
	}
}

func (f *NodeFactory) CreateNodeAndType(
	id string,
	executor string,
	name string,
	pool string,
	reportingNodeType string,
	unschedulable bool,
	taints []v1.Taint,
	labels map[string]string,
	totalResources ResourceList,
	allocatableResources ResourceList,
	allocatableByPriority map[int32]ResourceList,
) *Node {
	return CreateNodeAndType(
		id,
		f.allocateNodeIndex(),
		executor,
		name,
		pool,
		reportingNodeType,
		unschedulable,
		taints,
		labels,
		f.indexedTaints,
		f.indexedNodeLabels,
		totalResources,
		allocatableResources,
		allocatableByPriority,
	)
}

func (f *NodeFactory) FromSchedulerObjectsNode(node *schedulerobjects.Node) *Node {
	return FromSchedulerObjectsNode(node,
		f.allocateNodeIndex(),
		f.indexedTaints,
		f.indexedNodeLabels,
		f.allowedPriorities,
		f.resourceListFactory,
	)
}

func (f *NodeFactory) FromSchedulerObjectsExecutors(executors []*schedulerobjects.Executor, errorLogger func(string)) []*Node {
	result := []*Node{}
	for _, executor := range executors {
		for _, node := range executor.GetNodes() {
			if executor.Id != node.Executor {
				errorLogger(fmt.Sprintf("Executor name mismatch: %q != %q", node.Executor, executor.Id))
				continue
			}
			result = append(result, f.FromSchedulerObjectsNode(node))
		}
	}
	return result
}

func (f *NodeFactory) ResourceListFactory() *ResourceListFactory {
	return f.resourceListFactory
}

func (f *NodeFactory) AddLabels(nodes []*Node, extraLabels map[string]string) []*Node {
	result := make([]*Node, len(nodes))
	for i, node := range nodes {
		newLabels := util.MergeMaps(node.GetLabels(), extraLabels)
		result[i] = CreateNodeAndType(node.GetId(),
			node.GetIndex(),
			node.GetExecutor(),
			node.GetName(),
			node.GetPool(),
			node.GetReportingNodeType(),
			false,
			node.GetTaints(),
			newLabels,
			f.indexedTaints,
			f.indexedNodeLabels,
			node.GetTotalResources(),
			node.GetAllocatableResources(),
			node.AllocatableByPriority,
		)
	}
	return result
}

func (f *NodeFactory) AddTaints(nodes []*Node, extraTaints []v1.Taint) []*Node {
	result := make([]*Node, len(nodes))
	for i, node := range nodes {
		result[i] = CreateNodeAndType(node.GetId(),
			node.GetIndex(),
			node.GetExecutor(),
			node.GetName(),
			node.GetPool(),
			node.GetReportingNodeType(),
			false,
			append(node.GetTaints(), extraTaints...),
			node.GetLabels(),
			f.indexedTaints,
			f.indexedNodeLabels,
			node.GetTotalResources(),
			node.GetAllocatableResources(),
			node.AllocatableByPriority,
		)
	}
	return result
}

// MarkAllSchedulable rebuilds nodes so they are treated as schedulable, clearing the unschedulable
// flag and stripping cordon taints (see IsCordonTaint). It is used by the submit checker so that
// jobs targeting a node type whose nodes are all cordoned (via `kubectl cordon` or `armadactl
// cordon`) stay queued rather than being rejected. See issue #4946.
//
// Like AddTaints/AddLabels, this rebuilds via CreateNodeAndType so NodeType (used for nodeDb
// indexing) is recomputed from the reduced taint set and stays consistent.
func (f *NodeFactory) MarkAllSchedulable(nodes []*Node) []*Node {
	result := make([]*Node, len(nodes))
	for i, node := range nodes {
		taints := node.GetTaints()
		cordoned := node.IsUnschedulable()
		if !cordoned {
			for _, taint := range taints {
				if IsCordonTaint(taint) {
					cordoned = true
					break
				}
			}
		}
		if !cordoned {
			// Common case. Nodes are immutable, so reuse the node and skip recomputing its NodeType.
			result[i] = node
			continue
		}
		nonCordonTaints := make([]v1.Taint, 0, len(taints))
		for _, taint := range taints {
			if !IsCordonTaint(taint) {
				nonCordonTaints = append(nonCordonTaints, taint)
			}
		}
		result[i] = CreateNodeAndType(node.GetId(),
			node.GetIndex(),
			node.GetExecutor(),
			node.GetName(),
			node.GetPool(),
			node.GetReportingNodeType(),
			false,
			nonCordonTaints,
			node.GetLabels(),
			f.indexedTaints,
			f.indexedNodeLabels,
			node.GetTotalResources(),
			node.GetAllocatableResources(),
			node.AllocatableByPriority,
		)
	}
	return result
}

func (f *NodeFactory) allocateNodeIndex() uint64 {
	return f.nodeIndexCounter.Add(1)
}
