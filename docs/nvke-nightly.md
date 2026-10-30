# Nightly NVKR builds and NVKE end-to-end tests

The authored [GitHub workflow](../.github/workflows/nvkr-nightly.yml) runs daily at
03:17 UTC and supports manual dispatch. It resolves NVKE `main` once, dispatches
the existing generated NVKR producer, and tests the artifacts that producer
built and published. The first maintained runtime target is NVIDIA OVN on the
native ARM64 `arc-arm64-kvm` runner.

The four coordinator jobs select a candidate, dispatch and join its producer,
select a completed candidate, and run its native Go E2E test:

1. Pass the cycle's exact NVKE commit to the existing
   `nvkr-run-nvke-nightly-arm64.yml` producer. Its admitted runtime source policy
   selects that commit for every source-built NVKE component, including disk
   materials and the installer. Independent dependency pins retain their owners.
2. Build the Definition's 34 outputs through the generated producer's existing
   execution, private transport, publication, signing and verification stages.
   Runtime preparation and finalization bind the same frozen source selection;
   checked-in catalog pins need no nightly version-bump commit.
3. Retrieve the exact producer Run, frozen Plan and aggregate publication locator.
   `PrepareExactInputs` and `VerifyExactInputs` authenticate the selected closure
   under its signing policy. The native consumer requires the exact-input receipt
   and producing invocation, and rejects a Plan with a different cycle commit.
4. Run `TestNightlyPublishedCandidate`. Existing Go owners extract the authenticated
   inputs and invoke the built `nvkectl bootstrap site`, using a temporary
   kind/Lima backend and a real QEMU/KVM site-controller VM.
5. Check the live Kubernetes API `/readyz`, the identified Ready node and native
   VM evidence, then stop and join owned VM, backend and network cleanup.

Scheduled runs and candidate validation additionally create a tenant and check
workload and DNS. Manual main runs default to those checks and may explicitly
disable them; such a run cannot supply the full candidate-validation verdict.
The nightly consumer builds no product artifacts. The separate local
`TestEndToEnd` test retains its build-and-test developer flow; the nightly does
not invoke it. Published native conformance remains unsupported.

## Validate pipeline changes before merging

The same coordinator and generated producer admit one additional exact branch,
`nvkr-validation`. An operator approves the exact current PR commit before moving
that automation-owned branch. It is not an automatic untrusted-PR execution path.
Run a reviewed operator-helper revision from a trusted operator checkout; never
trust a helper solely because it arrived with a new candidate. For the initial
rollout, review and pin this PR's helper revision to establish that trusted copy.
Merging the helper is not a prerequisite:

```sh
bash scripts/nightly-validate.sh --approve PR_NUMBER EXACT_40_CHARACTER_COMMIT /absolute/fresh/output
```

GitHub supports dispatching an already registered workflow on another branch
([manual workflow runs](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manually-run-a-workflow)).
The candidate therefore runs its own coordinator and generated code before merge.

Passing `--approve` trusts that exact commit with source access, private registry
publication/signing, Docker, sudo and the KVM runner. The operator's GitHub
credential needs repository and workflow-write access; it stays in the operator
process and is never passed into the dispatched workflow. The command targets
`dsx-ai-factory/nvkr` explicitly, regardless of the local `origin` remote.

