# Zone Capacity Module Design

Design notes for `pkg/zonecapacity`, one module that both the controller and the
[Proxmox Scheduler](scheduler.md) use to work out what a zone can hold and where its VMs' CPUs go.
It implements the shared rules of [scheduler-shared.md](scheduler-shared.md),
[scheduler-cpushare.md](scheduler-cpushare.md) and [scheduler-numa.md](scheduler-numa.md).

Status: proposed.

## 1. Problem

The controller and the scheduler each keep their own picture of a zone:

| | Controller | Scheduler |
|---|---|---|
| What a VM is | `resources.VMResources` from the REST `qemu.Config` (`pkg/proxmox/resources/vm`) | `VMInfo` + `classifyVM` from the local `qemu.Config` (`cmd/proxmox-scheduler/vm.go`) |
| CPUs in use | `resourcemanager.ZoneCapacity.usage()` | `VMTracker.usedCPUs` / `sharedCPUs`, `sharedPool()` |
| Where CPUs go | `Pinned.place` at Reserve | `cpumanager.PlanShared` at runtime, on a pool the scheduler computes itself |

The three designs above only work if both pictures agree:

- Karpenter charges CPU share groups as `Σ ceil(count/N) × vCPU`; the scheduler must build exactly that many groups.
- A NUMA slot is free only if `|usable − pinned| − eCPUs ≥ slot CPUs`; the scheduler must be able to move the groups
  off a slot Karpenter hands out.
- Both compute the floating pool as `all − reserved − exclusive`.

Written twice, these rules drift: Karpenter offers a slot the scheduler cannot clear, or charges for groups the
scheduler never builds. The `affinity=` / `cpushare=` description tokens are also written in one place and parsed in
two.

## 2. Goals

- One module that owns every rule shared by the two binaries: VM classification and token parsing, the pool
  arithmetic, the CPU share group count, NUMA slots, pinned placement, and the runtime plan.
- A small interface: plain values in, values out. No Proxmox client types, no I/O, no clock.
- Both binaries cannot go around it: `cpumanager` becomes internal, so no caller can plan on a pool it computed
  itself.
- Drift is caught by tests at the module's interface, and reported at runtime when it happens anyway.

### Non-goals

- Pending reservations (timestamps, grace period, `KeepPending`). They are about Karpenter's sync timing and stay in
  `resourcemanager`.
- Zone choice and the placement strategy (`cloudcapacity/zonechoice.go`). The module answers `Fit` per zone; ranking
  zones stays in the controller.
- Applying a plan (`taskset`, governors, IRQs). That is the scheduler's I/O.
- Discovering topology. Each binary builds a `topology.Topology` from its own source (node settings, cadvisor).

## 3. Interface

```go
package zonecapacity

// Host is the zone's fixed shape. The node policy is not part of it: the scheduler does not know it.
type Host struct {
    Topology       *topology.Topology
    ReservedCPUs   cpuset.CPUSet
    ReservedMemory uint64 // split evenly across NUMA nodes; ignored when HugePageSize > 0
    HugePageSize   uint64 // > 0: each NUMA node's memory in Topology is its hugepage pool
}

func New(h Host) (*Zone, error)

// VM is one virtual machine as either binary reads it from its config. The module parses the raw fields.
type VM struct {
    ID          int
    Name        string         // `node-capacity` with the `karpenter` tag marks the node-capacity guest
    CPUs        int
    Memory      uint64
    Affinity    string         // the `affinity` option, "" when unset
    Description string         // `affinity=` fallback and `cpushare=N` tokens
    Tags        []string       // `karpenter` on the `node-capacity` VM marks the node-capacity guest, which is ignored
    NUMAMemory  map[int]uint64 // host NUMA node -> bound bytes, from numaN hostnodes
    CPUShare    int            // Reserve/Fit: ask for a CPU share VM of N seats (2, 3 or 4); wins over the token
    Slot        *SlotShape     // Reserve/Fit only: ask for a strict NUMA slot of this shape
}

// Policy is the node policy, passed only where the controller needs it.
type Policy struct {
    Pinned     bool    // static
    Overcommit bool    // simple-with-cpu-overcommit: no CPU share VMs; one made by hand counts as shared
    Ratio      float64 // CPU overcommit ratio for shared placement; 0 means 1
}

// Bookkeeping, both binaries.
func (z *Zone) Track(vm VM) error
func (z *Zone) Release(id int)

// Controller.
func (z *Zone) Reserve(p Policy, vm VM) (Grant, error)
func (z *Zone) Fit(p Policy, vm VM) (Fit, error)
func (z *Zone) Available(p Policy) Capacity
func (z *Zone) Slots(p Policy) []SlotOffer

// Scheduler.
func (z *Zone) Plan(current map[int]cpuset.CPUSet, o PlanOptions) Plan
```

