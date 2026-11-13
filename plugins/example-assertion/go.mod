module github.com/pgodlews/verity-api/plugins/example-assertion

go 1.26.0

toolchain go1.26.11

require github.com/pgodlews/verity-api v0.0.0

// Plugins build against the local core during development. For a released
// plugin, drop this replace and require a tagged version instead.
replace github.com/pgodlews/verity-api => ../..
