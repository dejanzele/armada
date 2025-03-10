package nodedb

import (
	"fmt"

	"github.com/segmentio/fasthash/fnv1a"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/armadaproject/armada/internal/scheduler/internaltypes"
	schedulercontext "github.com/armadaproject/armada/internal/scheduler/scheduling/context"
)

const (
	PodRequirementsNotMetReasonUnknown               = "unknown"
	PodRequirementsNotMetReasonInsufficientResources = "insufficient resources available"
)

type PodRequirementsNotMetReason interface {
	fmt.Stringer
	// Returns a 64-bit hash of this reason.
	Sum64() uint64
}

type UntoleratedTaint struct {
	Taint v1.Taint
}

func (r *UntoleratedTaint) Sum64() uint64 {
	h := fnv1a.Init64
	h = fnv1a.AddString64(h, "UntoleratedTaint")
	h = fnv1a.AddString64(h, r.Taint.Key)
	h = fnv1a.AddString64(h, r.Taint.Value)
	h = fnv1a.AddString64(h, string(r.Taint.Effect))
	return h
}

func (r *UntoleratedTaint) String() string {
	return fmt.Sprintf("taint %s=%s:%s not tolerated", r.Taint.Key, r.Taint.Value, r.Taint.Effect)
}

type MissingLabel struct {
	Label string
}

func (r *MissingLabel) Sum64() uint64 {
	h := fnv1a.Init64
	h = fnv1a.AddString64(h, "MissingLabel")
	h = fnv1a.AddString64(h, r.Label)
	return h
}

func (r *MissingLabel) String() string {
	return fmt.Sprintf("node does not match pod NodeSelector: label %s not set", r.Label)
}

type UnmatchedLabel struct {
	Label     string
	PodValue  string
	NodeValue string
}

func (r *UnmatchedLabel) Sum64() uint64 {
	h := fnv1a.Init64
	h = fnv1a.AddString64(h, "UnmatchedLabel")
	h = fnv1a.AddString64(h, r.Label)
	h = fnv1a.AddString64(h, r.PodValue)
	h = fnv1a.AddString64(h, r.NodeValue)
	return h
}

func (r *UnmatchedLabel) String() string {
	return fmt.Sprintf("node does not match pod NodeSelector: required label %s = %s, but node has %s", r.Label, r.PodValue, r.NodeValue)
}

type UnmatchedNodeSelector struct {
	NodeSelector *v1.NodeSelector
}

func (r *UnmatchedNodeSelector) Sum64() uint64 {
	h := fnv1a.Init64
	h = fnv1a.AddString64(h, "UnmatchedNodeSelector")
	for _, nodeSelectorTerm := range r.NodeSelector.NodeSelectorTerms {
		h = unmatchedNodeSelectorSum64AddNodeSelectorRequirements(h, nodeSelectorTerm.MatchExpressions)
		h = unmatchedNodeSelectorSum64AddNodeSelectorRequirements(h, nodeSelectorTerm.MatchFields)
	}
	return h
}

func unmatchedNodeSelectorSum64AddNodeSelectorRequirements(h uint64, nodeSelectorRequirements []v1.NodeSelectorRequirement) uint64 {
	for _, nodeSelectorRequirement := range nodeSelectorRequirements {
		h = fnv1a.AddString64(h, nodeSelectorRequirement.Key)
		h = fnv1a.AddString64(h, string(nodeSelectorRequirement.Operator))
		for _, value := range nodeSelectorRequirement.Values {
			h = fnv1a.AddString64(h, value)
		}
	}
	return h
}

func (r *UnmatchedNodeSelector) String() string {
	return fmt.Sprintf("node does not match pod NodeAffinity %s", r.NodeSelector)
}

type InsufficientResources struct {
	ResourceName string
	Required     resource.Quantity
	Available    resource.Quantity
}

func (r *InsufficientResources) Sum64() uint64 {
	h := fnv1a.Init64
	h = fnv1a.AddString64(h, r.ResourceName)
	h = fnv1a.AddUint64(h, uint64(r.Required.MilliValue()))
	h = fnv1a.AddUint64(h, uint64(r.Available.MilliValue()))
	return h
}

func (err *InsufficientResources) String() string {
	return "pod requires " + err.Required.String() + " " + err.ResourceName + ", but only " +
		err.Available.String() + " is available"
}

// NodeTypeJobRequirementsMet determines whether a pod can be scheduled on nodes of this NodeType.
// If the requirements are not met, it returns the reason for why.
// If the requirements can't be parsed, an error is returned.
func NodeTypeJobRequirementsMet(nodeType *internaltypes.NodeType, jctx *schedulercontext.JobSchedulingContext) (bool, PodRequirementsNotMetReason) {
	matches, reason := TolerationRequirementsMet(nodeType, jctx.AdditionalTolerations, jctx.PodRequirements.Tolerations)
	if !matches {
		return matches, reason
	}

	matches, reason = NodeSelectorRequirementsMet(nodeType.GetLabelValue, nodeType.GetUnsetIndexedLabelValue, jctx.AdditionalNodeSelectors)
	if !matches {
		return matches, reason
	}

	return NodeSelectorRequirementsMet(nodeType.GetLabelValue, nodeType.GetUnsetIndexedLabelValue, jctx.PodRequirements.NodeSelector)
}

