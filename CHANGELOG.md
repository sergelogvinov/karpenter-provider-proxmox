# Changelog

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
