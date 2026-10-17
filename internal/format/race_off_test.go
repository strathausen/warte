//go:build !race

package format

import "time"

// perfBudget is the time limit of the complexity tests.
const perfBudget = 2 * time.Second