// JobRequirementsMet determines whether a job can be scheduled onto this node.
// If the pod can be scheduled, the returned score indicates how well the node fits:
// - 0: Pod can be scheduled by preempting running pods.
// - 1: Pod can be scheduled without preempting any running pods.
// If the requirements are not met, it returns the reason why.
// If the requirements can't be parsed, an error is returned.
func JobRequirementsMet(node *internaltypes.Node, priority int32, jctx *schedulercontext.JobSchedulingContext) (bool, PodRequirementsNotMetReason, error) {
	matches, reason, err := StaticJobRequirementsMet(node, jctx)
	if !matches || err != nil {
		return matches, reason, err
	}
	matches, reason = DynamicJobRequirementsMet(node.AllocatableByPriority[priority], jctx)
	if !matches {
		return matches, reason, nil
	}
	return true, nil, nil
}

// StaticJobRequirementsMet checks if a job can be scheduled onto this node,
// accounting for taints, node selectors, node affinity, and total resources available on the node.
func StaticJobRequirementsMet(node *internaltypes.Node, jctx *schedulercontext.JobSchedulingContext) (bool, PodRequirementsNotMetReason, error) {
	matches, reason := NodeTolerationRequirementsMet(node, jctx.AdditionalTolerations, jctx.PodRequirements.Tolerations)
	if !matches {
		return matches, reason, nil
	}

	matches, reason = NodeSelectorRequirementsMet(node.GetLabelValue, nil, jctx.AdditionalNodeSelectors)
	if !matches {
		return matches, reason, nil
	}

	matches, reason = NodeSelectorRequirementsMet(node.GetLabelValue, nil, jctx.PodRequirements.NodeSelector)
	if !matches {
		return matches, reason, nil
	}

	matches, reason, err := NodeAffinityRequirementsMet(node, jctx.PodRequirements.GetAffinityNodeSelector())
	if !matches || err != nil {
		return matches, reason, err
	}

	// We are using total resource here, as it is an optimistic initial check
	// Ideally we'd use allocatable, however allocatable could be quite dynamic and have an adverse impact on the SubmitCheck
	matches, reason = resourceRequirementsMet(node.GetTotalResources(), jctx.KubernetesResourceRequirements)
	if !matches {
		return matches, reason, nil
	}

	return true, nil, nil
}

// DynamicJobRequirementsMet checks if a pod can be scheduled onto this node,
// accounting for resources allocated to pods already assigned to this node.
func DynamicJobRequirementsMet(allocatableResources internaltypes.ResourceList, jctx *schedulercontext.JobSchedulingContext) (bool, PodRequirementsNotMetReason) {
	matches, reason := resourceRequirementsMet(allocatableResources, jctx.KubernetesResourceRequirements)
	return matches, reason
}

func TolerationRequirementsMet(nodeType *internaltypes.NodeType, tolerations ...[]v1.Toleration) (bool, PodRequirementsNotMetReason) {
	untoleratedTaint, hasUntoleratedTaint := nodeType.FindMatchingUntoleratedTaint(tolerations...)
	if hasUntoleratedTaint {
		return false, &UntoleratedTaint{Taint: untoleratedTaint}
	}
	return true, nil
}

func NodeTolerationRequirementsMet(node *internaltypes.Node, tolerations ...[]v1.Toleration) (bool, PodRequirementsNotMetReason) {
	untoleratedTaint, hasUntoleratedTaint := node.FindMatchingUntoleratedTaint(tolerations...)
	if hasUntoleratedTaint {
		return false, &UntoleratedTaint{Taint: untoleratedTaint}
	}
	return true, nil
}

func NodeSelectorRequirementsMet(nodeLabelGetter func(string) (string, bool), unsetIndexedLabelGetter func(string) (string, bool), nodeSelector map[string]string) (bool, PodRequirementsNotMetReason) {
	for label, podValue := range nodeSelector {
		// If the label value differs between nodeLabels and the pod, always return false.
		if nodeValue, ok := nodeLabelGetter(label); ok {
			if nodeValue != podValue {
				return false, &UnmatchedLabel{
					Label:     label,
					PodValue:  podValue,
					NodeValue: nodeValue,
				}
			}
		} else {
			// If unsetIndexedLabels is provided, return false only if this label is explicitly marked as not set.
			//
			// If unsetIndexedLabels is not provided, we assume that nodeLabels contains all labels and return false.
			if unsetIndexedLabelGetter != nil {
				if _, ok := unsetIndexedLabelGetter(label); ok {
					return false, &MissingLabel{Label: label}
				}
			} else {
				return false, &MissingLabel{Label: label}
			}
		}
	}
	return true, nil
}

func NodeAffinityRequirementsMet(node *internaltypes.Node, nodeSelector *v1.NodeSelector) (bool, PodRequirementsNotMetReason, error) {
	if nodeSelector != nil {
		matchesNodeSelector, err := node.MatchNodeSelectorTerms(nodeSelector)
		if err != nil {
			return false, nil, err
		}
		if !matchesNodeSelector {
			return false, &UnmatchedNodeSelector{
				NodeSelector: nodeSelector,
			}, nil
		}
	}
	return true, nil, nil
}

func resourceRequirementsMet(available internaltypes.ResourceList, required internaltypes.ResourceList) (bool, PodRequirementsNotMetReason) {
	resourceName, availableQuantity, requiredQuantity, hasGreaterResource := required.ExceedsAvailable(available)
	if hasGreaterResource {
		return false, &InsufficientResources{
			ResourceName: resourceName,
			Required:     requiredQuantity,
			Available:    availableQuantity,
		}
	}
	return true, nil
}
