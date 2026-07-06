# Retry policies
- [Retry policies](#retry-policies)
  - [Overview](#overview)
  - [Enabling the retry engine](#enabling-the-retry-engine)
  - [Policy format](#policy-format)
    - [Matching semantics](#matching-semantics)
    - [Match fields](#match-fields)
  - [Retry budgets](#retry-budgets)
  - [Gang jobs](#gang-jobs)
  - [Pod naming](#pod-naming)
  - [Per-job opt-out](#per-job-opt-out)
  - [Managing policies with armadactl](#managing-policies-with-armadactl)
  - [Rollout guide for operators](#rollout-guide-for-operators)

## Overview

Retry policies let operators define, per queue, which job failures Armada should retry and which it should fail permanently. A retry policy is a named resource, managed through `armadactl` like a queue, and attached to one or more queues by name. When a job run fails, the scheduler looks up the policy attached to the job's queue, evaluates the policy rules against the failure, and either requeues the job for another attempt or fails it terminally.

The retry engine is off by default. It only runs when `scheduling.retryPolicy.enabled` is set to `true` in the scheduler configuration. With the flag off, or for queues with no policy attached, Armada behaves exactly as before: jobs are only re-leased on lease returns, up to the legacy attempt limit.

## Enabling the retry engine

The engine is controlled by the `retryPolicy` block under the `scheduling` section of the scheduler configuration:

```yaml
scheduling:
  retryPolicy:
    enabled: true
    globalMaxRetries: 5
```

* `enabled`: turns the engine on. Defaults to `false`.
* `globalMaxRetries`: a scheduler-wide cap on retries per job. See [Retry budgets](#retry-budgets) for the exact semantics.

Before enabling the flag, read the [rollout guide](#rollout-guide-for-operators). In particular, all executors must be upgraded before the flag is enabled anywhere.

## Policy format

Policies are written as YAML (or JSON) files and created with `armadactl`. A realistic example:

```yaml
apiVersion: armadaproject.io/v1beta1
kind: RetryPolicy
name: ml-training-retries
retryLimit: 3
defaultAction: Fail
rules:
  # Known-fatal exit codes: fail immediately, never retry.
  - action: Fail
    onExitCodes:
      operator: In
      values: [64, 78]
  # OOM kills and evictions are worth another attempt.
  - action: Retry
    onConditions: ["OOMKilled", "Evicted"]
  # Application errors whose termination message signals a transient fault.
  - action: Retry
    onConditions: ["AppError"]
    onTerminationMessagePattern: "(?i)(connection reset|timeout|temporarily unavailable)"
  # Infrastructure failures categorised by Armada.
  - action: Retry
    onCategory: internal
    onSubcategory: lease-expired
```

* `name`: unique name of the policy. Queues reference policies by this name.
* `retryLimit`: maximum number of retries after the initial failure. See [Retry budgets](#retry-budgets).
* `defaultAction`: `Retry` or `Fail`. Applied when no rule matches.
* `rules`: an ordered list of matching rules, each with an `action` (`Retry` or `Fail`) and one or more match fields.

### Matching semantics

* Within a single rule, all match fields that are set must match. Matchers are ANDed: a rule with both `onConditions` and `onTerminationMessagePattern` only applies when the failure matches both.
* Rules are evaluated top to bottom and the first matching rule wins. Later rules are not consulted.
* If no rule matches, `defaultAction` decides.

Order rules from most specific to most general. A common pattern is to put `Fail` rules for known-fatal errors first, followed by `Retry` rules for transient errors, with `defaultAction: Fail` as the safety net.

### Match fields

* `onConditions`: a list of failure conditions. The rule matches if the failure's condition is any of the listed values (values within the list are ORed). Available conditions: `OOMKilled`, `Evicted`, `DeadlineExceeded`, `AppError` (catch-all for container failures with no more specific condition), `Preempted`, `LeaseReturned`.
* `onExitCodes`: matches the exit code of the first failed container. `operator` is `In` or `NotIn`, `values` is a list of exit codes.
* `onTerminationMessagePattern`: an RE2 regular expression matched against the container's termination message (the contents of `/dev/termination-log`, or container logs if the pod uses the `FallbackToLogsOnError` termination message policy).
* `onCategory` and `onSubcategory`: match the failure category Armada assigned to the error, for example `internal` / `lease-expired`. `onSubcategory` narrows a category match and is only valid together with `onCategory`.

## Retry budgets

Two limits bound how often a job is retried: the per-policy `retryLimit` and the scheduler-wide `globalMaxRetries`.

* `retryLimit` counts retries, not attempts. `retryLimit: 3` allows 3 retries after the initial failure, so 4 total attempts before the job fails terminally.
* Preemptions never consume the failure budget. A preempted run does not count against `retryLimit`, and retries triggered by a `Preempted` rule are tracked with their own separate tally. A job that is preempted and retried can still use its full failure budget afterwards.
* `globalMaxRetries` is a scheduler-wide cap that applies on top of every policy. Unlike `retryLimit`, it counts every extra run a job gets, including legacy lease returns, so it bounds total scheduler work per job regardless of what individual policies allow.
* `globalMaxRetries: 0` disables all engine retries. This is the kill switch: with the cap at zero the engine never grants a retry, whatever the policies say.
* There is no unlimited setting for the global cap. Every deployment with the engine enabled has a finite scheduler-wide bound on retries per job.

## Gang jobs

Gang jobs are excluded from the retry engine in this version. Retrying a gang atomically requires aggregating failures across all members and restarting them together, which is not yet implemented. A job that is part of a gang with cardinality 2 or more is never retried by the engine, even if a policy rule matches. When that happens, the scheduler increments the `retry_policy_gang_skipped_total` metric and logs at info level, so you can see how often policies would have applied to gangs.

Gangs with cardinality 1 are treated as plain jobs and do retry.

Gang retry support is tracked in [armadaproject/armada#4683](https://github.com/armadaproject/armada/issues/4683).

## Pod naming

Without the retry engine, every pod for a job is named `armada-<jobId>-0`. With the engine enabled, first attempts keep that name, and retried attempts get the run index appended: the first retry runs as `armada-<jobId>-0-1`, the second as `armada-<jobId>-0-2`, and so on. This guarantees a retried pod never collides with its still-terminating predecessor on the same cluster.

Anything that embeds the pod name will see the new format for retried attempts. In particular, ingress hostnames that are derived from pod names differ between the first attempt and retried attempts. Workloads that rely on a stable, predictable hostname across attempts should derive it from the job id rather than the pod name.

Each pod also carries an `armada_job_run_index` label with its run index, which workloads can read through the downward API to detect whether they are a retry.

## Per-job opt-out

Jobs submitted with `failFast: true` (the `armadaproject.io/failFast` annotation) bypass the retry engine entirely. A fail-fast job fails terminally on its first failure regardless of the queue's retry policy. Use this for workloads where a repeated attempt is wasted work, for example jobs that are resubmitted by an external workflow engine with its own retry logic.

## Managing policies with armadactl

Create a policy from a file:

```bash
armadactl create retry-policy -f retry-policy.yaml
```

Update an existing policy in place. The change takes effect for failures evaluated after the scheduler's policy cache refreshes:

```bash
armadactl update retry-policy -f retry-policy.yaml
```

Inspect policies:

```bash
armadactl get retry-policy ml-training-retries
armadactl get retry-policies
```

Attach a policy to a queue at creation time, or to an existing queue:

```bash
armadactl create queue my-queue --retry-policy ml-training-retries
armadactl update queue my-queue --retry-policy ml-training-retries
```

Delete a policy:

```bash
armadactl delete retry-policy ml-training-retries
```

Deletion is rejected while any queue still references the policy. Detach it from all queues first, then delete it.

## Rollout guide for operators

**Upgrade all executors before enabling the flag anywhere.** The scheduler tells the executor which run index a lease is for, and upgraded executors use it to build the retry-safe pod name. Executors running an older version ignore the run index and reuse the legacy pod name `armada-<jobId>-0` for retried runs. The new pod then collides with its terminating predecessor, the run fails again, and each collision silently burns retry budget until the job exhausts it. There is no error that points at the version skew, so complete the executor rollout on every cluster before setting `scheduling.retryPolicy.enabled: true` on any scheduler.

**Disabling the flag mid-flight is safe.** Jobs that were already retried keep running. New failures fall back to legacy behaviour: retried runs go back to legacy pod naming and the legacy attempt limit applies instead of policy budgets. No job state is lost.

**Metrics to alert on:**

* Policy cache refresh failures and cache staleness. The scheduler periodically refreshes policies from the API. If the API is down the cache goes stale and, once it expires, jobs on policy-holding queues silently fall back to legacy retry behaviour. An outage here disables retries without any job-visible error, so this is the most important alert.
* Invalid-policy skips. A policy that fails validation (for example a bad regular expression) is skipped at cache refresh and the queues referencing it fall back to legacy behaviour.
* Gang skips (`retry_policy_gang_skipped_total`). A steadily growing count means users are attaching retry policies to queues that run gangs and expecting retries that never happen.
* Retry decision counters. Track the retry and fail decision rates per queue to spot policies that retry far more (or less) than intended, and to catch jobs burning through budgets on hopeless failures.
