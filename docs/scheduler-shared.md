# Shared CPU Pool Design

Design notes for extending the [Proxmox Scheduler](scheduler.md) to manage VMs that have **no** CPU affinity
in their configuration, so that they stop stealing CPU time from pinned VMs and from each other.

Status: implemented (`cpumanager.PlanShared`, `pkg/utils/sys.SetThreadsAffinity`/`SetProcessAffinity`,
and the daemon wiring in `cmd/proxmox-scheduler`), with two deliberate simplifications versus the plan
below:

- §7's NUMA hint comes from the NUMA nodes a shared VM's *current* cpuset already touches, not from reading
  `/proc/<pid>/numa_maps`. This still keeps a running VM from being moved across NUMA nodes on every
  rebalance (the sticky-retention pass in §4.3 is what actually does the pinning), it just doesn't yet look
  at where the guest's memory pages physically are before the very first placement.
- §4.3's mode-transition hysteresis (the 0.9× dead band) is not implemented; only sticky retention of
  still-valid assignments is. In practice this only matters right at the fitting/oversubscribed boundary.

`--shared-policy` defaults to `none` — the pre-existing behavior, unchanged for anyone who upgrades without
touching config (§13 step 4, "ship dark"). Set it to `partition` to turn the shared-pool placement in §4 on.

## 1. Problem

Today the scheduler daemon reacts to `/run/qemu-server/<vmid>.pid` events and only acts on VMs that carry an
explicit affinity (either the `affinity` config option or an `affinity=<cpuset>` token in the VM description,
see `loadVMConfig` in `cmd/proxmox-scheduler/vm.go`):

- if `cores == len(affinity)`, each QEMU vCPU thread is pinned 1:1 to a host CPU (`PinThreadsToCores`),
- the busy CPU governor is applied to those CPUs,
- IRQs of the VM's passthrough devices are steered to the same CPUs.

Every other VM is left untouched. Its vCPU threads keep the default affinity mask — **all** online CPUs,
including the ones that were carefully handed out to pinned VMs. The consequences:

1. **Pinning is not exclusive.** A pinned latency-sensitive VM shares its cores with every floating VM on the
   host. The pinning only guarantees *where* the VM runs, not that it runs *alone*.
2. **Floating VMs disturb each other.** Two unrelated VMs can end up on the same physical core (SMT siblings),
   which costs far more than running them on different cores while the host still has idle cores.
3. **Accounting drifts from reality.** `VMTracker.sharedCPUs` is already computed
   (`cmd/proxmox-scheduler/vm.go:updateVMInfo`) and `VMInfo.AssignedSet` already exists, but neither is ever
   applied to a process. Karpenter's capacity view (`pkg/providers/cloudcapacity`) counts those VMs as
   `assignedCPUs`, i.e. as if they were confined — on the host they are not.

The optimisation is also **post-start only**: it happens when the PID file appears, and nothing re-evaluates
the placement of already-running VMs when the set of VMs on the host changes.

## 2. Goals

- Confine every running VM to a cpuset, not just the ones with an explicit affinity.
- **While the host is not oversubscribed, no two VMs share a host CPU.** Floating VMs get disjoint cpusets
  carved out of the CPUs left over by pinned VMs.
- When the sum of vCPUs no longer fits, degrade gracefully and deterministically: shrink and overlap in a
  controlled way instead of falling back to "everyone on every CPU".
- Keep placement topology-aware (NUMA node, uncore cache / CCX, physical core) by reusing the existing
  `pkg/zonecapacity/internal/cpumanager` machinery instead of writing a second allocator.
- Re-evaluate on VM start, VM stop and resync, while keeping churn on running VMs minimal.

### Non-goals

- Live migration of guest memory between NUMA nodes. A running VM's memory placement is fixed; the shared
  planner works around it (§7).
- CPU bandwidth control (`cpulimit`, cgroup quotas). This design is about *placement*, not *share*.
- Changing how Karpenter provisions VMs. The daemon never edits VM configs (except the karpenter capacity VM),
  it only manipulates thread affinity at runtime.

## 3. VM classes

| Class | Detection | Handling |
|---|---|---|
| `pinned` | `affinity` set (config or description) and `cores == len(affinity)` | 1:1 vCPU→CPU pinning, exclusive ownership of those CPUs. Today's behaviour. |
| `constrained` | `affinity` set but `cores != len(affinity)` | Mask all vCPU threads with the affinity set; CPUs are marked exclusive but not owned 1:1. Today these VMs are silently skipped. |
| `shared` | no affinity | **New.** The scheduler computes a cpuset and applies it to all vCPU threads. |
| `ignored` | VM in the karpenter capacity/topology discovery role (`tags: karpenter`), or a VM the daemon cannot read | Excluded from planning, and its CPUs are not treated as used. |

