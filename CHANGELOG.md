# Changelog

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
