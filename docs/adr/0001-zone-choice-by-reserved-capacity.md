# Zone choice ranks by reserved capacity, not live CPU load

Zone choice orders a launch's candidate zones by fit, worked out from zone capacity (what is reserved and tracked in each zone), and the `Balanced` placement strategy means "the zone with the most left over after this VM". The live CPU load Proxmox reports per node (`CPULoad`) used to be the `Balanced` ranking, and is now kept for status and logs only: it is sampled at capacity sync, so pending reservations don't move it and a burst of launches all lands on the same "least loaded" zone, and it is a second model of the zone that can disagree with what zone capacity will actually admit.

## Considered Options

- Keep `Balanced` on `CPULoad` and add `BestFit` on fit: rejected, it keeps two models of a zone behind one setting.
- Blend `CPULoad` into fit as a tie-breaker: rejected for now, it brings back the sync-lag problem for exactly the bursts where ties are common.

## Consequences

`Balanced` changes behaviour on upgrade: a zone whose VMs are busy but small is no longer avoided, and a zone with large idle reservations is.
