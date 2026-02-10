package retry

import (
	"github.com/sirupsen/logrus"

	"github.com/armadaproject/armada/internal/scheduler/configuration"
)

// PolicyResolver resolves the retry policy for a given queue.
type PolicyResolver struct {
	config configuration.RetryPolicyConfig
	log    *logrus.Entry
}

// NewPolicyResolver creates a new PolicyResolver.
// It pre-compiles all regex patterns in the configuration for better performance.
func NewPolicyResolver(config configuration.RetryPolicyConfig) (*PolicyResolver, error) {
	resolver := &PolicyResolver{
		config: config,
		log:    logrus.WithField("component", "PolicyResolver"),
	}

	// Pre-compile regex patterns for better performance
	if err := resolver.compileRegexPatterns(); err != nil {
		return nil, err
	}

	return resolver, nil
}

// compileRegexPatterns pre-compiles all regex patterns in the configuration.
func (r *PolicyResolver) compileRegexPatterns() error {
	// Compile patterns in default policy
	if err := compileRulesRegex(r.config.Default.Rules); err != nil {
		return err
	}

	// Compile patterns in named policies
	for name, policy := range r.config.Policies {
		if err := compileRulesRegex(policy.Rules); err != nil {
			r.log.WithError(err).WithField("policy", name).Error("Failed to compile regex pattern")
			return err
		}
	}

	return nil
}

// compileRulesRegex compiles regex patterns in a slice of rules.
func compileRulesRegex(rules []configuration.Rule) error {
	for i := range rules {
		if rules[i].OnTerminationMessage != nil {
			if err := rules[i].OnTerminationMessage.Compile(); err != nil {
				return err
			}
		}
	}
	return nil
}

// Enabled returns true if the retry policy system is enabled.
func (r *PolicyResolver) Enabled() bool {
	return r.config.Enabled
}

// GlobalMaxRetries returns the global maximum retries limit.
func (r *PolicyResolver) GlobalMaxRetries() uint {
	return r.config.GlobalMaxRetries
}

// GetPolicy returns the policy for the given queue.
// If the queue specifies a policy that exists, it is returned.
// If the queue specifies a policy that doesn't exist, a warning is logged and default is returned.
// If the queue doesn't specify a policy, default is returned.
func (r *PolicyResolver) GetPolicy(queueRetryPolicy string) *configuration.Policy {
	if !r.config.Enabled {
		return nil
	}

	// If queue specifies a policy, look it up
	if queueRetryPolicy != "" {
		if policy, exists := r.config.Policies[queueRetryPolicy]; exists {
			return &policy
		}
		// Policy not found - log warning and fall through to default
		r.log.WithFields(logrus.Fields{
			"policy": queueRetryPolicy,
		}).Warn("Queue references unknown retry policy, using default")
	}

	return &r.config.Default
}