```go
type Grant struct {
    CPUSet            cpuset.CPUSet       // empty for floating and CPU share VMs
    NUMA              map[int]NUMABinding // host node -> guest vCPUs and memory, ready for numaN
    HugePageSize      uint64              // > 0: launch the VM with hugepages of this size
    DescriptionTokens []string            // e.g. "affinity=8-15", "cpushare=2": written into the VM description
}

type Fit struct {
    NUMANodes int     // how many NUMA nodes the VM would span
    Leftover  float64 // share of the zone's scarcer resource left free
}

type Capacity struct { CPUs int; Memory uint64 }

type SlotShape struct { Fraction, CPUs int; Memory uint64 }
type SlotOffer struct { Shape SlotShape; Free int }

type PlanOptions struct {
    Partition bool // --shared-policy=partition; false = none
    MinWidth  int  // --shared-min-width
}

type Plan struct {
    Changed map[int]cpuset.CPUSet // VMs whose cpuset must change; absent = leave as is
    OneToOne map[int]bool         // VMs in Changed whose vCPUs are pinned 1:1, not masked
    Busy    cpuset.CPUSet         // CPUs some VM uses, for the busy governor
    Idle    cpuset.CPUSet         // the rest of the usable CPUs
    Notes   []string              // spills, groups that did not fit, accounting drift
}
```

### 3.1 Invariants

- **Records are policy-free facts.** `Track` never takes a policy and never fails on budget. The policy is applied
  only when `Reserve`, `Fit`, `Available` or `Slots` run.
- **Usage is recomputed from the records** on every query, so tracking, reserving or releasing the same VM any number
  of times never drifts (as `ZoneCapacity` does today).
- **Classification**, in order:
  1. the node-capacity guest (named `node-capacity` and tagged `karpenter`): ignored, never counted. Workload VMs
     often carry the `karpenter` tag too, so the tag alone is not enough,
  2. an affinity (option, or `affinity=` token) with `cores == |affinity|`: pinned,
  3. any other affinity: constrained (a `cpushare` token next to an affinity is ignored, with a note),
  4. a `cpushare=N` token: CPU share VM,
  5. otherwise: floating.
- **One count.** `eCPUs = Σ ceil(count/N) × vCPU` over each `(N, vCPU)` class is computed by one internal function.
  Accounting subtracts it in `Reserve`, `Fit`, `Available` and `Slots`; `Plan` builds exactly that many groups.
