# ADR 0001 - Framework migration and `moved {}` block support

- **Status**: Proposed (2026-05-20). Phase A of execution; gated by phase-by-phase merges.
- **Tracking issue**: [#123](https://github.com/alekc/terraform-provider-kubectl/issues/123)
- **Milestone**: [v3.0.0 - framework migration](https://github.com/alekc/terraform-provider-kubectl/milestone/1)
- **Authors**: alekc

## Context

Terraform 1.8 / OpenTofu 1.8 introduced cross-provider state moves via the `moved {}` HCL block. Users on `gavinbunney/kubectl` (the upstream provider this repo was forked from in 2022) want to migrate to `alekc/kubectl` using `moved {}` instead of `terraform state replace-provider`.

Three reasons the `terraform state replace-provider` workaround is insufficient (raised by @stevehipwell on #123, still valid):

1. Has to be done atomically across all resources in the state.
2. Requires direct state access; awkward in least-privilege environments.
3. No good story for users consuming third-party modules.

The forcing functions are growing: gavinbunney is functionally stalled on Kubernetes v1.32 (community PRs for v1.33 and v1.34 closed without merge), and an open CVE tracker (`gavinbunney/terraform-provider-kubectl#356`) flags GO-2026-4337 / CVE-2025-68121 exposure with no maintainer response. Anyone on k8s 1.33+ has to fork gavinbunney themselves or switch providers; anyone on a CVE-sensitive build has the same pressure.

Usage stats captured on 2026-05-20:
- gavinbunney/kubectl: 186.4M lifetime downloads, 1,220 public .tf files (GitHub code search).
- alekc/kubectl: 55.4M lifetime, 1,108 public .tf files.
- Recent-rate estimates put both providers in a similar order of magnitude (210-420K/week each).

The migration cohort is real, large, and increasingly motivated. Native `moved {}` support is the right answer.

## Hard constraint

`moved {}` for cross-provider moves is a **destination-side** feature implemented via `terraform-plugin-framework`'s `MoveStateResource` interface. The SDK v2 cannot implement it. This repo currently runs a SDK v2 + plugin-framework mux server:

- **SDK v2 side**: resources `kubectl_manifest`, `kubectl_server_version`; data sources `kubectl_manifest`, `kubectl_filename_list`, `kubectl_file_documents`, `kubectl_path_documents`, `kubectl_server_version`.
- **Framework side**: provider config schema mirror; ephemeral resource `kubectl_manifest`.

Mux servers reject same-name resource types on both halves. Adding a sibling framework-side `kubectl_manifest_v2` would force users to rename their resources, defeating the purpose. The migration must therefore be an **atomic per-resource swap**: each resource is removed from SDK v2 and re-implemented on the framework side in the same PR.

## Schema-diff finding (load-bearing for this ADR)

Schema diff between gavinbunney v1.19.0 and alekc master, captured 2026-05-20:

- gavinbunney's `kubectl_manifest` resource has **20 top-level attributes**; alekc has **24** (4 alekc-only: `upgrade_api_version`, `field_manager`, `wait_for`, `delete_cascade`).
- All 20 gavinbunney attributes exist in alekc with the same name and type. **Zero gavinbunney-only attributes**.
- Both at `SchemaVersion: 1`. No upgrade dance across the move boundary.
- Of the 20 shared, 18 are byte-identical; the two with deltas are:
  - `api_version`: `ForceNew: true` in gavinbunney, unset in alekc (behaviour change favourable to migrating users - no destructive plan when api_version changes between equivalent group/version strings).
  - `validate_schema`: typo fix in the `Description` field (cosmetic).
- One real capability gap exists outside the resource migration: gavinbunney has `data "kubectl_kustomize_documents"` (added post-fork via `gavinbunney/terraform-provider-kubectl#113`, merged 2024-12-09). alekc never inherited it. **Phase C ports this data source as framework-native, closing the gap.**

The implication is that the `MoveState` handler for `kubectl_manifest` collapses to roughly 50 lines of passthrough + validation rather than the elaborate converter we initially budgeted for.

## Decision

**Migrate all SDK v2 resources and data sources to plugin-framework in six PRs over ~4-6 weeks, ship Phase D as v3.0.0.**

### Phased delivery

| Phase | PR scope | Release | Risk |
| --- | --- | --- | --- |
| A | This PR. ADR + cross-provider acc-test smoke (continue-on-error until Phase D) | v2.5.0 | Low |
| B | Port `kubectl_server_version` resource + data source to framework | v2.6.0 | Low |
| C | Port `kubectl_filename_list` / `kubectl_file_documents` / `kubectl_path_documents` data sources to framework; **add `kubectl_kustomize_documents`** (port from gavinbunney#113, framework-native from day one) | v2.7.0 | Low |
| D | Port `kubectl_manifest` resource to framework, implement `MoveState` for `gavinbunney/kubectl` source. **Closes #123.** | **v3.0.0** | High |
| E | Port `kubectl_manifest` data source to framework | v3.1.0 | Low |
| F | Drop SDK v2 from go.mod, fully framework-only provider config | v3.2.0 | Medium |

### Why v3.0.0 at Phase D specifically

Phases A through C are not user-observable. Resources that move from SDK v2 to framework with identical schemas produce identical HCL surface and identical state JSON. Users see a minor version bump and nothing else.

Phase D is the breaking-change risk surface, even with byte-identical attributes:

- The protocol-level shape Terraform stores for `kubectl_manifest` can differ subtly between SDK v2 and framework (despite same `SchemaVersion`). The first plan after upgrade may show drift that converges on second apply.
- `CustomizeDiff` -> `PlanModifier` port introduces the highest implementation risk - see the risk table below.
- Cross-provider `moved {}` is a new feature; major-version bumps are the right vehicle for "behaviour shifts subtly, here are the release notes".

Phases E and F ship as v3.x minor versions because by then the major hump is past.

### Why phase-per-PR off master, not a long-lived v3 branch

The work fans across ~6 PRs and ~4-6 weeks. A long-lived feature branch would accumulate merge debt against master (dependabot bumps, the kind of small fixes that land between us shipping #287/#288/#290/#291 in two days). The user-facing impact of Phases A-C is zero, so there's no reason to gate them behind a branch.

The v3 milestone in GitHub Issues tracks the epic; every Phase A-F PR is labelled with it.

## End-user risk surface (SDK v2 -> framework)

The framework's maturity in 2026 is materially different from its 2022-2023 state. `terraform-plugin-framework` v1.0 GA was October 2022 and the version this repo currently uses is v1.19.x. Mux server, MoveState, EphemeralResources, UpgradeState, plan modifiers, block types, and testing primitives are all production-grade. HashiCorp's own providers (aws, google) are mid-migration on the same framework.

What does NOT change for end users:

- HCL syntax (resource / data / provider blocks)
- State file format (Terraform's state JSON is protocol-level, not SDK-specific)
- Schema attribute names and types
- Provider configuration block
- `plan` / `apply` / `destroy` lifecycle

What is potentially user-visible:

| Risk area | Change | Who's affected | Mitigation |
| --- | --- | --- | --- |
| **Plan output formatting** | Framework prints diffs more precisely (better "known after apply" reasoning, attribute paths in errors). | CI / scripts that grep `terraform plan` text | Document in v3.0.0 release notes |
| **Plan-time vs apply-time errors** | `CustomizeDiff` fires at one well-known moment; `PlanModifier` fires at slightly different lifecycle points. Errors may surface at a different phase. | CI that gates on plan-time errors | Acc-test against the existing 94KB `resource_kubectl_manifest_test.go`; flag any timing shifts in release notes |
| **`kubectl_manifest` diff behaviour** (the big one) | The existing `CustomizeDiff` does YAML canonicalisation + live-cluster comparison + drift detection. Porting to `PlanModifier` has to preserve WHEN each piece fires. A subtle port bug surfaces as an unexpected one-time diff on the first plan after upgrade. | Every kubectl_manifest user | Run the alekc-internal upgrade-path smoke from #279 against the Phase D PR; gate merge on plan-no-op result |
| **`MaxItems: 1` schema** | Framework intentionally does NOT emit `MaxItems` at the protocol level. The constraint moves into `ValidateConfig` runtime validation. Same effective behaviour, error surfaces at config-validate time rather than embedded in plan output. | Users hitting MaxItems violations (rare) | Equivalent error message; mention in release notes |
| **Sensitive marking** | Framework has cleaner attribute-level sensitive propagation. Outputs that reference sensitive resource attributes may newly trigger "value will be marked as sensitive" notices. | Users with custom outputs referencing sensitive fields | Document in v3.0.0 release notes |
| **Empty vs null handling** | Framework distinguishes `""` from null more strictly than SDK v2. | Users who deliberately set `foo = ""` | Spot-checked during Phase D acc tests |
| **Diagnostic message wording** | Errors are now structured (summary + detail + attribute path) rather than flat strings. Better UX; pattern-matchers may break. | Logging / monitoring that greps errors | Document in release notes |

What is NOT a risk anymore (closed gaps versus the 2022-2023 framework):

- Mux server (SDK v2 + framework in one provider): production-grade; this repo already runs one.
- State migration: `UpgradeState` is mature; in our case we keep `SchemaVersion: 1` so it's a no-op anyway.
- Validators (resource-level and attribute-level): full parity with SDK v2 `ValidateFunc`.
- Plan modifiers: declarative, composable, supersede `CustomizeDiff`.
- Cross-provider `MoveStateResource`: this is the feature we're shipping.
- Ephemeral resources: alekc already ships one.
- Block types (`SingleNestedBlock`, `ListNestedBlock`, etc.): all stable.
- Testing primitives: `tfresource.Test` works identically against framework resources.

Subjectively rough corners that affect **maintenance**, not end users:

- Framework code is ~30-40% more verbose than SDK v2 for equivalent functionality. Author-facing cost only.
- HashiCorp's `tfplugin-framework-migrator` tool is a starting point for greenfield ports; for the 1500-line `resource_kubectl_manifest.go` it's not a finished port. Manual review of every attribute is unavoidable.
- Plan-modifier sequencing on the same attribute is implicit (declaration order). Easy to introduce subtle bugs during port; this is the highest-risk part of Phase D.

### Net risk assessment for v3.0.0

The user-facing risk is concentrated in **one specific path**: the first `terraform plan` against existing state after upgrading from v2.x to v3.0.0. If the `PlanModifier` port on `kubectl_manifest` is bug-free, the plan is no-op and the user notices nothing except improved error messages. If the port has a subtle wrong-when-it-fires bug, users see an unexpected one-time diff that converges on the second apply.

The alekc-internal upgrade-path smoke from `#279` is the gate that catches this: it applies with the published v2.x provider from registry, swaps to the built v3.0.0 binary via `dev_overrides`, and asserts plan is no-op. If that smoke goes green on the Phase D PR, end-user surprises are bounded.

## Alternatives considered

**Tactical interim: `terraform-provider-kubectl-migrate` CLI**. A separate tool wrapping `terraform state replace-provider` with the multi-module coordination it currently lacks. Lower-cost answer; doesn't close #123 but reduces urgency. Decision: build only if Phase D slips past ~6 weeks. The framework migration is the right long-term answer.

**Close #123 as out-of-scope citing `terraform state replace-provider`**. Rejected: @stevehipwell's three objections still stand (atomic-across-modules, state access, manual coordination). The migration cohort is large and motivated; the request is reasonable.

**Long-lived v3 feature branch**. Rejected: merge debt accumulates fast against master; we move at ~2 PRs/day on this repo and a side branch would diverge quickly. Phase-per-PR off master gets the same outcome with less rebasing pain.

**Big-bang single PR**. Rejected: 6 phases of work in a single review is unreviewable. The phases are also chosen so each ships independently meaningful value (Phase A's smoke catches regressions in any subsequent phase; Phase B and C ship invisible-but-useful framework wiring; Phase D delivers the feature; E and F are cleanup).

## Acceptance - Phase D specifically

Phase D ships v3.0.0 (closes #123) when:

- `kubectl_manifest` is a framework Resource implementing `MoveStateResource` for `registry.terraform.io/gavinbunney/kubectl` source.
- The full existing acc test suite (`resource_kubectl_manifest_test.go`, 94KB / ~2700 lines) passes against the framework implementation.
- The new cross-provider acc smoke from Phase A flips `continue-on-error: false` and stays green.
- The alekc-internal upgrade-path smoke from #279 stays green (proves byte-identical state round-trip from v2.x to v3.0.0).
- Release notes document every behaviour shift from the risk table above.

## Consequences

- Users upgrading from any v2.x to v3.0.0 follow a documented release-notes path; subtle behaviour shifts are listed.
- Users on `gavinbunney/kubectl` can write `moved { from = kubectl_manifest.foo; to = kubectl_manifest.foo }` (with `required_providers` updated to alekc/kubectl) and run `terraform apply` to migrate. No state-CLI dance.
- alekc maintains a framework-native codebase going forward. SDK v2 dependency dropped at Phase F.
- The `kubectl_kustomize_documents` data source (long absent from alekc, added to gavinbunney post-fork) joins alekc in Phase C, closing the last "gavinbunney has X that alekc doesn't" caveat.
