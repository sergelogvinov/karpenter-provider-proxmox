# The family digit encodes the fraction, and NodePools select only on the family

The NUMA families are `n1`, `n2` and `n4` (a whole, half or quarter NUMA slot), and the CPU share families are `e2`, `e3` and `e4` (two, three or four seats in a CPU share group). The digit after the letter is that fraction, not a generation as in `c1` or `m1`. NodePools pick these instance types with the existing `instance-family` label only; no `instance-numa` or `instance-cpushare` label is added. What makes an instance types file entry a NUMA template or a CPU share type is its `numa` or `cpushare` field, never its name, so an operator may call the families anything.

## Considered Options

- One family per feature with the fraction in the name or a label (`n1.half-16VCPU-63GB` plus `instance-numa=half`, `e1` plus `instance-cpushare=2`): rejected, it gives NodePools two ways to say the same thing, and a label per feature grows with every new family.
- Parse the fraction from the family name (`n2` means half): rejected, names are free-form and parsing them breaks as soon as an operator renames a family.

## Consequences

Instance type names are stamped on nodes and compared by drift detection, so renaming a family later replaces every node of it. The digit means two different things across families (`c1` a generation, `n2` a fraction); readers have to know which family they are looking at.
