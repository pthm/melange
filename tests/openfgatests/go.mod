module github.com/pthm/melange/tests/openfgatests

go 1.25.3

require (
	github.com/openfga/api/proto v0.0.0-20251105142303-feed3db3d69d
	github.com/openfga/language/pkg/go v0.2.0-beta.2
	github.com/openfga/openfga v1.8.2
	github.com/pthm/melange v0.0.0
	github.com/pthm/melange/tooling v0.0.0
)

// Use local modules during development
replace github.com/pthm/melange => ../../

replace github.com/pthm/melange/tooling => ../../tooling