Before promotion it checks the current open, non-draft PR head and its exact
`copy-pr-bot` mirror. An atomic GitHub reference update acquires a single
automation-owned lock and replaces the candidate branch only if its previously
observed commit still matches. It does not update `main`, the PR branch or the
mirror. Both candidate refs are reserved for this workflow, not human development.
Existing candidate runs, including waiting deployments, prevent another promotion.
The helper also requires the main nightly coordinator to be idle before promotion
and checks it again before dispatch. Main and candidate coordinators share the
deployed `nvkr-nightly-main` workflow concurrency group, so they cannot reach the
native runtime slot concurrently. Other main-branch workflows do not block this
admission. GitHub's default single-pending queue can replace a queued invocation;
there is no FIFO guarantee. Such cancellation fails the exact validation and
retains its lease for explicit recovery rather than accepting another run.
Ref-write restrictions protect candidate code, not workflow-dispatch access.
Another repository writer can still dispatch this workflow and displace a pending
run before any job guard or environment approval executes. Restricting candidate
dispatch alone would not prevent main dispatches from sharing that queue.
[GitHub workflow execution policies](https://docs.github.com/en/actions/how-tos/administer/control-workflow-execution)
can restrict actors for a workflow. Selecting that actor policy and preserving
authorized main manual and scheduled execution are a separate administrator-owned
decision; this implementation does not configure such a policy or invent an actor
allowlist. Pending-run availability therefore remains a documented limitation.
The persistent lock's idle value is the existing Git checkpoint
`e74161ab5b41b89e0f4a6e9dce790664059a6d47`. That value is only a concurrency
marker; it grants no artifact or release authority. Any other value holds the
lease. The first use may create an absent lock, and no operation deletes either ref.

The command dispatches the existing nightly at that branch and joins only the
exact returned run ID. It requires successful build and runtime jobs, an exact
commit/branch/attempt verdict, tenant checks and joined cleanup, then rechecks the
PR head. A new PR commit invalidates the result. An ambiguous dispatch is never
replayed or replaced with a latest-run lookup. Cancellation requests apply only
to the returned coordinator ID; its producer retains its existing cleanup owner.
The candidate branch remains frozen while any candidate run is active or cannot
be inspected.

Only a fully verified successful cycle releases its lock automatically. A failed
or canceled coordinator can itself have an ambiguous child dispatch, so failure
retains the lock even when no run is yet visible. After a failure, crash or
ambiguous request, inspect the recorded run facts and all
candidate runs before recovering an orphaned lock:

```sh
bash scripts/nightly-validate.sh --unlock EXACT_LOCK_COMMIT /absolute/fresh/recovery-output
```

Recovery requires the previous operator process and its requests to have ended;
investigate any ambiguous dispatch before using it. An idle scan alone cannot
prove that a delayed request will never surface. Never recover automatically as
part of retrying a failed command. Recovery refuses active or unreadable candidate
runs and resets only the matching held lock to its idle marker through an exact
compare-and-swap. It does not move the candidate branch or delete a ref.
The candidate SHA is not a unique process-attempt token: recovery followed by a
retry of that same SHA cannot fence a revived older invocation. Terminate the
previous invocation before recovery; never recover concurrently with it.

One-time repository setup is required before the first invocation:

- Reserve `nvkr-validation` and `nvkr-validation-lock` for the authorized operator.
  Restrict creation and updates to that operator, admit its force updates, enforce
  the restrictions for administrators, and disable deletions for both refs.
  Review the exact commit before permitting privileged candidate code, including
  any push-triggered code. No broad force-push or deletion grant is required.
- Add the exact `nvkr-validation` branch to the `nvkr-arc` and `nvkr-release`
  deployment policies, and grant the existing private storage and runner access.
  Do not widen either policy to arbitrary PR branches.
- Keep the new `ghcr.io/dsx-ai-factory/nvkr/nightly-candidate/nvke-nightly-arm64`
  packages private/internal and unlinked from the public repository. The existing
  metadata-only package bootstrap and private-publication checks run before binary
  uploads. Existing linked destinations are not silently admitted.

The helper produces manual pre-merge evidence. It does not configure a required
GitHub check or claim that branch protections enforce this process. No successful
full candidate run or completed provider setup is recorded for this implementation.

## Inputs and access

The maintained producer uses
[`arm64-nvidia-kvm.json`](../inputs/definitions/demo/arm64-nvidia-kvm.json) and
[`github-nightly-nvidia.json`](../inputs/environments/github-nightly-nvidia.json).
The Environment's `githubRuntimeSource` policy admits the exact NVKE commit
through the existing `run generate --runtime --source-commit` option. The
generator, source-pin and finalization owners validate that selection. Git
ancestry admission requires the selected commit to belong to `main`, allowing
the branch to advance during the cycle. A branch name, caller path or detached
digest alone grants no artifact authority.

Finalization repeats the proof against the then-current `main`. A delayed retry
outside the 64-commit fetch window, or a ref rewrite that removes the frozen
commit, fails closed before a Run record is issued even if signing and publication
already completed.

The coordinator's `NKE_SOURCE_TOKEN` resolves NVKE `main`; producer source access
uses its existing credential policy. Dispatch and metadata reads use separate
job-scoped GitHub token permissions. Product publication remains in the generated
producer. The runtime job receives only `NVKR_REGISTRY_READ` and
`NVKR_REGISTRY_READ_USER` for publication reads, and
`NVKR_CI_STORAGE_READ_TOKEN`/`NVKR_CI_STORAGE_READ_USER` for private CI transport.
The native runtime owners start candidate processes with their separate native
environment. Source code and the existing network helper remain trusted with
sudo and Docker access; this is no independent sandbox.

Binary bundles, OCI images, QCOW payloads and raw diagnostics use private storage
under the existing fail-closed transport/publication policies. Coordinator
Actions uploads contain only constructed selection JSON, exact producing-Run
locator JSON and the cycle verdict JSON. The runtime job has no registry writer
or OIDC signing permission, and Go's shared Actions cache is disabled.

The native Go consumer admits AMD64 and ARM64 only when the profile's VM target,
selected disk in the Plan and native execution host agree. It repeats that check
after restoring the exact producer Plan; publication receipts still own artifact
authentication. The maintained coordinator and producer currently select ARM64.
No additional AMD64 producer bundle is introduced. A public AMD64 runner lane
requires matching AMD64 artifacts and a measured complete consumer run before
operational acceptance. The upstream-OVN producer remains a separate manual
diagnostic; the maintained native consumer currently selects NVIDIA OVN only.

## Timeouts and evidence

Selection and completed-candidate selection have ten-minute job limits. Producer
dispatch/join and native runtime jobs each have a 360-minute limit; subprocesses
and setup operations have smaller bounds. The runtime Go test receives the time
remaining below a deadline set five minutes before its job limit. It reserves
125 minutes for native cleanup, verdict reporting and margin, and refuses to
start work when that reserve would consume the remaining time. SIGINT/SIGTERM
cancel work through the same owned cleanup lifecycle.

The Go cycle verdict is an unsigned repository integration result, outside
qualified release acceptance. Local contracts cover exact producer/source joins,
publication receipts, native input selection, deadlines and orderly signals.
They do not establish a completed hosted build, publication, bootstrap and cleanup
cycle. That full current ARM64 cycle remains unverified until a recorded run
completes.
