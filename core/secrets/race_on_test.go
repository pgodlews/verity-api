//go:build race

package secrets

// raceEnabled is true under -race, whose instrumentation changes allocation
// counts.
const raceEnabled = true
