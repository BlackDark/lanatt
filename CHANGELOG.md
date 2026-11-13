# Changelog

## [0.5.0](https://github.com/BlackDark/lanatt/compare/v0.4.0...v0.5.0) (2026-11-13)


### Features

* **daemon:** allow SLAAC alongside static IPv6 ([3512d72](https://github.com/BlackDark/lanatt/commit/3512d72fa4436c05e8f8e6713a32e478774fd51d))

## [0.4.0](https://github.com/BlackDark/lanatt/compare/v0.3.0...v0.4.0) (2026-11-13)


### Features

* **claims:** record each claim's parent for MAC checks ([#55](https://github.com/BlackDark/lanatt/issues/55)) ([e1382e9](https://github.com/BlackDark/lanatt/commit/e1382e9796dec9f78f876b206b70a1211b2dbb60))
* **daemon:** keep devices on record until removal is verified ([#56](https://github.com/BlackDark/lanatt/issues/56)) ([dcc0ad0](https://github.com/BlackDark/lanatt/commit/dcc0ad023bece8d4682064bc48ff24856ed5c12d))
* durable CNI attachment journal with reconciliation ([014388d](https://github.com/BlackDark/lanatt/commit/014388d45fb548cfe6de18500cefdd0dcb8fefd3))
* ipvlan L2 network driver ([99df498](https://github.com/BlackDark/lanatt/commit/99df49860d6ad90a06f9f569cb3a7d92dd3eac3b)), closes [#21](https://github.com/BlackDark/lanatt/issues/21)
* lanatt claims lists claims with liveness ([1627ae7](https://github.com/BlackDark/lanatt/commit/1627ae7c4513c1057f1c16c6156584ef6cb18c74))
* noSLAAC option for IPv4-only networks ([#54](https://github.com/BlackDark/lanatt/issues/54)) ([e334812](https://github.com/BlackDark/lanatt/commit/e3348127ceb3391e5ea6cbc91bd8e084c53dd663))
* opt-in static ULA IPv6 on networks ([#45](https://github.com/BlackDark/lanatt/issues/45)) ([4c11ce4](https://github.com/BlackDark/lanatt/commit/4c11ce484f87ed05ed0af50e782255a1d4530bb6)), closes [#19](https://github.com/BlackDark/lanatt/issues/19)


### Bug Fixes

* claims live only while their owner process holds the netns ([320713b](https://github.com/BlackDark/lanatt/commit/320713b820ff884de03411689db5d7c16d1b59d8))
* **claims:** refuse a MAC held by another network on the same parent ([#53](https://github.com/BlackDark/lanatt/issues/53)) ([4516f08](https://github.com/BlackDark/lanatt/commit/4516f0834ec612063372f8f5225e32ad1c8be88b))
* **config:** refuse overlapping IPv4 prefixes on one parent ([#46](https://github.com/BlackDark/lanatt/issues/46)) ([a8428cf](https://github.com/BlackDark/lanatt/commit/a8428cf7fe4b7093e1f5cea202f6c337b2b4e4e4))
* refusals name the claim holding the address or MAC ([3a21634](https://github.com/BlackDark/lanatt/commit/3a216344f7aa9d54cff12b912c7a54276eff2fda))


### Refactoring

* claimlist uses claims.ShortID ([bc509e5](https://github.com/BlackDark/lanatt/commit/bc509e5c543a4b96bc9a58620c139c654de5fa79))
* **claims:** clarify conflicting join identity ([172a4fc](https://github.com/BlackDark/lanatt/commit/172a4fc3bb23a7922a13e8993efe3b2df5619e55))
* **claims:** separate planning from persistence ([6a92272](https://github.com/BlackDark/lanatt/commit/6a922729c2ef08f47e3076ab4095c5b7b862436f))
* **daemon:** keep reservations with their attachment ([#60](https://github.com/BlackDark/lanatt/issues/60)) ([70f9104](https://github.com/BlackDark/lanatt/commit/70f91047edc8ef663882ee310c0d1ef45a82ac71))
* **daemon:** separate default route handling ([56096d7](https://github.com/BlackDark/lanatt/commit/56096d753083ae8da89ec28966e80a9a5b6062df))
* use encoding/json/v2 ([#57](https://github.com/BlackDark/lanatt/issues/57)) ([4cc0cc1](https://github.com/BlackDark/lanatt/commit/4cc0cc1ee413464c572d9dc38e91913f6bf314ac))


### Documentation

* CNI delegation research and implementation tickets ([e1611e3](https://github.com/BlackDark/lanatt/commit/e1611e3242a083a582b21142b28c099663c5556d))
* CNI plugin support across engines ([37137d6](https://github.com/BlackDark/lanatt/commit/37137d623a430231e32d65d93f0a66b679964ae6))
* containerd NRI research for lanatt hooks ([3d52304](https://github.com/BlackDark/lanatt/commit/3d52304c77f813d0ce77e7007c8d074d83b6700d))
* host shim example so the host reaches its attachments ([f28531c](https://github.com/BlackDark/lanatt/commit/f28531c21c2f1478917f7c2745c71efaf61a92fa))
* performance notes and Podman networking references ([8a8cd02](https://github.com/BlackDark/lanatt/commit/8a8cd02772a0561ab59f762f7b0bd712a1dbe57f))
* remove CNI delegation research, keep it in [#25](https://github.com/BlackDark/lanatt/issues/25) ([e1395e3](https://github.com/BlackDark/lanatt/commit/e1395e392cc104fb31175f7dfb152b923c50f1ae))
* remove research notes, keep findings in issues ([ed739a5](https://github.com/BlackDark/lanatt/commit/ed739a5c937f8bf1e6104e0cd7e4cd379e199e9a))

## [0.3.0](https://github.com/BlackDark/lanatt/compare/v0.2.0...v0.3.0) (2026-11-02)


### Features

* attach static binaries to each release ([5f1a1eb](https://github.com/BlackDark/lanatt/commit/5f1a1ebb0078ae118249177adc45a01a152a74e9)), closes [#16](https://github.com/BlackDark/lanatt/issues/16)
* networks can limit the addresses they hand out ([6b246e9](https://github.com/BlackDark/lanatt/commit/6b246e900caec9a70d6601bfdc2b918a8992d594)), closes [#18](https://github.com/BlackDark/lanatt/issues/18)
* run the daemon container without --privileged ([995ab84](https://github.com/BlackDark/lanatt/commit/995ab849aeb8ea2635367cf84ef621502c9fddc1)), closes [#17](https://github.com/BlackDark/lanatt/issues/17)


### Documentation

* release PR runs always need approval, approve only the newest ([b811c6c](https://github.com/BlackDark/lanatt/commit/b811c6c49e517c793f2a2dc1a8b9f9ec3586ce51))
* usage example joins an address inside the range ([3ae468d](https://github.com/BlackDark/lanatt/commit/3ae468d5532e02f3caa5183f6742d7cac173d3fe))

## [0.2.0](https://github.com/BlackDark/lanatt/compare/v0.1.0...v0.2.0) (2026-11-01)


### Features

* allowRoot lets rootful containers join networks ([b6f60bc](https://github.com/BlackDark/lanatt/commit/b6f60bcca5d0722a2f8d64babeb9f8c1957d8944)), closes [#4](https://github.com/BlackDark/lanatt/issues/4)
* lanatt-&lt;runtime&gt; picks the OCI runtime it wraps ([91b1f9c](https://github.com/BlackDark/lanatt/commit/91b1f9c78d4b522370121bca60fe7acfc1c8225a)), closes [#2](https://github.com/BlackDark/lanatt/issues/2)


### Documentation

* drop the release-as note ([cfa67cf](https://github.com/BlackDark/lanatt/commit/cfa67cf2675d28fa66c9870f8c2ce252b3f0e037))

## 0.1.0 (2026-11-01)


### Features

* join a workload netns to admin-defined networks ([0e37cd6](https://github.com/BlackDark/lanatt/commit/0e37cd68f1f87d0842c32af09ef12252e3178728))
* lanatt-runc runtime wrapper for Docker ([e6ffae5](https://github.com/BlackDark/lanatt/commit/e6ffae5ccd0eca052b56b71a72a794676de1c5e5))


### Bug Fixes

* delete leftover temporary macvlans when the daemon starts ([372d5f3](https://github.com/BlackDark/lanatt/commit/372d5f3eae1dc992607343c6575a43144d846354))
* refuse a config with an empty allowlist ([1912050](https://github.com/BlackDark/lanatt/commit/1912050a6afd152c5d4cc840f952c53c128b6d2f))


### Documentation

* engine wiring, hooks_dir and the Docker runtime ([043024b](https://github.com/BlackDark/lanatt/commit/043024bc1b5b0d73406a275aeb75867af892e7ee))
* README, container and Podman hook usage, agent map ([5d7800d](https://github.com/BlackDark/lanatt/commit/5d7800d1d27d2c9a396fa3dbc119e0db47a34719))
* record how a rootless workload joins a network ([acbe87d](https://github.com/BlackDark/lanatt/commit/acbe87dd7acacd8b5628af1c6217e159774c0e96))
* record how claims are owned, pruned and cleaned up ([6ecb10a](https://github.com/BlackDark/lanatt/commit/6ecb10a4f8f9ada63984501599ddc3a97de6def7))
* record why Docker uses a runtime wrapper, not a plugin ([106fa63](https://github.com/BlackDark/lanatt/commit/106fa6348613100eec92ac3acd9f7beb0e9853ef))
* restructure README ([d8469c3](https://github.com/BlackDark/lanatt/commit/d8469c3014d2c36e6588a96022d82fdc29af32f0))
