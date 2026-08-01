# Failover needs a plan that can hold more than one candidate

Status: resolved 2026-08-02
Deferred: 2026-08-02
Attempted at: `e04f405`
Design: `docs/design/02-architecture.md` §12 and the automatic-fallback rule

## What was attempted

Multi-candidate failover: a RouteSet naming a second upstream, an attempt
policy permitting a second try, and a pipeline loop that uses it only while
nothing has reached the client.

Three of the four pieces were straightforward and worked:

- `RouteSet.Fallback`, a closed policy of `disabled` and
  `pre_first_byte_idempotent_only`, validated so that allowing a fallback with
  one candidate is refused — a policy that promises what the plan cannot do;
- the decision itself: policy allows it, a candidate remains, the request's
  replay class permits sending it again, the failure is one another candidate
  could answer, nothing has been committed downstream, and no tool call is
  open. Every one of these has to hold at once, because a timeout, a 429, or a
  5xx does not prove the upstream did not process the request;
- the attempt loop, with the CommitLedger outside it so commits accumulate
  across the logical Exchange while everything a candidate decides does not.

## What blocked it

The Access plan compiler is single-profile from the inside. `compile` resolves
one profile, one target, one account, one transport plan, one model policy and
one codec plan, and `AccessPlanSnapshot` holds exactly one compiled target. A
second candidate reaches selection and finds no target to resolve.

Making that work is not a change to failover. It is a rework of what a
compiled Access plan is: per-candidate compiled targets, transports, model
policies and codec plans, each frozen at the same revision, with the plan hash
covering all of them. That is a plan-shape change with its own migration and
its own invariants, and it belongs in a slice of its own rather than being
carried in on the back of this one.

## What was done instead, and then

The work was reverted rather than left half-built. A loop that cannot reach a
second candidate is a feature in name only, and this repository has spent this
whole effort refusing that trade.

The prerequisite was then built on its own at `499b597`: the compiler resolves
every RouteSet candidate in the order the plan declares them, each with its own
frozen target, codec plan and transport plan, each checked the way the first
always was. Candidate zero stayed the plan's own, so every accessor returned
what it always had and a one-candidate plan hashed to the same value.

Failover followed on top of it, and this note is closed. The rule it had to
keep true is still what it was: once bytes have reached the client the answer
is committed and must not be sent again.
