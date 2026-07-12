# Changelog

## [0.8.5](https://github.com/pthm/melange/compare/v0.8.4...v0.8.5) (2026-07-12)


### Bug Fixes

* **migrator:** guard version-column widen so dependent views don't break ([8d3cec2](https://github.com/pthm/melange/commit/8d3cec26aa446950c41d76900abfeab0cbff9437))
* **sqlgen:** list userset query subjects through complex-closure arm ([f6ccd36](https://github.com/pthm/melange/commit/f6ccd36a4e22e3d97a2907fd3d34255aad058389))
* **sqlgen:** make self-referential recursive TTU lists complete and sound ([#12](https://github.com/pthm/melange/issues/12)) ([bdd6c00](https://github.com/pthm/melange/commit/bdd6c006a153d061c87de19d4b5cb78fc7aa355a))
* **sqlgen:** qualify ambiguous subject_id in list_subjects wildcard+exclusion path ([43113c3](https://github.com/pthm/melange/commit/43113c39acda2bead80a2103b04998cc42f7edff))
* **sqlgen:** route self-referential recursive TTU to recursive list strategy ([c901401](https://github.com/pthm/melange/commit/c901401cc39869091ac3dcafa1300640216adcac))
* **sqlgen:** surface TTU-reachable wildcards in list_subjects ([1cb4001](https://github.com/pthm/melange/commit/1cb4001a155b6a7ec45991ad7505be61192d8d9f))
* widen migration version columns to TEXT for go-install pseudo-versions ([280ffe3](https://github.com/pthm/melange/commit/280ffe3e6a6410a6459c270672f6909a48c5cea0))


### Performance

* nested IF-chain dispatcher for schema-size-independent routing ([8b68aad](https://github.com/pthm/melange/commit/8b68aadbbdca801fc0a3546cbe40c6fcc0ba2a2e))
* schema-independent dispatch + set-oriented list composition ([#67](https://github.com/pthm/melange/issues/67)) ([25e13ca](https://github.com/pthm/melange/commit/25e13cad917c123d50c34aa17b6d626d69221f25))
* **sqlgen:** compose closure-source verification on subject-first TTU ([ff76c89](https://github.com/pthm/melange/commit/ff76c891c6a70760b74b5c3de3a4bfaeedd0c67d))
* **sqlgen:** compose complex-closure list_objects blocks with list functions ([f0fe5b9](https://github.com/pthm/melange/commit/f0fe5b91ec3735dbc3860b65883bc64694cce1dc))
* **sqlgen:** compose complex-closure list_subjects blocks with list functions ([b982f9a](https://github.com/pthm/melange/commit/b982f9a9b25094113a93b34900e7ebc156130c2b))
* **sqlgen:** compose complex-userset list_subjects block with list_subjects ([a6d14c4](https://github.com/pthm/melange/commit/a6d14c4c5b18a4f225966c4901f3b3d9c5d1bcfc))
* **sqlgen:** compose composed-strategy recursive TTU list_objects block ([e127d03](https://github.com/pthm/melange/commit/e127d03e8c91519dc9fd17815194cfe805dc8dab))
* **sqlgen:** compose list_objects arms with parent list functions when acyclic ([35e2111](https://github.com/pthm/melange/commit/35e2111a11771b29fb0112fc57be50ba7f79eccd))
* **sqlgen:** compose list_objects intersection group parts ([8f4796b](https://github.com/pthm/melange/commit/8f4796babbd7c1b11c7e6ab989455b6074bcae2b))
* **sqlgen:** drop search_path from dispatchers/wrappers and call internal directly in list_subjects ([5764322](https://github.com/pthm/melange/commit/5764322f1fe14b9099b66a42833c213b606e153f))
* **sqlgen:** filter inline closure/userset VALUES per list function ([5460360](https://github.com/pthm/melange/commit/546036000cc57b4095c2199266ba4647f172bf6e))
* **sqlgen:** filter inline VALUES per check function to referenced types ([9cecad2](https://github.com/pthm/melange/commit/9cecad2d8503e0e9df2224e56db8e92a7af6aa1f))
* **sqlgen:** fold userset self-check to compile-time IN-list ([25ecfab](https://github.com/pthm/melange/commit/25ecfabc21594ea693c0d4e10829b496387e16f7))
* **sqlgen:** guard userset-composition list_objects check to userset subjects ([ec8ed75](https://github.com/pthm/melange/commit/ec8ed750b317695c6604786fc2ec33c25489a00e))
* **sqlgen:** set-oriented anti-join for complex exclusions in list_objects ([f93ba37](https://github.com/pthm/melange/commit/f93ba37b5c09944b6b619cb04c949ce147a3474f))
* **sqlgen:** subject-first composition for cross-type TTU in list_subjects ([2882def](https://github.com/pthm/melange/commit/2882def29c326162fbbaa8270b1e5d65f5357a50))

## [0.8.4](https://github.com/pthm/melange/compare/v0.8.3...v0.8.4) (2026-07-01)


### Features

* add Explain and Expand APIs (OpenFGA UsersetTree parity) ([a2adff6](https://github.com/pthm/melange/commit/a2adff6e670fff81f30344762b5d00d5337367c7))
* add trace foundations for explain and expand apis ([d76f360](https://github.com/pthm/melange/commit/d76f360f5938c0d574c785f8ea1dbf3a8ee60690))
* **cache:** add ExplainCache / ExpandCache interfaces + wire into Checker ([be41a0e](https://github.com/pthm/melange/commit/be41a0e450d41717c3cad281ef6f74f5ddb62af7))
* **cli:** map explain/expand output to OpenFGA palette + header chip ([9258edb](https://github.com/pthm/melange/commit/9258edb860d39c69a10fa318f24de52636c186ac))
* **doctor:** add Expand fan-out advisory (Stage 3) ([5df3f5c](https://github.com/pthm/melange/commit/5df3f5c5c122a1a4c8373497fe479d541ee8d257))
* **expand:** add intersection support to the Expand renderer (slice 2.2c) ([1c0e46e](https://github.com/pthm/melange/commit/1c0e46e3c69e2626d86ddecdcbc715bfa4faf82a))
* **expand:** add p_max_leaf cap + users_truncated extension flag (slice 2.4) ([e284681](https://github.com/pthm/melange/commit/e284681ec96201bcc8120fb669a0c871bde395fc))
* **expand:** add TTU + simple exclusion to the Expand renderer (slice 2.2a/b) ([976d525](https://github.com/pthm/melange/commit/976d5256ec51e708f55b6ead17e92034095c797b))
* **expand:** inline wildcards + userset references in Leaf.Users (slice 2.3) ([6fc109b](https://github.com/pthm/melange/commit/6fc109b930398e6ee6ba77b19b5770325811118e))
* **expand:** ship Stage 2 slice 2.1 Expand MVP with OpenFGA UsersetTree parity ([f40d397](https://github.com/pthm/melange/commit/f40d3974373a966f44ebe81ca65b6619d60795fd))
* **expand:** support multi/TTU/intersection exclusion via nested Difference (slice 2.7) ([be42619](https://github.com/pthm/melange/commit/be42619eb1fc124ee9f1da533aead6e072a1c347))
* **expand:** TypeScript client + ExpandRecursive walker + OpenFGA listObjectsAssertions parity sweep (slice 2.5) ([aece636](https://github.com/pthm/melange/commit/aece6363206451852a6de4beee043991d48b14c7))
* **explain,expand:** drop HasComplexUsersetPatterns gate (slices 1.8 + 2.6) + sentinel report tooling ([b0ab602](https://github.com/pthm/melange/commit/b0ab60274da0b89a08dd5a19d76ea73dbcb45057))
* **explain,expand:** support IsThis / TTU / per-part-exclusion intersection parts (slice 1.9) ([6a553e6](https://github.com/pthm/melange/commit/6a553e60dc8d20169d2c9cbbb80b0289aac01e07))
* **explain:** drop conservative cross-type TTU eligibility (slice 1.10) ([d0e621f](https://github.com/pthm/melange/commit/d0e621f163d1f4e1dea340b94e15953ea19f9496))
* ship Explain MVP for direct-grant relations ([75c6132](https://github.com/pthm/melange/commit/75c6132a6036f86da04cbd4ce0b5d10b8facf84b))
* **sqlgen:** add intersection + exclusion support to Explain renderer ([2882c30](https://github.com/pthm/melange/commit/2882c30d0792b727c91ea4b3980005af3b659dbb))
* **sqlgen:** add transitive eligibility + implied recursion infrastructure ([037a461](https://github.com/pthm/melange/commit/037a4617aac23e33c5e1c62c7f5838c51f96ffa3))
* **sqlgen:** add TTU support to Explain renderer ([7f0f077](https://github.com/pthm/melange/commit/7f0f0772408473ab27dd73b7eeecd208d144d485))
* **sqlgen:** add userset reference support to Explain renderer ([5ec8b19](https://github.com/pthm/melange/commit/5ec8b1998965af198823f589c439ac5490e2d31c))
* **sqlgen:** add wildcard sentinels + p_max_nodes truncation to Explain ([c87dde1](https://github.com/pthm/melange/commit/c87dde1f8ca01e80994419d654b4a50d56fde97b))


### Bug Fixes

* **deps:** bump vite, esbuild, and openfga to patched versions ([f28ac45](https://github.com/pthm/melange/commit/f28ac45d2b66cc8680843e2163820df9d52c95c3))
* **deps:** bump vite, esbuild, and openfga to patched versions ([3d324c1](https://github.com/pthm/melange/commit/3d324c103c763ef560d261fdb6c137a6cc378313))


### Performance

* **sqlgen:** list cross-type TTU objects subject-first ([76212b0](https://github.com/pthm/melange/commit/76212b09b8e486b7aadecaca1356d26d860b6a76))
* **sqlgen:** list cross-type TTU objects subject-first ([6445bfc](https://github.com/pthm/melange/commit/6445bfc2573a4185553057bfa23465e92cf30a35))

## [0.8.3](https://github.com/pthm/melange/compare/v0.8.2...v0.8.3) (2026-06-16)


### Features

* **doctor:** surface schema-derived index recommendations ([39bdf19](https://github.com/pthm/melange/commit/39bdf198c5bc6847c239b7a252304f102612d351))
* **sqlgen:** emit recommended composite indexes as GeneratedSQL output ([3650a81](https://github.com/pthm/melange/commit/3650a81f1c3148d95ad600b0ada97ba8bcf5cb4d))
* **sqlgen:** opt-in AS MATERIALIZED hints for multi-referenced CTEs ([93d02ac](https://github.com/pthm/melange/commit/93d02acd399846676b8b308b66778190d1b9232e))


### Performance

* **sqlgen:** cheap-first branch ordering + schema-derived index recommendations ([67cd3b1](https://github.com/pthm/melange/commit/67cd3b1093ff22be49f6b4e521ef5cafe8c68972))
* **sqlgen:** order check-function branches cheap-first and hint recursive functions as expensive ([1c945e2](https://github.com/pthm/melange/commit/1c945e290dd6fb384e444975866f04b97454c3d7))

## [0.8.2](https://github.com/pthm/melange/compare/v0.8.1...v0.8.2) (2026-04-30)


### Features

* **explaintest:** walk all stages instead of just the first ([fb96895](https://github.com/pthm/melange/commit/fb96895e3c084341ec214eb9a57c255e4ffbeee3))


### Performance

* contextual-tuples plumbing and list_subjects fast path ([416a9fa](https://github.com/pthm/melange/commit/416a9fa891f4d4d74236005d81d2486e768fd08c))
* inline contextual tuples and cache base schema lookup ([4e5948f](https://github.com/pthm/melange/commit/4e5948f39329de40957ac69fc96b74d702cc2215))
* **sqlgen:** extend list_*_sub fast path to userset/wildcard parents ([9b42634](https://github.com/pthm/melange/commit/9b426340c8107dd418b20239f1da6f38d296a528))