- **Round trip.** Tracking a VM whose config was written from a `Grant` yields exactly the record `Reserve` created.
- **Slot free** iff its CPUs are unpinned, its NUMA node has the slot's memory, and `|usable − pinned| − eCPUs ≥ slot
  CPUs`. Strict slot allocation takes the lowest NUMA node, then the lowest slot index.
- **`Fit` fails exactly when `Reserve` would**, and reserves nothing.
- **`Plan` is deterministic** in the records, `current` and the options, and does not depend on map order.
  Priority: pinned > CPU share groups > floating.
- **`Plan` with `Partition: false`** only touches floating VMs when at least one CPU share group exists (to keep them
  off the groups); with no groups it is the pre-existing no-op.

### 3.2 Ordering

- The `Zone` is safe for concurrent use; every call runs under one internal lock.
- `Fit` then `Reserve` is not atomic: `Reserve` checks again and may return `ErrNoRoom`, as zone choice already
  expects.
- The scheduler passes `current` as the cpusets it has actually applied, i.e. only after `taskset` succeeded. The
  module never assumes its last plan was applied.
- When a VM restarts with a new PID, the scheduler `Release`s and `Track`s it again.

### 3.3 Errors

| Error | When | Caller action |
|---|---|---|
| `ErrNoRoom` | `Reserve`/`Fit`: not enough CPUs, memory, or no free slot (wrapped with the reason) | `InsufficientCapacityError`, try another zone |
| `ErrNotOffered` | a CPU share VM under overcommit, or a slot under shared placement | the type has no offering here; permanent for the policy |
| `ErrExists` | `Reserve` on an ID that has a record | bug in the caller |
| `ErrInvalid` | no ID, no CPUs/memory for `Reserve`/`Fit`, unparseable affinity or token, bad ratio | bug or bad config |

`Release` of an unknown ID is a no-op. `Plan` never fails; problems come back in `Notes`.

## 4. Usage

### 4.1 Controller

`resourcemanager` keeps one `Zone` per zone, plus the pending reservations (the `VM` and `Grant` it reserved, and when).

```go
z, err := zonecapacity.New(zonecapacity.Host{
    Topology: topo, ReservedCPUs: cpuset.New(ns.ReservedCPUs...),
    ReservedMemory: ns.ReservedMemory, HugePageSize: ns.HugePageSize,
})

for _, vm := range running {               // capacity sync
    _ = z.Track(vmFromREST(vm.Resource, vm.Config))
}
pending.retrack(z)                         // KeepPending, outside the module

fit, err := z.Fit(policy, vmFromRequest(req))   // zone choice, per candidate zone
grant, err := z.Reserve(policy, vmFromRequest(req))
// write grant.CPUSet as affinity, grant.NUMA as numaN, grant.DescriptionTokens into the description

for _, o := range z.Slots(policy) {        // n1/n2/n4 offerings
    offer(o.Shape, o.Free > 0)
}
```

`vmFromREST` and `vmFromRequest` are field copies: no rules.

### 4.2 Scheduler

`VMTracker` keeps only PIDs and the applied cpusets.

```go
z, err := zonecapacity.New(zonecapacity.Host{Topology: topo, ReservedCPUs: reserved})

_ = z.Track(zonecapacity.VM{                // start / sync
    ID: id, CPUs: cores, Memory: mem,
    Affinity: cfg.Affinity, Description: cfg.Description, Tags: tags,
})
z.Release(id)                               // stop

plan := z.Plan(tracker.applied(), zonecapacity.PlanOptions{Partition: partition, MinWidth: w})
for id, cpus := range plan.Changed {        // debounced rebalance
    if apply(id, cpus, plan.OneToOne[id]) == nil {
        tracker.setApplied(id, cpus)
    }
}
setGovernors(plan.Busy, plan.Idle)
logNotes(plan.Notes)
```

`classifyVM`, `sharedPool`, `numaHint`, the `usedCPUs`/`sharedCPUs` bookkeeping and the direct `cpumanager.PlanShared`
call are deleted.

## 5. What the implementation hides

- Description token parsing and VM classification.
- Per-NUMA reserved memory, or the hugepage pool.
- Pinned placement: tightest NUMA node, soft packing away from free slots, memory split across nodes.
- NUMA slot carving: CCX order, whole cores and whole GiB, buddy nesting, the three free-slot rules, strict
  allocation.
- The CPU share group count and its effect on pinned room and shared capacity.
- The pool arithmetic: exclusive, group, shared.
- The CPU share group planner (keep, merge, place on the NUMA node with the most pinned CPUs, spill) and
  `PlanShared` (fitting and oversubscribed modes, stickiness, NUMA hints from `current`).
- `TakeByTopology` and the CPU sorters of `cpu_assignment.go`.

## 6. Package layout

```
pkg/zonecapacity/
    zone.go, vm.go, plan.go, slots.go, ...   the interface above
    topology/                                public: both binaries build a Topology
    settings/                                public: NodeSettings, moved from resourcemanager/settings
    internal/cpumanager/                     cpu_assignment.go, take.go, planner_shared.go, planner_cpushare.go