The union of the CPU sets of `pinned` + `constrained` VMs is the **exclusive pool**. Everything else, minus
the host-reserved CPUs, is the **shared pool**:

```
sharedPool = allCPUs - reservedCPUs - exclusivePool
```

`reservedCPUs` is the host's own slice (PVE services, ZFS, Ceph, network softirqs). The controller side
already has this concept in `settings.NodeSettings.ReservedCPUs`; the daemon needs the same knob (§9).

## 4. Planner

A pure function, easy to unit-test, that turns the observed state into a placement plan:

```go
// pkg/zonecapacity/internal/cpumanager/planner.go
type SharedRequest struct {
    VMID    int
    CPUs    int            // cores from the VM config
    NUMAHint cpuset.CPUSet // NUMA nodes the VM's memory already lives on (empty for new VMs)
    Current cpuset.CPUSet  // currently applied set, for stability
}

type SharedPlan map[int]cpuset.CPUSet // VMID -> cpuset to apply

func PlanShared(logger logr.Logger, topo *topology.CPUTopology,
    pool cpuset.CPUSet, reqs []SharedRequest, opts SharedOptions) (SharedPlan, error)
```

It reuses `cpu_assignment.go` as-is: `takeByTopologyNUMAPacked` for the per-VM carve-out, and the
`cpuAccumulator` sorting (`CPUSortingStrategyPacked` / `CPUSortingStrategySpread`) for the ordering. No new
topology code is required.

Determinism: requests are sorted by `(CPUs desc, VMID asc)` before allocation, so the same input always yields
the same plan regardless of event order.

### 4.1 Fitting mode (`sum(CPUs) <= |sharedPool|`)

Allocate each VM a **disjoint** set of exactly `CPUs` host CPUs:

```
remaining := sharedPool
for each req (sorted):
    cpus := takeByTopologyNUMAPacked(topo, remaining ∩ preferred(req), req.CPUs,
                                     CPUSortingStrategyPacked, preferAlignByUncoreCache=true)
    plan[req.VMID] = cpus
    remaining = remaining.Difference(cpus)
```

`preferred(req)` narrows the candidate set to the VM's sticky NUMA nodes when it has any (§7); if the
narrowed set cannot satisfy the request, the planner retries against the whole `remaining` and logs the
spill.

Packed sorting deliberately keeps a VM inside one NUMA node / uncore cache; `DistributeCPUsAcrossCores` is
*off*, so a 4-vCPU VM lands on 2 physical cores with their SMT siblings rather than on 4 cores shared with
other guests. Owning both siblings of a core is the point: SMT siblings are the strongest noisy-neighbour
channel.

### 4.2 Oversubscribed mode (`sum(CPUs) > |sharedPool|`)

Full isolation is impossible, so the planner minimises the *degree* of sharing instead of giving up:

1. **Scale.** `factor = |sharedPool| / sum(CPUs)`; each VM's target width becomes
   `max(minWidth, ceil(CPUs * factor))`, rounded up to a whole physical core.
   `minWidth` defaults to `CPUsPerCore()` (one full core).
2. **Group.** Partition the shared pool into `G` groups along topology boundaries (NUMA node first, then
   uncore cache), where `G = floor(|sharedPool| / maxWidth)`, and bin-pack VMs into groups by total requested
   vCPUs (least-loaded first).
3. **Overlap inside a group only.** Each VM gets a set of its target width taken from its group. VMs in
   different groups never share a CPU; VMs in the same group may.

So the isolation guarantee degrades from "no VM shares a CPU" to "a VM shares CPUs only with the handful of
VMs in its group", and only once the host is genuinely oversubscribed. In the pathological case
(`G == 1`), the result is the current behaviour restricted to the shared pool — which is still strictly better
than today, because pinned VMs stay protected.

### 4.3 Stability

Re-planning from scratch on every event would shuffle running VMs constantly. Two dampeners:

- **Sticky allocation.** A VM whose current set is still valid (inside the pool, right width, on its sticky
  NUMA nodes, disjoint from others in fitting mode) keeps it: the planner seeds `remaining` by first removing
  all such retained sets, then allocates only the rest.
- **Hysteresis.** The plan is only applied where it differs from the applied state, and the transition
  fitting ↔ oversubscribed uses a small dead band (e.g. re-expand only when `sum(CPUs) <= 0.9 * |sharedPool|`)
  so a VM flapping on and off does not resize every guest on the host.

## 5. Applying a plan

`PinThreadsToCores` requires `len(threads) == len(cores)` and is therefore only usable for the `pinned` class.
Shared VMs need a *mask*, not a 1:1 map — a new helper:

