# Changelog

## [0.13.2](https://github.com/wilke/GoWe/compare/v0.13.1...v0.13.2) (2026-07-07)


### Bug Fixes

* **release:** per-binary archives so the release produces assets ([#152](https://github.com/wilke/GoWe/issues/152)) ([f531de5](https://github.com/wilke/GoWe/commit/f531de537b9a6e652527cc9fb1bbf0d3eed1ddd6))

## [0.13.1](https://github.com/wilke/GoWe/compare/v0.13.0...v0.13.1) (2026-07-07)


### Bug Fixes

* **release:** build gowe-worker for linux only ([#150](https://github.com/wilke/GoWe/issues/150)) ([89fce8b](https://github.com/wilke/GoWe/commit/89fce8ba681f3213917a2178374e9b8c37b3a6fd))
* **worker:** make total-memory detection cross-compile (unbreak release binaries) ([#151](https://github.com/wilke/GoWe/issues/151)) ([265ff51](https://github.com/wilke/GoWe/commit/265ff51410886beb5b41533d847b40d7a88e9c63))


### Security

* **tokencrypt:** bind at-rest token ciphertext to its row via enc:v2 AAD ([#147](https://github.com/wilke/GoWe/issues/147)) ([999ac96](https://github.com/wilke/GoWe/commit/999ac96d252d3c2d6c9607125d3a860bc9f1b42d))
* **worker-keys:** stop PATCH clobbering last_used_at; test admin gate ([#148](https://github.com/wilke/GoWe/issues/148)) ([3c81747](https://github.com/wilke/GoWe/commit/3c81747bf1b8d224ede4814adee6051c18e21ded))

## [0.13.0](https://github.com/wilke/GoWe/compare/v0.12.0...v0.13.0) (2026-07-07)


### Features

* add --env-file flag for non-secret container env vars ([d56a721](https://github.com/wilke/GoWe/commit/d56a72146cd22c32562cddfa952724d28ad3c9e4))
* add --output-destination flag to gowe submit ([d841c33](https://github.com/wilke/GoWe/commit/d841c332d6a240dadb6626b5fd92377ac1f020c1))
* add --workflow flag, fail submissions on output staging errors ([35071c8](https://github.com/wilke/GoWe/commit/35071c82d39afebee15dab41b79d788825422f90))
* add /workflows/{id}/inputs and /outputs convenience endpoints ([fe7c107](https://github.com/wilke/GoWe/commit/fe7c107e25953481eac3b653639efc52492e9b2b))
* add make dev target and enable workspace staging on server ([6f2911a](https://github.com/wilke/GoWe/commit/6f2911a097c1546e0546fcda1e6489cb86289131))
* add workflow labels with controlled vocabulary ([126b60b](https://github.com/wilke/GoWe/commit/126b60b11b592472d771b3922b70ecaf2dd740f4))
* admin active tasks view (API + UI) ([833bdae](https://github.com/wilke/GoWe/commit/833bdae0c7978db44724e1cc6fc957ed2ef7a30c))
* auto-create workspace directories on output staging ([9f71f11](https://github.com/wilke/GoWe/commit/9f71f11ed7f1d5379c8f5853cc85a8bbe4e6094d))
* blast-protein-search workflow via gowe://Homology ([ffc8f35](https://github.com/wilke/GoWe/commit/ffc8f35dafa45106778066c994bc42ab6d634cd6))
* BV-BRC executor populates task outputs from job result ([21e7e09](https://github.com/wilke/GoWe/commit/21e7e090ffd42e2e1abff0cf6587f5225f7d982f))
* CLI --workspace-upload flag for BV-BRC input staging ([b19742a](https://github.com/wilke/GoWe/commit/b19742a2e30c9dc4777363d30736b02ca96a5fe0))
* conditional container runtime registration and worker health summary ([585a7af](https://github.com/wilke/GoWe/commit/585a7afa0ec00ffd57d840a8c44912d2695b2c8c)), closes [#16](https://github.com/wilke/GoWe/issues/16)
* genome analysis pipeline (CGA → PhyloTree + cgMLST + WG-SNP) ([57ff1ca](https://github.com/wilke/GoWe/commit/57ff1ca5533001eb945e73ec804f5325d72dd05a))
* GPU workers prefer GPU tasks over CPU tasks during checkout ([91b08b7](https://github.com/wilke/GoWe/commit/91b08b718d7a23632fd860bc272af3050a53585a))
* GPU-aware task scheduling ([79ac2e9](https://github.com/wilke/GoWe/commit/79ac2e9513391abd156b3f9154da0301f3c37a7d))
* output catalog for BV-BRC apps and typed CWL output generation ([85faa2e](https://github.com/wilke/GoWe/commit/85faa2ef74b4af3b712fafbca6d0f0c57fa4a459))
* PATCH /workflows/{id}/labels endpoint and BV-BRC filter in UI ([20e28ad](https://github.com/wilke/GoWe/commit/20e28adbe61276730033ad8ec839f8873c68bd03))
* per-user submission isolation and workflow creator tracking ([f5fc72b](https://github.com/wilke/GoWe/commit/f5fc72b148e44e40db6481e7be9f91bcc7969f7e))
* per-user submission isolation and workflow creator tracking ([2071faf](https://github.com/wilke/GoWe/commit/2071fafe6bd9ddf61497d7f8053c280146182298))
* return exit code 33 for InplaceUpdateRequirement in server modes ([56a7c2b](https://github.com/wilke/GoWe/commit/56a7c2b9f00b9b2ea79f0427bcb0ad44039c8f51))
* **security:** per-worker keys — issuance, revocation, hashed-at-rest ([#139](https://github.com/wilke/GoWe/issues/139)) ([0fbbbb5](https://github.com/wilke/GoWe/commit/0fbbbb5a1b9b48ca3ac42c53ee3889e59a83544c))
* **server:** native TLS and TLS-aware Secure session cookies ([#136](https://github.com/wilke/GoWe/issues/136)) ([154c3e9](https://github.com/wilke/GoWe/commit/154c3e930bcef4e4135f2f2eb5de3f11617eb541))
* show creator on workflow cards and role badge in navbar ([c9f728e](https://github.com/wilke/GoWe/commit/c9f728e077950ea214c22c44d809300556888638))
* stuck task error propagation, retry endpoint, no wasted retries ([16e050b](https://github.com/wilke/GoWe/commit/16e050b25342a312b0aeca00b9a7ae08430a2926))
* stuck task error propagation, retry endpoint, no wasted retries ([707f5aa](https://github.com/wilke/GoWe/commit/707f5aad9a43a5fcc8bea9c8de7b177b34447388))
* support gowe:// references in workflow registration ([e3afd3a](https://github.com/wilke/GoWe/commit/e3afd3a2b751c24c8727ef7416f3a8abed963cee))
* support gowe:// URI references in workflow registration ([932ae16](https://github.com/wilke/GoWe/commit/932ae1691bfc1de23c7f8b09cd40ac7a8a9900cc))
* task priority for queue ordering ([84e7273](https://github.com/wilke/GoWe/commit/84e727326a070725857152a6e4a5b217eba7e935))
* typed CWL outputs for BV-BRC apps and executor output mapping ([11d918b](https://github.com/wilke/GoWe/commit/11d918bc130091df831e0562b289e6210290ba4e))
* typed outputs for BV-BRC analysis tools, inject_bvbrc_token hint ([2a3fd9f](https://github.com/wilke/GoWe/commit/2a3fd9f7d8b90bc6e5a3b49aa3fb12dc12987d4f))
* **ui,worker:** submission delete, workflow edit, log capture, scoped token injection ([#132](https://github.com/wilke/GoWe/issues/132)) ([6da014f](https://github.com/wilke/GoWe/commit/6da014f5f5ff67af58e1dd45a849ebe4c7f64d19))
* unified table navigation — search, sort, pagination across all list views ([e29d498](https://github.com/wilke/GoWe/commit/e29d498576e89b0b64a529b4994e085189369665))
* unified table navigation across all list views ([409667c](https://github.com/wilke/GoWe/commit/409667c46655d58945a7033f6debcc8354c69532))
* upload output manifest to workspace after staging ([3509082](https://github.com/wilke/GoWe/commit/35090824984715c141d2de613e2ac9c10ecc3d10))
* upload output manifest to workspace after staging ([79c3449](https://github.com/wilke/GoWe/commit/79c3449d5cc066a369d89776e5750ccafea13d17))
* **validator:** reject shell-style ${...} in outputBinding glob/outputEval ([f216c5b](https://github.com/wilke/GoWe/commit/f216c5ba1f69efe1882aa2f1d3a7429703100e50))
* wire query parameter parsing into all API list endpoints ([e43eee2](https://github.com/wilke/GoWe/commit/e43eee2fecc993234f1c5a8477a99a672eb66a18))
* wire query parameter parsing into API list endpoints ([8866a4c](https://github.com/wilke/GoWe/commit/8866a4cb69ac88087c82d1fbdcf16574b352af78))
* worker improvements, workspace staging, and full CWL v1.2 conformance ([cbdb469](https://github.com/wilke/GoWe/commit/cbdb4690c4f9fca690880f35c294a614f510d36b))
* workflow labels with controlled vocabulary ([6783f91](https://github.com/wilke/GoWe/commit/6783f91472a7ee136b98e1d4c00d286ef5d24592))


### Bug Fixes

* address PR review — Class comment, redundant search parse, filterQuery docs ([1892e69](https://github.com/wilke/GoWe/commit/1892e69183d51632354ecc2b451f27f7c12d104f))
* address PR review — deterministic graph order, ensureDir error handling ([c890ca3](https://github.com/wilke/GoWe/commit/c890ca3456b3666ae0a467e7ac23d91aa463b6a2))
* address PR review — json_each filter, key validation, URL escaping, test checks ([fd4cf2a](https://github.com/wilke/GoWe/commit/fd4cf2a12c534bb8a615f837eba9842ad25e85a9))
* address PR review — limit clamping, paged task tests, X-Max-Limit header ([9d11958](https://github.com/wilke/GoWe/commit/9d11958ef3d047b996656eec2878bc83bf70b71d))
* address PR review — request context, nil slices, worker summary test ([bde15eb](https://github.com/wilke/GoWe/commit/bde15eb3ae127484a881654f02d06d18c33fddc7))
* address PR review — restore API field, check marshal errors, add tests ([5f35e1d](https://github.com/wilke/GoWe/commit/5f35e1d8e920b0f649e207b42b22b52e7cbbaf31))
* address PR review — token sources, envelope note, error handling, argparse ([895efca](https://github.com/wilke/GoWe/commit/895efca1588574ea42dbd6b043221b2fbf10a193))
* allow literal File/Directory objects without path/location in validation ([804e929](https://github.com/wilke/GoWe/commit/804e929ba5900930fcdccad7bd6d45e4805b68a6))
* **bvbrc:** flatten File/Directory nested inside group/record params ([772f1ab](https://github.com/wilke/GoWe/commit/772f1abc334661aab3f27673e0d3b4b25a93c806))
* **bvbrc:** flatten File/Directory nested inside group/record params ([aa790cd](https://github.com/wilke/GoWe/commit/aa790cd2a72086d8049ff7e551c3842fd77295ea))
* cache URL query parse and hoist search normalization out of loops ([912ee40](https://github.com/wilke/GoWe/commit/912ee409bb04f9cfd1f1a25ccc915d6c0b492b22))
* **cli:** only skip upload for remote-URI directories, not file:// ([ba3bc5e](https://github.com/wilke/GoWe/commit/ba3bc5ea9e5ee83bb1d98ddd834fb87dfef2b60e))
* **cli:** only skip upload for remote-URI directories, not file:// ([6cbb606](https://github.com/wilke/GoWe/commit/6cbb606b490a6d980e8168e5a3ee3dc73d61d2a1))
* **cli:** preserve ws:// Directory location during input upload ([7f37b44](https://github.com/wilke/GoWe/commit/7f37b4406b113df0bc44f3a610a9ec672b6cdf02))
* **cli:** preserve ws:// Directory location during input upload ([35aadd9](https://github.com/wilke/GoWe/commit/35aadd9f597a13e03d435ae060cf90665bde7683))
* compute task_summary in API submission endpoints ([1052c0b](https://github.com/wilke/GoWe/commit/1052c0b1fd8079b96be8b416b4ff1fbbdfcec70f))
* container runtime availability checks & health endpoint worker summary ([1a0abca](https://github.com/wilke/GoWe/commit/1a0abca2d1d91a5f31d1866a8e55edf5c04baac7))
* default executor is now fallback, not override ([d75c21f](https://github.com/wilke/GoWe/commit/d75c21f055a52240a9424628cb256e4440fad85d))
* don't finalize submission while tasks have retries remaining ([8d566c7](https://github.com/wilke/GoWe/commit/8d566c74142ae93c28995444660c50167f1ae414))
* fetch BV-BRC job logs via task_info REST endpoint, strip null inputs ([336de46](https://github.com/wilke/GoWe/commit/336de469849feca2a05963f72a094bbc321e30cb))
* harden error handling, add per-tick caching, batch DB operations ([7f60343](https://github.com/wilke/GoWe/commit/7f60343fae2bd7ead3ea608d6b955b60359a458f))
* harden error handling, add per-tick caching, batch DB operations ([99871c8](https://github.com/wilke/GoWe/commit/99871c8ba8bd99249c09463b4ea10e97e9ebf4aa))
* Homology.cwl output globs and BLAST tuning params ([884ceb4](https://github.com/wilke/GoWe/commit/884ceb48f6b0b3b902d6bdf26fff3d5cf3610f51))
* **Homology:** output_path string type to bypass bundle ws:// mangling ([03df2ec](https://github.com/wilke/GoWe/commit/03df2ec1b5d99dc44e3a186bbbc70204f115171b))
* ignore all ensureDir errors during workspace output staging ([6e50fd0](https://github.com/wilke/GoWe/commit/6e50fd0c0cc85bee38b8b94b93972558cc178bd6))
* pass user token and output destination from UI submissions ([609bfa3](https://github.com/wilke/GoWe/commit/609bfa369dd6562b1103afb64543feed710d603f))
* re-mount DAG component after HTMX content swap ([aefa35b](https://github.com/wilke/GoWe/commit/aefa35bfeb0cc1b910dbb91d44febebffe04c299))
* reject File/Directory inputs with no path or location ([86c9d04](https://github.com/wilke/GoWe/commit/86c9d042efb43ee0ba52a0b77a9f23f19bbd104f)), closes [#99](https://github.com/wilke/GoWe/issues/99)
* replace deprecated PhylogeneticTree with CodonTree + PGFam wait gate ([aa981ea](https://github.com/wilke/GoWe/commit/aa981ea7131e9e356ad1e7f763016370960bdc3b))
* resolve File inputs to path strings in BV-BRC executor ([f73c971](https://github.com/wilke/GoWe/commit/f73c9718c7846db76ed24f4ba1f4e9b26c990185))
* **resolvers:** preserve ws:// and shock:// URIs in File/Directory locations ([7922a57](https://github.com/wilke/GoWe/commit/7922a57e843afefc0c1b78d6b8633a178aa8d412))
* **resolvers:** preserve ws:// and shock:// URIs in File/Directory locations ([e79a5b7](https://github.com/wilke/GoWe/commit/e79a5b76bb1ff822e50dadf8f4a5180a05961698)), closes [#117](https://github.com/wilke/GoWe/issues/117)
* scatter inputs, BV-BRC utility tools, workflow naming, UI colors ([baa9903](https://github.com/wilke/GoWe/commit/baa99035f20e1eac0eb8344a4dbc6f033feeee8d))
* **scheduler:** embed user token on BV-BRC tasks regardless of staging mode ([f6deb8b](https://github.com/wilke/GoWe/commit/f6deb8be4d64920455b07a7a69a3ab091ce5336a))
* send stored auth token with all CLI requests ([c0db1eb](https://github.com/wilke/GoWe/commit/c0db1eb3d10d4f7937cfedc9c51aca8a3c7f1ff2))
* show static duration for completed tasks instead of live clock ([31d3b13](https://github.com/wilke/GoWe/commit/31d3b13bd3bed82a3d1dea819ad7686ee5637df7))
* skip embedding user token in tasks when server-side staging is active ([80806ae](https://github.com/wilke/GoWe/commit/80806ae9029338412dde869bd695cfb1c44b7d13))
* strip credentials from task API responses ([61182b9](https://github.com/wilke/GoWe/commit/61182b9fd756e5f323dfa4c485a407bd500b1a99))
* treat batch step-instance failure as hard error, clamp task count ([de7526b](https://github.com/wilke/GoWe/commit/de7526b381e4092e66e5af342ef90cdcb94dedf8))
* use workflow names as graph IDs in gowe:// reference resolution ([00db86a](https://github.com/wilke/GoWe/commit/00db86aaecaebfd18449267c3b8e31649ea012e9))
* **validator:** accept valid CWL ${...} JavaScript expression bodies ([da0113a](https://github.com/wilke/GoWe/commit/da0113a67a46cd084e766ac4c47dec4d70275ec6))
* **validator:** accept valid CWL ${...} JavaScript expression bodies ([d74fd62](https://github.com/wilke/GoWe/commit/d74fd62079cae4ed356d4fefb08a69a950770654)), closes [#120](https://github.com/wilke/GoWe/issues/120)
* **worker:** reconcile orphaned tasks and cancel running tasks via heartbeat ([20b6951](https://github.com/wilke/GoWe/commit/20b69514f41a2ffacb7794ba704e2e759d48fd3b))
* **worker:** reconcile orphaned tasks and cancel running tasks via heartbeat ([1888a39](https://github.com/wilke/GoWe/commit/1888a39088bdde99d3d76090c6e9295000d15ce7))


### Security

* encrypt provider tokens at rest (AES-256-GCM) ([#138](https://github.com/wilke/GoWe/issues/138)) ([c7c4723](https://github.com/wilke/GoWe/commit/c7c4723756187420c89549a067e20b74a9151621))
