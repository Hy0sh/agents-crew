// Package layout computes the pane splits needed to stack N worker panes
// next to a master pane, each split giving 1/k of the remaining space to
// the pane being split so the final panes end up roughly equal height.
package layout

// Split describes one herdr "pane split" call: which existing pane to
// split (identified by its index in the worker sequence, 0 for the master
// pane), in which direction, and what ratio to give the existing pane.
type Split struct {
	Direction string  // "right" or "down"
	Ratio     float64 // fraction of space kept by the pane being split
}

// WorkerSplits returns, for n workers, the ordered list of splits to
// perform starting from the master pane: split 1 carves the worker column
// out of the master pane (right, 60%); splits 2..n stack worker i+1 under
// worker i (down, 1/(remaining slots)).
func WorkerSplits(n int) []Split {
	if n <= 0 {
		return nil
	}
	splits := make([]Split, 0, n)
	splits = append(splits, Split{Direction: "right", Ratio: 0.6})
	for i := 2; i <= n; i++ {
		remaining := n - i + 2
		splits = append(splits, Split{Direction: "down", Ratio: 1.0 / float64(remaining)})
	}
	return splits
}