```

`topology` stays public because each binary discovers it its own way. `settings` moves with it, because
`topology.DiscoverFromSettings` imports it today (`cpumanager/topology/discovery.go`), and `topology` cannot live under
`zonecapacity` while it imports from `providers/.../resourcemanager`.

No logger: everything the scheduler logs today about placement comes back in `Plan.Notes`. No ports or adapters
inside the module: every dependency is in-process, and the only adapters are the field copies each binary owns.

## 7. Testing

All tests go through the interface above, on synthetic topologies (`test/topology`).

- **Worked examples** of [scheduler-shared.md](scheduler-shared.md) §10, [scheduler-cpushare.md](scheduler-cpushare.md)
  §9 and [scheduler-numa.md](scheduler-numa.md) §9, as table tests.
- **Round trip.** For random requests, `Reserve` → build the VM config from the `Grant` → `Track` on a fresh zone gives
  the same `Available` and `Slots`.
- **Consistency.** For random VM sets: the CPU share groups in `Plan` add up to the `eCPUs` accounting charges, no two
  groups, pinned VMs or floating VMs overlap while the zone fits, and every slot `Slots` reports free can be
  `Reserve`d and then planned without moving a group off the zone.
- **Determinism.** `Plan` on shuffled input gives the same result.

The existing unit tests of `placement.go`, `planner_shared.go`, `policy_test.go` and `classifyVM` are replaced by
these once they cover the same cases.

`Plan` also emits a drift note at runtime when the groups it builds differ from the charged `eCPUs`, so a mismatch
the tests missed shows up in the scheduler's log rather than as a silent overbooking.

## 8. Migration plan

Each step builds and passes the tests on its own.

1. **Move `settings` and `topology`.** `resourcemanager/settings` → `pkg/zonecapacity/settings`,
   `cpumanager/topology` → `pkg/zonecapacity/topology`. Import path changes only (controller, scheduler,
   `pkg/utils/nodesettings`, `test/topology`).
2. **Move `cpumanager` to `pkg/zonecapacity/internal/cpumanager`.** The scheduler's direct `PlanShared` call blocks
   this step, so it lands together with a temporary `zonecapacity.Plan` that wraps today's `sharedPool` +
   `PlanShared` logic (floating VMs only).
3. **Port `ZoneCapacity`.** Move `zonecapacity.go` and `placement.go` behind `Zone`; `resourcemanager` keeps
   settings loading, policy selection and pending reservations, and maps `VMResources` ↔ `VM`/`Grant`. Token parsing
   moves from `pkg/proxmox/resources/vm` and `cmd/proxmox-scheduler/vm.go` into `VM`. The discovery guest is ignored
   by its tag in both binaries.
4. **Switch the scheduler.** `VMTracker` → PIDs + applied cpusets; `Track`/`Release`/`Plan` replace the class and
   pool bookkeeping.
5. **Add the contract tests** (§7) and delete the old unit tests they cover.

The CPU share groups ([scheduler-cpushare.md](scheduler-cpushare.md) §11) and NUMA slots
([scheduler-numa.md](scheduler-numa.md) §11) are then implemented inside the module, each changing `Plan`,
accounting and `Slots` together.

## 9. Alternatives considered

- **Three entry points** (`Apply(op, vm)` with a Probe/Reserve/Track/Release kind, and one `Snapshot` view). Kept: the
  module parses raw fields. Dropped: the kind switch adds no depth, and the controller would pay for a plan it never
  reads.
- **Sealed request and policy types with read-only views** for future callers (metrics, CLI). Kept: drift is reported
  in the plan. Dropped: a wide interface for callers that do not exist.
- **Pure functions over immutable snapshots.** Kept: runtime truth (`current`) is an argument to `Plan`, not state in
  the zone. Dropped: it pushes the Reserve-vs-Reserve race and the "append the placement unchanged" rule onto the
  controller.
- **Scheduler state inside the zone** (`Applied(id, cpus)` or a `Current` field on `VM`). Dropped: it puts runtime
  state into records the controller shares, and a module that remembers its own plan would assume `taskset`
  succeeded.