```go
// pkg/utils/sys/affinity.go
func SetThreadsAffinity(ctx context.Context, vmID, pid int, threads []int, cpus cpuset.CPUSet) error
```

implemented as `taskset --cpu-list --pid <cpus> <tid>` per thread, mirroring the existing style.

Which threads:

- **vCPU threads** — `GetProcessThreads(pid, "CPU")`, the same filter used today.
- **The QEMU process itself** (`taskset -pa`) so that emulator, IO and VNC threads inherit the mask, and any
  thread created later starts inside the set. Worth doing for the pinned class too: today only the `CPU`
  threads are placed, and the emulator threads keep roaming the whole host.

Applying is idempotent: the daemon reads the current mask (`/proc/<pid>/task/<tid>/status:Cpus_allowed_list`
or `taskset -p`) and skips threads that already match, so a resync over an unchanged host writes nothing.

**Governor.** The shared pool is busy whenever at least one shared VM exists, so `cpuGovernorBusy` applies to
`exclusivePool ∪ usedSharedCPUs` and `cpuGovernorFree` to the remainder. Note the existing gap: `handleVMStop`
computes the freed set from `tracker.vms` but never updates `tracker.usedCPUs`/`sharedCPUs`, so the tracker
stays stale until the next start or resync. The shared planner depends on that state being correct, so
`handleVMStop` must recompute both — fix it as part of this work.

## 6. Control flow

```
                 ┌──────────── reconciler events ────────────┐
   pid created ──┤  FileEvent(create) → handleVMStart        │
   pid removed ──┤  FileEvent(remove) → handleVMStop         ├──► markDirty()
   resync tick ──┤  SyncEvent         → handleSyncEvent      │
                 └───────────────────────────────────────────┘
                                     │
                              (debounce ~2s)
                                     ▼
                        rebalanceShared(ctx)
                    1. snapshot tracker → requests + pool
                    2. PlanShared(...)
                    3. diff vs applied, apply deltas
                    4. governor sweep, log status
```

`handleVMStart` keeps its current work for pinned VMs (1:1 pinning + IRQ steering) and additionally marks the
tracker dirty; the shared placement is not done inline, because a new VM changes the placement of others. The
debounce collapses the burst of events a host reboot or a Karpenter scale-up produces.

A newly started shared VM is confined *before* the rebalance too — a provisional mask equal to
`sharedPool - exclusivePool` — so it never runs unconstrained, even for the debounce window.

## 7. Memory and NUMA

A running VM's memory is already allocated; moving its vCPUs to another NUMA node turns every access into a
remote access. Therefore:

- On first placement, a shared VM is preferably confined to a single NUMA node with enough free memory. The
  daemon can read the actual placement from `/proc/<pid>/numa_maps` (or `numastat -p <pid>`) and record the
  nodes where the VM's pages actually live.
- That set becomes the VM's **sticky NUMA hint** and is honoured on every subsequent rebalance. A rebalance
  may narrow or widen a VM's cpuset *within* its nodes, and may only move it across nodes when the VM has no
  memory yet or the current nodes cannot satisfy `minWidth`.
- VMs that already span several nodes (large guests, ballooning) get the union of their nodes as the hint.

This is the main asymmetry with the controller-side `staticPolicy`, which plans placement *before* the VM
exists and can therefore choose both CPU and memory freely.

## 8. Relation to `cloudcapacity`

`pkg/providers/cloudcapacity` (controller side, talks to the Proxmox API) and the scheduler daemon (host side,
talks to `/proc` and the local socket) answer different questions:

| | cloudcapacity | scheduler daemon |
|---|---|---|
| When | before a VM is created | while a VM runs |
| Output | `VMResources.CPUSet` / `NUMANodes` written into the VM config | thread affinity applied to a live process |
| Scope | whole cluster, per node | one host |

They must not fight each other. The contract:

- Whatever `cloudcapacity` writes into `affinity`/`numaN` makes the VM `pinned`, and the daemon honours it.
- Everything else is the daemon's to place, and the daemon never writes it back into the VM config — the
  assignment is runtime-only state, so a `qm migrate` or a manual `qm set` cannot conflict with it.
- Capacity accounting is unchanged: `simplePolicy`/`staticPolicy` keep counting an unpinned VM as `CPUs`
  against `assignedCPUs`. The shared plan does not create or destroy capacity, it only decides where those
  vCPUs land.

Shared code lives in `cpumanager`: `PlanShared` sits next to the policies and is consumed by the daemon.
`topology.Topology` is already shared (`DiscoverCadvisor` on the host, `DiscoverFromSettings` in the
controller).

## 9. Configuration

New flags/env vars, in the style of the existing ones:

