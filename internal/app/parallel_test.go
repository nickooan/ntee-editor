package app

import (
	"sync/atomic"
	"testing"
)

func TestForEachParallelVisitsEveryIndexOnce(t *testing.T) {
	for _, count := range []int{0, 1, 7, 1000} {
		visits := make([]atomic.Int32, count)
		forEachParallel(count, func(index int) { visits[index].Add(1) })
		for index := range visits {
			if got := visits[index].Load(); got != 1 {
				t.Fatalf("count %d: index %d visited %d times", count, index, got)
			}
		}
	}
}
