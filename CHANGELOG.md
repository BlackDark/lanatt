# Changelog

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
