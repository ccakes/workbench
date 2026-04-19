package runner

import (
	"syscall"

	ps "github.com/mitchellh/go-ps"
)

// descendants returns all descendant PIDs of root (not including root itself).
// It snapshots the full process table and walks the tree from root via PPIDs.
// Returns nil if the process table cannot be read.
func descendants(root int) []int {
	procs, err := ps.Processes()
	if err != nil {
		return nil
	}

	// Build ppid → []pid map
	children := map[int][]int{}
	for _, p := range procs {
		children[p.PPid()] = append(children[p.PPid()], p.Pid())
	}

	// BFS from root
	var result []int
	queue := children[root]
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		result = append(result, pid)
		queue = append(queue, children[pid]...)
	}
	return result
}

// signalAll sends sig to each pid individually, ignoring errors
// (process may already be dead or inaccessible).
func signalAll(pids []int, sig syscall.Signal) {
	for _, pid := range pids {
		_ = syscall.Kill(pid, sig)
	}
}

// anyAlive returns true if any of the given PIDs are still alive.
func anyAlive(pids []int) bool {
	for _, pid := range pids {
		// Signal 0 tests whether the process exists without sending a signal.
		if syscall.Kill(pid, 0) == nil {
			return true
		}
	}
	return false
}