| Flag | Environment Variable | Default | Meaning |
|---|---|---|---|
| `--shared-policy` | `SHARED_POLICY` | `partition` | `none` (today's behaviour), `partition` (§4). |
| `--reserved-cpus` | `RESERVED_CPUS` | `""` | Host-only CPUs, e.g. `0,32`. Excluded from every pool. |
| `--shared-min-width` | `SHARED_MIN_WIDTH` | `0` | Minimum CPUs per shared VM; `0` = one physical core. |
| `--rebalance-debounce` | `REBALANCE_DEBOUNCE` | `2s` | Event coalescing window. |

`--shared-policy=none` must remain a supported escape hatch: it restores exactly today's semantics.

## 10. Worked example

The 64-CPU / 4-NUMA host from [scheduler.md](scheduler.md) (`0-7,32-39` = node 0, siblings included), with
`reservedCPUs = 0,32`:

```
VM 100  cores=8  affinity=8-15          → pinned,   exclusive 8-15
VM 200  cores=4  (no affinity)          → shared
VM 201  cores=4  (no affinity)          → shared
VM 202  cores=16 (no affinity)          → shared

exclusivePool = 8-15
sharedPool    = 1-7,16-31,33-39,40-63      (54 CPUs)
sum(shared CPUs) = 24 ≤ 54                 → fitting mode

VM 202 → 16-23,48-55   (node 2, all 8 cores with their siblings)
VM 200 → 1,2,33,34     (node 0)   ← width 4 = 2 physical cores + their siblings
VM 201 → 3,4,35,36     (node 0, disjoint from VM 200)
```

No two guests share a CPU, VM 100 finally has 8-15 to itself, and each shared VM sits inside one NUMA node. The
packed strategy fills the partly used node 0 before breaking into node 3, which stays whole for the next large VM.
Start a 32-vCPU VM 203 and the total becomes 56 > 54: the planner switches to oversubscribed mode, scales
widths by ~0.96, groups the pool by NUMA node, and only VMs inside the same node group overlap.

## 11. Observability

- `logVMStatus` gains the plan: per VM the class, the applied set, and its sticky NUMA nodes; per host the
  pool sizes and the mode (`fitting` / `oversubscribed`, with the fit ratio).
- A counter of "VMs moved" per rebalance makes churn visible; a steady-state host should report `0`.
- Optional state dump (`/run/proxmox-scheduler/state.json`) for debugging and for `troubleshooting.md`.

## 12. Edge cases

- **vCPU hotplug** — the thread list changes without a PID event; the resync tick catches it, and a rebalance
  re-reads `GetProcessThreads`.
- **A vCPU thread appears late** — QEMU creates vCPU threads during startup; `handleVMStart` already errors and
  relies on the reconciler's retry. Unchanged.
- **Affinity narrower than the pool** — a `constrained` VM (`cores != len(affinity)`) keeps its configured
  mask; the daemon only marks its CPUs exclusive.
- **Topology mismatch** — an affinity referencing CPUs the host does not have is already logged and skipped
  without retry; those CPUs must not enter the exclusive pool.
- **`qm migrate` / VM stop during a rebalance** — applying to a dead PID must be tolerated (`ESRCH` → drop the
  VM from the tracker, re-plan on the next tick), never fatal.
- **Host reboot / daemon restart** — no persisted state is needed: the sync event rebuilds the tracker from
  `/run/qemu-server` and the plan converges to the same result (the planner is deterministic).
- **CPU offlining** — `allCPUs` comes from the topology discovered at startup; a changed online mask needs a
  restart. Worth logging a warning if `/sys/devices/system/cpu/online` drifts.

## 13. Implementation plan

1. Fix the tracker: recompute `usedCPUs`/`sharedCPUs` on stop; classify VMs (`pinned`/`constrained`/`shared`/
   `ignored`); populate `VMInfo.AssignedSet`.
2. `SetThreadsAffinity` + idempotent apply in `pkg/utils/sys`, with the current-mask read.
3. `cpumanager.PlanShared` (fitting mode only) + unit tests reusing the topology fixtures from
   `policy_test.go`.
4. Wire the debounced `rebalanceShared` into the reconciler; `--shared-policy=none` by default at first, to
   ship it dark.
5. Oversubscribed mode, stickiness and hysteresis.
6. NUMA hint from `/proc/<pid>/numa_maps`.
7. Flip the default to `partition`, document in `scheduler.md`.

Testing: the planner is pure and gets table tests (single/multi NUMA, SMT on/off, fitting and oversubscribed,
stability across repeated calls with a mutating VM set). The apply path gets an interface around `taskset` so
it can be faked; the e2e lifecycle tests cover "VM starts → mask applied → VM stops → mask released".
