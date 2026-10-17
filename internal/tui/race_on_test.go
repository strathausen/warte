//go:build race

package tui

// raceEnabled relaxes timing assertions under the race detector.
const raceEnabled = true
