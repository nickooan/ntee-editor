package app

import (
	"runtime"
	"sync"
	"sync/atomic"
)

// forEachParallel calls work once for every index in [0, count) on a worker
// pool of at most NumCPU goroutines, and returns when all calls are done.
// Workers take the next index as they free up, so a slow item doesn't hold
// back the rest; work writes into its own index slot to keep order.
func forEachParallel(count int, work func(index int)) {
	if count <= 0 {
		return
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(runtime.NumCPU(), count) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				index := int(next.Add(1)) - 1
				if index >= count {
					return
				}
				work(index)
			}
		}()
	}
	wg.Wait()
}
