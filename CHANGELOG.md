# Changelog

## [0.16.0](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.15.0...v0.16.0) (2026-10-31)


### Features

* add e2 CPU share types ([f76894b](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/f76894b6f270f9c5e800a26538d506cd9afc729b))
* drop VM templates with affinity from unmanaged templates without root ([26363e6](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/26363e65b26f328c67ea9fc53933045d788f9e9f)), closes [#298](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/298)
* filter NodeClass offerings by the template's hugepages without root ([8ffcd8a](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/8ffcd8a76ca73cbcc456f98b785200e2b32afe21)), closes [#297](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/297)
* launch without root, leaving affinity and hugepages unset ([2c1d024](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/2c1d024b7aa0c38547fd81a687d900af90f8fa7f)), closes [#295](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/295)
* record template hugepages and affinity in status ([cdf2b66](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/cdf2b667c249e454070c9566bf6a088e1e3eedcb))
* sync the running VMs from the qemu client ([a78d8b4](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/a78d8b46105b54e07e8fb37f2397549b7b7dea55))


### Bug Fixes

* goreleaser ([f82eb47](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/f82eb475fc354b24a95ad39981fada37b80f801a))
* handle each VM start once with the qemu watch ([884d366](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/884d366fd8e8b1c9d9c12e7d9edd01a6632039be)), closes [#306](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/306)
* ignore node-capacity templates and document config updates as PUT ([953da74](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/953da74ee03479289b9ea2ed96843de02f109cda)), closes [#295](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/295)

## [0.15.0](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.14.0...v0.15.0) (2026-10-30)


### Features

* add CPU share instance types ([ad322e8](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/ad322e8c4d6521340e0ecef5b1a3c3d14c7ab919)), closes [#271](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/271)
* add n1 and n2 NUMA types to the default instance types ([cf60068](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/cf6006804001eb6cbd718869906a912060fddfd3))
* add the cpushare generator option and docs ([d2824a9](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/d2824a9a2dbd40012728c85a864aa996ad1f563b)), closes [#273](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/273)
* add the NUMA template generator option ([4dcab3b](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/4dcab3b17ea8c1a5d9b3a3786c53c71bc2ec7117))
* choose the launch zone by fit and placement strategy ([64d294a](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/64d294ab5fabb1c26fddb32ddc169769d0e66f1f))
* keep NUMA types that live NodeClaims still use ([eb882ac](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/eb882ac6877d3c21a122d036b72b8943edfbd3f5)), closes [#276](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/276)
* launch NUMA VMs into a strict NUMA slot ([65b37ba](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/65b37ba7138ffb07384496f21a61db88a3b03e76)), closes [#275](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/275)
* name the zones in the offerings report ([4a73a59](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/4a73a595e826d4f8924dadab46dde168e4c50adc))
* numa slot offerings ([d469b3e](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/d469b3e78bc8e4d0fc2ff2a829ef5e40c5e27bf0)), closes [#274](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/274)
* report the hugepage pool in the node-capacity guest ([ffb19f2](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/ffb19f21ae41523d7ca0db114c4e8612f8d268ea))
* scheduler builds CPU share groups ([dba33e1](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/dba33e1fb44df72b4005b19abef47916b87fc1e6)), closes [#272](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/272)
* scheduler plans through the zone capacity module ([7eb8e81](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/7eb8e81d0d4a483d1574bc8a4a7724adf7439dd6)), closes [#270](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/270)
* skip NUMA slot shapes with less than 1GiB per vCPU ([2d08a8a](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/2d08a8a4d3d96da45dbbaabfbb904f13ebc456ca))
* soft pack regular VMs around NUMA slots ([8f1ca58](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/8f1ca5813152958a217e5ca5175f5f0ccaebfec2)), closes [#277](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/277)
* zone capacity module serves the controller ([48a1563](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/48a1563c390cb52128760f27477e967cad1640f0)), closes [#269](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/269)


### Bug Fixes

* address CPU share types review findings ([9b4cfb2](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/9b4cfb2b72052112a6252fb3af9e15d519cd572b)), closes [#271](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/271)
* address cpushare generator review findings ([6654f70](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/6654f7036326083536184655db090766835a81ff)), closes [#273](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/273)
* disable unsupport feature ([162ea7c](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/162ea7cc6f2ae6b6032bdf29f61e1317fdf38b09))
* drop only the dead process in a rebalance ([d6fae09](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/d6fae096bfa68172813a96f7c843696893bc39b3)), closes [#270](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/270)
* goreleaser ([dfaaacd](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/dfaaacd1b0c0569da06477ffbde7c5bebd5fc535))
* ignore only the node-capacity guest, count unreadable affinities ([8659c1d](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/8659c1df5412828dbfa4add318a0f31e78c1a772)), closes [#269](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/269)
* instance types generation and update offerings logging ([d6cc7a0](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/d6cc7a0f29bcede4a9f867e0c8238387f7fec0a5))
* keep pending reservations across capacity sync ([f5f0f64](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/f5f0f646df665ba4d8394ff714e08c01d2233e01))
* let pinned VMs span NUMA nodes that cannot each hold all their memory ([b70b32d](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/b70b32d7cc8ff3fffde7bab737193e996ea5068f))
* only replace or delete VM templates Karpenter created ([b17e64b](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/b17e64b2f965c1335f0f0c4fe18d776f8fa14cc4))
* remove the cloud-init ISO temp file when building it fails ([f20d54a](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/f20d54a19f2c74f9d44881e02ab4a669e328012a))
* reuse a VM template the cache has not listed yet ([9439ac8](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/9439ac811251043d39249742b301bb2b161a88a9))
* roll back every side effect of a failed instance launch ([16a9c54](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/16a9c54055ac34823c125d7e1e8cba42a3a32ac2))
* scheduler keeps its behavior on the zone capacity plan ([63f5712](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/63f57126bbcf46ec05fe12b0fcc9fad9aaf7ed6e)), closes [#270](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/270)
* spread pinned CPUs by free NUMA memory when packing does not fit ([d72744b](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/d72744b3644a0ae75f6773937856ecf80a4a1869))
* zone capacity module review findings ([f32bf0c](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/f32bf0c070b1bcf8ebd5427e001ae3bd0b0bf492)), closes [#269](https://github.com/sergelogvinov/karpenter-provider-proxmox/issues/269)

## [0.14.0](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.13.0...v0.14.0) (2026-10-27)


### Features

* reserve first subnet ip in node ipam ([636ed59](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/636ed5967f2a0bd5db11949628c49eb09a3181a5))
* support configurable shared CPU overcommit ([7b0573c](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/7b0573ce997f1fcb1681583cf4aa8f2df583aef2))


### Bug Fixes

* deadlock deleting a template from one zone twice ([e8ef1bf](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/e8ef1bf606c2c2770b015fb6e87f2b39ab8d7b86))
* delete VM templates of zones a template no longer targets ([a102533](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/a1025334cda0960530709080cb823b500a4f2dde))
* goreleaser ([51f9b56](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/51f9b5665a55d55950aa0a97ddf7fc150cb1570d))
* keep placements of failed zones and finish in-place updates ([1dc7a44](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/1dc7a4477a47040ad3872a0108af6bf7c1a9521f))
* keep template placements when deletion races create ([322bc83](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/322bc83d21fa4c03b0ea909747be7115bf924489))
* proxmox-scheduler ([3ab0fb8](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/3ab0fb854376cb08ff44439609c47ee915c5fe8a))
* update build version to include SHA in the Makefile ([850fa88](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/850fa88b0a87fe24f16f05d07bb19a4f0a5f587f))

## [0.13.0](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.12.0...v0.13.0) (2026-09-27)


### Features

* add proxmoxNodeClass and related tests ([022007b](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/022007bbb14ade6625f10e4e7529ae1c4990e251))
* e2e test template ([17d1f75](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/17d1f75a11a05379774c27ff6bb179d20c820764))
* enhance e2e lifecycle tests ([16508f2](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/16508f2c304eb062fd584f279bfb0fcc7deba71f))
* enhance VM scheduling with shared CPU policies ([283d31f](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/283d31fd6e0ab9eef63eddd621e4e3dbc34ba7d3))
* implement HA maintenance node handling in capacity sync ([d7c543e](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/d7c543e4e965d170f5c5c8087021881e465c01d3))
* refactor Proxmox scheduler to use go-proxmox-local ([e2faff8](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/e2faff85a4d72ab572e9e4d0f77d676d517fce25))

## [0.12.0](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.11.1...v0.12.0) (2026-07-19)


### Features

* add onBoot to ProxmoxTemplateSpec ([74758b6](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/74758b60e9d78eb6636b4dd8aab933987b168b0c))

## [0.11.1](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.11.0...v0.11.1) (2026-07-10)


### Bug Fixes

* vm affinity pinning ([e868773](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/e8687734a7ed6062d63fca3b93b316977104e8f6))

## [0.11.0](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.10.1...v0.11.0) (2026-04-01)


### Features

* introduce advanced VM tracker ([e2e6eab](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/e2e6eabdbb451e862136df0758f9e9e4ff44daf0))


### Bug Fixes

* release ip ([830d0c4](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/830d0c44bf2ef0c1c614055a74b9613fa5b7c9d7))

## [0.10.1](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.10.0...v0.10.1) (2026-02-16)


### Bug Fixes

* goreleaser ([af88bab](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/af88babfb739d51065006ed984077d90e3f3c8f4))

## [0.10.0](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.9.0...v0.10.0) (2026-02-16)


### Features

* gather nume memory topology ([a33085d](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/a33085d396cd8648f5d7a51a968962b2e62a0511))
* proxmox sheduler ([421335e](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/421335ee7190d1185b4c543ba1cc6c4fd41b09a3))


### Bug Fixes

* skip lxc containrs ([14af1b1](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/14af1b17d94cd38fc12ec6e8704210803be96e40))

## [0.9.0](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.8.0...v0.9.0) (2026-01-25)


### Features

* enhance cpu management and node resource reservation ([7cfea7f](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/7cfea7f16e96e0e6ad5562af94d4dbe1e0915185))
* hugepages in instance types ([14b02e6](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/14b02e62a4762814343d3260f55ae91421946c6d))
* static policy allocation ([fa66916](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/fa6691670e13bcda8e62cf5d486dad74909fb711))
* static policy allocation ([ae721bb](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/ae721bbbea55129a72ea50b62c5b6a1ca93d59e6))
* support numa topology in node settings ([6f25278](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/6f2527838752b4cc31a8c6179eb1514d64385b97))


### Bug Fixes

* error handling in capacity management ([17f2156](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/17f21568e65728352c6eb567a7df3cc0d1ea8a7b))
* ipam contoller ([93c75ac](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/93c75ac72d87b30fb6e8169b27a9a0f193a96a46))
* proxmox node topology prediction ([b3a9867](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/b3a98670a2553c58564c6aba37d8e9176e2f1916))

## [0.8.0](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.7.2...v0.8.0) (2026-01-11)


### Features

* add bios and tpm configuration ([62d2a15](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/62d2a15d3bfc265aca5d8bbff3ce0d5283e18ecb))
* add resource pool support for ProxmoxNodeClass ([abb423e](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/abb423ef53da7c02d7e1c3e077fa8453f495bac6))
* inplace VM pool membership update ([cd4740a](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/cd4740a376517aa8b5c38a766b58122ec7b676f6))
* vm templates in-place update ([835f276](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/835f276711928487670629e71b12a67456216226))


### Bug Fixes

* add required taint to the kubelet configuration ([a6b2ce6](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/a6b2ce60639a907bc135e29014d2a3017739264b))

## [0.7.2](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.7.1...v0.7.2) (2026-01-04)


### Bug Fixes

* download images ([6753c5d](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/6753c5d921e458473516a4afb7ecf4d954cb9681))

## [0.7.1](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.7.0...v0.7.1) (2025-10-27)


### Bug Fixes

* continue node scanning even if some nodes are inaccessible ([a823d87](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/a823d87f2c6fba5bdfb2efd87d89cc34cfc698dd))
* default mtu size ([375eacf](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/375eacf56572dbd246a4f0a7ad0e5bd7973881b2))
* find instancetype by capacity ([4f97f52](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/4f97f52a0e82fe12043975833c572e145d023ee2))
* ipam subnets do not exists warning ([905e1bf](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/905e1bfffdccd6747482ec0fa5a503650488a6f6))
* network trunk and mtu options ([f564db2](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/f564db2df553d7430fdbe7b11c7b4c42435b2721))
* set default gateway as cidr ([ecfb26d](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/ecfb26d01bcf4670ee361a9ca5ffe6a2e61eb74a))

## [0.7.0](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.6.0...v0.7.0) (2025-09-21)


### Features

* simple ipam ([22aecbd](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/22aecbd1f6db0c424dc9be134ca93e80a5ada3a1))
* template options agent, ipconfig, hostpci ([1fe7495](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/1fe74955b1b77204bc350a6320744e947265155e))

## [0.6.0](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.5.0...v0.6.0) (2025-09-15)


### Features

* add hasTag func ([0f34c30](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/0f34c30930ef97fa6cdc17c74eab67793c1aeae3))
* expose proxmox host bridge ips ([5333bcc](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/5333bccd239262e8514f6fc061d1bd82d97d24ee))
* network config in user-metadata template ([0011441](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/001144120b25cda70f672ad7b9eae6a82bb7bdbc))
* search template by tags ([8eb4906](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/8eb49068607f8582c01b34314ca1242be07466dc))


### Bug Fixes

* check storage for nodeclass status ([a887794](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/a887794bc56b79a0c314dd2feabc6acade3dc137))
* user-data template ([0c3c52e](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/0c3c52e8d7136c8d5bc3d5ac69555df8915eec2a))

## [0.5.0](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.4.0...v0.5.0) (2025-09-08)


### Features

* api credentials as files ([93e21c1](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/93e21c12d7ccdaf5b546aad408592351435e79dc))
* detach cloud-init iso ([e2e4419](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/e2e441917610ac56ad84caffd39ffc0441d88fdc))
* in place update ([a0cad4d](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/a0cad4da3efc4792fbca3a9ee159c03e41ca30cd))
* kubelet bootstrap token ([384b22c](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/384b22c71cf357b1e0e9e331143c146dcce3a3f4))


### Bug Fixes

* **chart:** rbac permission for provider ([afc83e1](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/afc83e1c247aec263b007caf6e7b2fe0f66458e8))
* **chart:** writible folder ([8beb809](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/8beb809bb9b24d5da1e3223d8e678402d082491f))

## [0.4.0](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.3.0...v0.4.0) (2025-09-01)


### Features

* add custom values to cloud-init templates ([e813c30](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/e813c300a10b31d73026cf8bd05b1dc679552994))
* additional node labels ([b2db7ea](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/b2db7ea69f912e438f89dc5c73b66d8ed08859cc))
* custom instance types ([c495448](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/c495448d03e35e29bcb00984bba2587b8f7f7d9b))
* remember proxmox virtual machine last id ([5db4e02](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/5db4e02d073dbb423b855832d0642a24ebd0da14))


### Bug Fixes

* **chart:** rbac permission for crds ([e1eb558](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/e1eb558b9e68826c11c02de8d9944a419d19388c))

## [0.3.0](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.2.0...v0.3.0) (2025-08-26)


### Features

* add proxmox vm-template controller and related configurations ([872beb6](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/872beb6a1f030057dc42c4f7a6684be53f008cb2))
* drift instances ([c8afc81](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/c8afc818451771dde74f4f4c947b04ba046a2952))
* kubelet reserved resources ([84d45ae](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/84d45aef7f0c769a1b969d5fe0ae06b8fd3d8f68))
* nodeclass validation ([71394c4](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/71394c4c9752dda34dcf603b8b7484faf47d9f7d))

## [0.2.0](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.1.0...v0.2.0) (2025-08-18)


### Features

* add devcontainer for Go development ([7df658f](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/7df658f3e2b1744aeaf2cc6d09b88b470d83a18a))
* cloud-init as kubernetes secret ([cfe328f](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/cfe328f600388aaab4e80e1fd8bb0ceef14f78fc))
* instance template status ([58d78b7](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/58d78b7c69e48553baffdd589c94ae9c147bc834))
* kubelet config file ([9eed04f](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/9eed04f36e1c2fee48bffa8c3671cb00504ffa20))
* regional schedule ([bf9431c](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/bf9431c2fbc9cad38e10274e2d944b3ff59d06ab))
* security groups ([0fd72ac](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/0fd72ac4742c41561ec3cc9a9089c7f944ed3daf))

## [0.1.0](https://github.com/sergelogvinov/karpenter-provider-proxmox/compare/v0.0.1...v0.1.0) (2025-08-10)


### Features

* add cloud-config param ([bf30a99](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/bf30a991902031ee194579b47b4f2d49aa71b9ae))
* add nodeclass controllers ([88311b2](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/88311b2d0989f8f87a1abb48550b2dfa0bb265fa))
* **chart:** add helm chart ([7b70ad6](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/7b70ad613cb39d4a932259960259a51076ad8caf))
* initial commit ([074f1e3](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/074f1e3185ac4fb40cb6127d7d8340e04c6682dd))
* multi zone support ([a5c52a8](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/a5c52a868a10c146d1e77a0386b9918358a89aa0))
* simple create delete instances ([75d2146](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/75d214662e1d358afd07f91980f4816d4dd17b57))
* skaffold project ([0027deb](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/0027deba1cbeda45c024f13996acd297c18223fc))
* update documentation ([8dde932](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/8dde93208569cb15d787c8892cd3a80f53067214))
* update plugin crd ([be189f7](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/be189f73a92fb50f5eb6f823e676d18e054614a6))


### Bug Fixes

* multi zone support ([ac3e2f1](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/ac3e2f17b00a803c78af1e82af788cce9d9ad007))
* scale up and down nodes ([67fcdf4](https://github.com/sergelogvinov/karpenter-provider-proxmox/commit/67fcdf4d0d589c6126b775cba4095730026bc3e9))
