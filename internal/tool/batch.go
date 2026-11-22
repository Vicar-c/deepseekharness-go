package tool

import (
	"context"
	"fmt"
	"sync"
)

// ConcurrencySafe is implemented by tools whose calls may overlap with other
// safe calls from the same model response. Tools that do not implement it (or
// report false) are exclusive: they run alone, with a barrier before and after,
// so file mutations and shell commands never interleave by accident.
type ConcurrencySafe interface {
	ConcurrencySafe() bool
}

// isSafe reports whether one call may join a parallel group. Unknown tools are
// treated as exclusive; they fail fast anyway and must not widen a barrier.
func (p *Pipeline) isSafe(name string) bool {
	registered, ok := p.registry.Get(name)
	if !ok {
		return false
	}
	safe, ok := registered.(ConcurrencySafe)
	return ok && safe.ConcurrencySafe()
}

// planBatch splits a call batch into execution groups: consecutive
// concurrency-safe calls share a group (bounded by limit) and every exclusive
// call becomes its own singleton group, which acts as a barrier.
func (p *Pipeline) planBatch(calls []Call, limit int) [][]int {
	if limit < 1 {
		limit = 1
	}
	var groups [][]int
	var current []int
	flush := func() {
		if len(current) > 0 {
			groups = append(groups, current)
			current = nil
		}
	}
	for index, call := range calls {
		if !p.isSafe(call.Name) {
			flush()
			groups = append(groups, []int{index})
			continue
		}
		current = append(current, index)
		if len(current) >= limit {
			flush()
		}
	}
	flush()
	return groups
}

// ExecuteBatch runs one model step's tool calls. Concurrency-safe calls overlap
// up to maxParallel, exclusive calls run alone, and the returned results are
// always in model order regardless of the order work finished in.
//
// A cancelled context stops new calls from starting; calls already running are
// awaited, and skipped calls still get an error result so the model sees a
// complete batch instead of a broken tool-call history.
func (p *Pipeline) ExecuteBatch(ctx context.Context, calls []Call, maxParallel int) []Result {
	results := make([]Result, len(calls))
	if len(calls) == 0 {
		return results
	}
	if maxParallel <= 0 {
		maxParallel = p.maxParallel
	}
	for _, group := range p.planBatch(calls, maxParallel) {
		if len(group) == 1 {
			index := group[0]
			if err := ctx.Err(); err != nil {
				results[index] = skippedResult(calls[index], err)
				continue
			}
			results[index] = p.Execute(ctx, calls[index])
			continue
		}
		var wait sync.WaitGroup
		for _, index := range group {
			if err := ctx.Err(); err != nil {
				results[index] = skippedResult(calls[index], err)
				continue
			}
			wait.Add(1)
			go func(index int) {
				defer wait.Done()
				results[index] = p.Execute(ctx, calls[index])
			}(index)
		}
		wait.Wait()
	}
	return results
}

func skippedResult(call Call, err error) Result {
	return Result{
		IsError: true,
		Code:    CodeAborted,
		Content: fmt.Sprintf("tool %q was not started: %v", call.Name, err),
	}
}
