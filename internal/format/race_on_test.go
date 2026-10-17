//go:build race

package format

import "time"

// perfBudget is the time limit of the complexity tests: the race detector
// (and the rest of a parallel -race run) slows them down several times.
const perfBudget = 15 * time.Second
