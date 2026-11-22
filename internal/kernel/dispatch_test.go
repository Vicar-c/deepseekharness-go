package kernel_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
)

type dispatchKey string

// recordingHook returns a Hook that appends label to order and returns the
// supplied value and error.
func recordingHook(order *[]string, label string, value any, err error) kernel.Hook {
	return func(context.Context, any) (any, error) {
		*order = append(*order, label)
		return value, err
	}
}

// recordingLayer returns a WaterfallHandler that appends label to order and
// either calls next or vetoes the rest of the chain.
func recordingLayer(order *[]string, label string, callNext bool, value any, err error) kernel.WaterfallHandler {
	return func(ctx context.Context, payload any, next func(context.Context) (any, error)) (any, error) {
		*order = append(*order, label)
		if !callNext {
			return value, err
		}
		result, nextErr := next(ctx)
		*order = append(*order, label+":after")
		if nextErr != nil {
			return nil, nextErr
		}
		return result, err
	}
}

func TestSerialRunsHooksInRegistrationOrder(t *testing.T) {
	events := kernel.NewEvents()
	ctx := context.WithValue(context.Background(), dispatchKey("request"), "abc")

	var order []string
	var gotPayload any
	var gotContext any
	events.OnHook("demo", func(hookCtx context.Context, payload any) (any, error) {
		order = append(order, "first")
		gotPayload = payload
		gotContext = hookCtx.Value(dispatchKey("request"))
		return nil, nil
	})
	events.OnHook("demo", recordingHook(&order, "second", nil, nil))
	events.OnHook("demo", recordingHook(&order, "third", nil, nil))

	value, err := events.Serial(ctx, "demo", "the-payload")
	if err != nil {
		t.Fatalf("Serial: %v", err)
	}
	if value != nil {
		t.Fatalf("Serial value = %v, want nil", value)
	}
	if gotPayload != "the-payload" {
		t.Fatalf("payload = %v, want the-payload", gotPayload)
	}
	if gotContext != "abc" {
		t.Fatalf("context value = %v, want abc", gotContext)
	}
	if want := []string{"first", "second", "third"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("hook order = %v, want %v", order, want)
	}
}

func TestSerialBailValues(t *testing.T) {
	var typedNil *int
	tests := []struct {
		name     string
		value    any
		wantBail bool
	}{
		{name: "nil continues", value: nil},
		{name: "false continues", value: false},
		{name: "true bails", value: true, wantBail: true},
		{name: "string bails", value: "stop", wantBail: true},
		{name: "zero bails", value: 0, wantBail: true},
		{name: "empty string bails", value: "", wantBail: true},
		{name: "typed nil pointer bails", value: typedNil, wantBail: true},
		{name: "struct bails", value: struct{ N int }{N: 1}, wantBail: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := kernel.NewEvents()
			var order []string
			events.OnHook("demo", recordingHook(&order, "producer", test.value, nil))
			events.OnHook("demo", recordingHook(&order, "after", nil, nil))

			value, err := events.Serial(context.Background(), "demo", nil)
			if err != nil {
				t.Fatalf("Serial: %v", err)
			}
			if test.wantBail {
				if want := []string{"producer"}; !reflect.DeepEqual(order, want) {
					t.Fatalf("hook order = %v, want %v", order, want)
				}
				if !reflect.DeepEqual(value, test.value) {
					t.Fatalf("Serial value = %#v, want %#v", value, test.value)
				}
				return
			}
			if want := []string{"producer", "after"}; !reflect.DeepEqual(order, want) {
				t.Fatalf("hook order = %v, want %v", order, want)
			}
			if value != nil {
				t.Fatalf("Serial value = %v, want nil", value)
			}
		})
	}
}

func TestSerialStopsAtFirstError(t *testing.T) {
	events := kernel.NewEvents()
	firstErr := errors.New("first failure")
	laterErr := errors.New("must not run")

	var order []string
	events.OnHook("demo", recordingHook(&order, "first", nil, nil))
	events.OnHook("demo", recordingHook(&order, "second", "ignored-value", firstErr))
	events.OnHook("demo", recordingHook(&order, "third", nil, laterErr))

	value, err := events.Serial(context.Background(), "demo", nil)
	if !errors.Is(err, firstErr) {
		t.Fatalf("Serial error = %v, want %v", err, firstErr)
	}
	if errors.Is(err, laterErr) {
		t.Fatalf("Serial ran hooks after the first error: %v", err)
	}
	if value != nil {
		t.Fatalf("Serial value = %v, want nil on error", value)
	}
	if want := []string{"first", "second"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("hook order = %v, want %v", order, want)
	}
}

func TestSerialRecoversHookPanics(t *testing.T) {
	tests := []struct {
		name       string
		panicValue any
		wantError  string
	}{
		{name: "string panic", panicValue: "boom", wantError: "kernel: hook panicked: boom"},
		{name: "error panic", panicValue: errors.New("bad state"), wantError: "kernel: hook panicked: bad state"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := kernel.NewEvents()
			events.OnHook("demo", func(context.Context, any) (any, error) {
				panic(test.panicValue)
			})
			var laterCalls int
			events.OnHook("demo", func(context.Context, any) (any, error) {
				laterCalls++
				return nil, nil
			})

			value, err := events.Serial(context.Background(), "demo", nil)
			if err == nil {
				t.Fatal("Serial returned no error for a panicking hook")
			}
			if err.Error() != test.wantError {
				t.Fatalf("Serial error = %q, want %q", err, test.wantError)
			}
			if value != nil {
				t.Fatalf("Serial value = %v, want nil", value)
			}
			if laterCalls != 0 {
				t.Fatalf("later hook ran %d times after a panic", laterCalls)
			}
		})
	}
}

func TestSerialWithoutHooks(t *testing.T) {
	events := kernel.NewEvents()
	value, err := events.Serial(context.Background(), "missing", nil)
	if err != nil {
		t.Fatalf("Serial: %v", err)
	}
	if value != nil {
		t.Fatalf("Serial value = %v, want nil", value)
	}
}

func TestBailIsSerialAlias(t *testing.T) {
	events := kernel.NewEvents()
	var calls int
	events.OnHook("demo", func(context.Context, any) (any, error) {
		calls++
		return "bailed", nil
	})
	events.OnHook("demo", func(context.Context, any) (any, error) {
		calls++
		return "later", nil
	})

	value, err := events.Bail(context.Background(), "demo", nil)
	if err != nil {
		t.Fatalf("Bail: %v", err)
	}
	if value != "bailed" {
		t.Fatalf("Bail value = %v, want bailed", value)
	}
	if calls != 1 {
		t.Fatalf("Bail called %d hooks, want 1", calls)
	}
}

func TestOnHookUnsubscribeIsIdempotent(t *testing.T) {
	events := kernel.NewEvents()
	var order []string
	unsubscribeFirst := events.OnHook("demo", recordingHook(&order, "first", nil, nil))
	events.OnHook("demo", recordingHook(&order, "second", nil, nil))

	unsubscribeFirst()
	unsubscribeFirst()
	unsubscribeFirst()

	if _, err := events.Serial(context.Background(), "demo", nil); err != nil {
		t.Fatalf("Serial: %v", err)
	}
	if want := []string{"second"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("hook order = %v, want %v", order, want)
	}

	unsubscribeNil := events.OnHook("demo", nil)
	unsubscribeNil()
	unsubscribeNil()
}

func TestHooksRespectClose(t *testing.T) {
	events := kernel.NewEvents()
	var calls int
	events.OnHook("demo", func(context.Context, any) (any, error) {
		calls++
		return "value", nil
	})
	events.Close()

	value, err := events.Serial(context.Background(), "demo", nil)
	if err != nil {
		t.Fatalf("Serial: %v", err)
	}
	if value != nil {
		t.Fatalf("Serial value = %v after Close, want nil", value)
	}
	if err := events.Parallel(context.Background(), "demo", nil); err != nil {
		t.Fatalf("Parallel: %v", err)
	}
	if calls != 0 {
		t.Fatalf("hooks ran %d times after Close", calls)
	}

	unsubscribe := events.OnHook("demo", func(context.Context, any) (any, error) {
		calls++
		return nil, nil
	})
	unsubscribe()
	unsubscribe()
	if _, err := events.Serial(context.Background(), "demo", nil); err != nil {
		t.Fatalf("Serial: %v", err)
	}
	if calls != 0 {
		t.Fatalf("hook registered after Close ran %d times", calls)
	}
}

func TestHooksAreIsolatedByName(t *testing.T) {
	events := kernel.NewEvents()
	var order []string
	events.OnHook("alpha", recordingHook(&order, "alpha", nil, nil))
	events.OnHook("beta", recordingHook(&order, "beta", nil, nil))

	if _, err := events.Serial(context.Background(), "alpha", nil); err != nil {
		t.Fatalf("Serial: %v", err)
	}
	if want := []string{"alpha"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("hook order = %v, want %v", order, want)
	}
}

func TestParallelRunsEveryHookAndJoinsErrors(t *testing.T) {
	events := kernel.NewEvents()
	firstErr := errors.New("first failure")
	secondErr := errors.New("second failure")
	var calls atomic.Int32
	events.OnHook("demo", func(context.Context, any) (any, error) {
		calls.Add(1)
		return "ignored", firstErr
	})
	events.OnHook("demo", func(context.Context, any) (any, error) {
		calls.Add(1)
		return nil, nil
	})
	events.OnHook("demo", func(context.Context, any) (any, error) {
		calls.Add(1)
		return false, secondErr
	})

	err := events.Parallel(context.Background(), "demo", nil)
	if !errors.Is(err, firstErr) {
		t.Fatalf("Parallel error = %v, want it to wrap %v", err, firstErr)
	}
	if !errors.Is(err, secondErr) {
		t.Fatalf("Parallel error = %v, want it to wrap %v", err, secondErr)
	}
	if calls.Load() != 3 {
		t.Fatalf("Parallel called %d hooks, want 3", calls.Load())
	}
	firstIndex := strings.Index(err.Error(), firstErr.Error())
	secondIndex := strings.Index(err.Error(), secondErr.Error())
	if firstIndex < 0 || secondIndex < 0 || firstIndex > secondIndex {
		t.Fatalf("joined errors are not in registration order: %q", err)
	}
}

func TestParallelReturnsNilWhenAllHooksSucceed(t *testing.T) {
	events := kernel.NewEvents()
	var calls atomic.Int32
	for i := 0; i < 4; i++ {
		events.OnHook("demo", func(context.Context, any) (any, error) {
			calls.Add(1)
			return nil, nil
		})
	}

	if err := events.Parallel(context.Background(), "demo", nil); err != nil {
		t.Fatalf("Parallel: %v", err)
	}
	if calls.Load() != 4 {
		t.Fatalf("Parallel called %d hooks, want 4", calls.Load())
	}
}

func TestParallelRecoversPanicsAndStillRunsEveryHook(t *testing.T) {
	events := kernel.NewEvents()
	otherErr := errors.New("other failure")
	var successes atomic.Int32
	events.OnHook("demo", func(context.Context, any) (any, error) {
		panic("boom")
	})
	events.OnHook("demo", func(context.Context, any) (any, error) {
		return nil, otherErr
	})
	events.OnHook("demo", func(context.Context, any) (any, error) {
		successes.Add(1)
		return nil, nil
	})

	err := events.Parallel(context.Background(), "demo", nil)
	if err == nil {
		t.Fatal("Parallel returned no error")
	}
	if !strings.Contains(err.Error(), "kernel: hook panicked: boom") {
		t.Fatalf("Parallel error = %q, want it to mention the panic", err)
	}
	if !errors.Is(err, otherErr) {
		t.Fatalf("Parallel error = %v, want it to wrap %v", err, otherErr)
	}
	if successes.Load() != 1 {
		t.Fatalf("successful hook ran %d times, want 1", successes.Load())
	}
}

func TestParallelRunsHooksConcurrently(t *testing.T) {
	events := kernel.NewEvents()
	const hookCount = 5

	started := make(chan struct{}, hookCount)
	release := make(chan struct{})
	var finished sync.WaitGroup
	finished.Add(hookCount)
	var calls atomic.Int32

	for i := 0; i < hookCount; i++ {
		events.OnHook("demo", func(context.Context, any) (any, error) {
			calls.Add(1)
			started <- struct{}{}
			<-release
			finished.Done()
			return nil, nil
		})
	}

	done := make(chan error, 1)
	go func() {
		done <- events.Parallel(context.Background(), "demo", nil)
	}()

	for i := 0; i < hookCount; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatalf("only %d of %d hooks started; Parallel did not overlap them", i, hookCount)
		}
	}
	close(release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Parallel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Parallel did not return after all hooks were released")
	}
	finished.Wait()
	if calls.Load() != hookCount {
		t.Fatalf("Parallel called %d hooks, want %d", calls.Load(), hookCount)
	}
}

func TestWaterfallRunsLayersOutermostFirst(t *testing.T) {
	events := kernel.NewEvents()
	ctx := context.WithValue(context.Background(), dispatchKey("request"), "abc")

	var order []string
	var payloads []any
	var contextValues []any
	for _, label := range []string{"outer", "middle", "inner"} {
		label := label
		events.OnWaterfall("demo", func(layerCtx context.Context, payload any, next func(context.Context) (any, error)) (any, error) {
			order = append(order, label+":before")
			payloads = append(payloads, payload)
			contextValues = append(contextValues, layerCtx.Value(dispatchKey("request")))
			result, err := next(layerCtx)
			order = append(order, label+":after")
			return result, err
		})
	}

	var terminalCalls int
	value, err := events.Waterfall(ctx, "demo", "the-payload", func(terminalCtx context.Context) (any, error) {
		terminalCalls++
		if got := terminalCtx.Value(dispatchKey("request")); got != "abc" {
			return nil, fmt.Errorf("terminal context value = %v, want abc", got)
		}
		order = append(order, "terminal")
		return "result", nil
	})
	if err != nil {
		t.Fatalf("Waterfall: %v", err)
	}
	if value != "result" {
		t.Fatalf("Waterfall value = %v, want result", value)
	}
	if terminalCalls != 1 {
		t.Fatalf("terminal ran %d times, want 1", terminalCalls)
	}
	wantOrder := []string{
		"outer:before", "middle:before", "inner:before",
		"terminal",
		"inner:after", "middle:after", "outer:after",
	}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("waterfall order = %v, want %v", order, wantOrder)
	}
	wantPayloads := []any{"the-payload", "the-payload", "the-payload"}
	if !reflect.DeepEqual(payloads, wantPayloads) {
		t.Fatalf("layer payloads = %v, want %v", payloads, wantPayloads)
	}
	wantContextValues := []any{"abc", "abc", "abc"}
	if !reflect.DeepEqual(contextValues, wantContextValues) {
		t.Fatalf("layer context values = %v, want %v", contextValues, wantContextValues)
	}
}

func TestWaterfallLayerCanReplaceContext(t *testing.T) {
	events := kernel.NewEvents()
	ctx := context.Background()
	events.OnWaterfall("demo", func(layerCtx context.Context, payload any, next func(context.Context) (any, error)) (any, error) {
		return next(context.WithValue(layerCtx, dispatchKey("layer"), "replaced"))
	})

	value, err := events.Waterfall(ctx, "demo", nil, func(terminalCtx context.Context) (any, error) {
		if got := terminalCtx.Value(dispatchKey("layer")); got != "replaced" {
			return nil, fmt.Errorf("terminal context value = %v, want replaced", got)
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("Waterfall: %v", err)
	}
	if value != "ok" {
		t.Fatalf("Waterfall value = %v, want ok", value)
	}
	if got := ctx.Value(dispatchKey("layer")); got != nil {
		t.Fatalf("original context was mutated: %v", got)
	}
}

func TestWaterfallVetoSkipsRemainingLayersAndTerminal(t *testing.T) {
	events := kernel.NewEvents()
	var order []string
	events.OnWaterfall("demo", recordingLayer(&order, "outer", true, nil, nil))
	events.OnWaterfall("demo", recordingLayer(&order, "veto", false, "vetoed", nil))
	events.OnWaterfall("demo", recordingLayer(&order, "inner", true, nil, nil))

	var terminalCalls int
	value, err := events.Waterfall(context.Background(), "demo", nil, func(context.Context) (any, error) {
		terminalCalls++
		order = append(order, "terminal")
		return "terminal", nil
	})
	if err != nil {
		t.Fatalf("Waterfall: %v", err)
	}
	if value != "vetoed" {
		t.Fatalf("Waterfall value = %v, want vetoed", value)
	}
	if terminalCalls != 0 {
		t.Fatalf("terminal ran %d times after veto, want 0", terminalCalls)
	}
	wantOrder := []string{"outer", "veto", "outer:after"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("waterfall order = %v, want %v", order, wantOrder)
	}
}

func TestWaterfallWithoutLayersRunsTerminal(t *testing.T) {
	events := kernel.NewEvents()
	var gotPayload any
	value, err := events.Waterfall(context.Background(), "demo", nil, func(context.Context) (any, error) {
		gotPayload = "terminal ran"
		return 42, nil
	})
	if err != nil {
		t.Fatalf("Waterfall: %v", err)
	}
	if value != 42 {
		t.Fatalf("Waterfall value = %v, want 42", value)
	}
	if gotPayload != "terminal ran" {
		t.Fatalf("terminal did not run: %v", gotPayload)
	}
}

func TestWaterfallPassesNilPayload(t *testing.T) {
	events := kernel.NewEvents()
	var layerSawNil, innerSawNil bool
	events.OnWaterfall("demo", func(ctx context.Context, payload any, next func(context.Context) (any, error)) (any, error) {
		layerSawNil = payload == nil
		return next(ctx)
	})
	events.OnWaterfall("demo", func(ctx context.Context, payload any, next func(context.Context) (any, error)) (any, error) {
		innerSawNil = payload == nil
		return next(ctx)
	})

	value, err := events.Waterfall(context.Background(), "demo", nil, func(context.Context) (any, error) {
		return "done", nil
	})
	if err != nil {
		t.Fatalf("Waterfall: %v", err)
	}
	if value != "done" {
		t.Fatalf("Waterfall value = %v, want done", value)
	}
	if !layerSawNil || !innerSawNil {
		t.Fatalf("layers saw nil payload: outer=%v inner=%v", layerSawNil, innerSawNil)
	}
}

func TestWaterfallNextMayBeCalledOnlyOnce(t *testing.T) {
	events := kernel.NewEvents()
	var terminalCalls int
	events.OnWaterfall("demo", func(ctx context.Context, payload any, next func(context.Context) (any, error)) (any, error) {
		first, err := next(ctx)
		if err != nil {
			return nil, err
		}
		if first != "terminal-value" {
			return nil, fmt.Errorf("first next = %v, want terminal-value", first)
		}
		second, secondErr := next(ctx)
		if second != nil {
			return nil, fmt.Errorf("second next value = %v, want nil", second)
		}
		return "layer-value", secondErr
	})

	_, err := events.Waterfall(context.Background(), "demo", nil, func(context.Context) (any, error) {
		terminalCalls++
		return "terminal-value", nil
	})
	if err == nil {
		t.Fatal("Waterfall returned no error for a reused next")
	}
	if err.Error() != "kernel: next called more than once" {
		t.Fatalf("Waterfall error = %q, want the reuse error", err)
	}
	if terminalCalls != 1 {
		t.Fatalf("terminal ran %d times, want 1", terminalCalls)
	}
}

func TestWaterfallNextReuseAfterDownstreamError(t *testing.T) {
	events := kernel.NewEvents()
	terminalErr := errors.New("terminal failure")
	var terminalCalls int
	events.OnWaterfall("demo", func(ctx context.Context, payload any, next func(context.Context) (any, error)) (any, error) {
		_, firstErr := next(ctx)
		if !errors.Is(firstErr, terminalErr) {
			return nil, firstErr
		}
		_, secondErr := next(ctx)
		return nil, secondErr
	})

	_, err := events.Waterfall(context.Background(), "demo", nil, func(context.Context) (any, error) {
		terminalCalls++
		return nil, terminalErr
	})
	if err == nil {
		t.Fatal("Waterfall returned no error")
	}
	if err.Error() != "kernel: next called more than once" {
		t.Fatalf("Waterfall error = %q, want the reuse error", err)
	}
	if terminalCalls != 1 {
		t.Fatalf("terminal ran %d times after a failed next, want 1", terminalCalls)
	}
}

func TestWaterfallRecoversLayerPanic(t *testing.T) {
	events := kernel.NewEvents()
	var belowCalls, terminalCalls int
	events.OnWaterfall("demo", func(ctx context.Context, payload any, next func(context.Context) (any, error)) (any, error) {
		return next(ctx)
	})
	events.OnWaterfall("demo", func(context.Context, any, func(context.Context) (any, error)) (any, error) {
		panic("boom")
	})
	events.OnWaterfall("demo", func(ctx context.Context, payload any, next func(context.Context) (any, error)) (any, error) {
		belowCalls++
		return next(ctx)
	})

	_, err := events.Waterfall(context.Background(), "demo", nil, func(context.Context) (any, error) {
		terminalCalls++
		return "terminal", nil
	})
	if err == nil {
		t.Fatal("Waterfall returned no error for a panicking layer")
	}
	if err.Error() != "kernel: waterfall layer panicked: boom" {
		t.Fatalf("Waterfall error = %q, want the layer panic error", err)
	}
	if belowCalls != 0 {
		t.Fatalf("layers below the panic ran %d times, want 0", belowCalls)
	}
	if terminalCalls != 0 {
		t.Fatalf("terminal ran %d times after a panic, want 0", terminalCalls)
	}
}

func TestWaterfallRecoversTerminalPanic(t *testing.T) {
	events := kernel.NewEvents()
	events.OnWaterfall("demo", func(ctx context.Context, payload any, next func(context.Context) (any, error)) (any, error) {
		return next(ctx)
	})

	_, err := events.Waterfall(context.Background(), "demo", nil, func(context.Context) (any, error) {
		panic("terminal boom")
	})
	if err == nil {
		t.Fatal("Waterfall returned no error for a panicking terminal")
	}
	if err.Error() != "kernel: waterfall terminal panicked: terminal boom" {
		t.Fatalf("Waterfall error = %q, want the terminal panic error", err)
	}
}

func TestWaterfallRejectsNilTerminalBeforeRunningLayers(t *testing.T) {
	events := kernel.NewEvents()
	var layerCalls int
	events.OnWaterfall("demo", func(ctx context.Context, payload any, next func(context.Context) (any, error)) (any, error) {
		layerCalls++
		return next(ctx)
	})

	value, err := events.Waterfall(context.Background(), "demo", nil, nil)
	if err == nil {
		t.Fatal("Waterfall returned no error for a nil terminal")
	}
	if err.Error() != "kernel: waterfall terminal is nil" {
		t.Fatalf("Waterfall error = %q, want the nil terminal error", err)
	}
	if value != nil {
		t.Fatalf("Waterfall value = %v, want nil", value)
	}
	if layerCalls != 0 {
		t.Fatalf("layer ran %d times before the nil terminal check, want 0", layerCalls)
	}
}

func TestOnWaterfallUnsubscribeIsIdempotent(t *testing.T) {
	events := kernel.NewEvents()
	var order []string
	unsubscribeFirst := events.OnWaterfall("demo", func(ctx context.Context, payload any, next func(context.Context) (any, error)) (any, error) {
		order = append(order, "removed")
		return next(ctx)
	})
	events.OnWaterfall("demo", recordingLayer(&order, "kept", true, nil, nil))

	unsubscribeFirst()
	unsubscribeFirst()
	unsubscribeFirst()

	value, err := events.Waterfall(context.Background(), "demo", nil, func(context.Context) (any, error) {
		order = append(order, "terminal")
		return "result", nil
	})
	if err != nil {
		t.Fatalf("Waterfall: %v", err)
	}
	if value != "result" {
		t.Fatalf("Waterfall value = %v, want result", value)
	}
	if want := []string{"kept", "terminal", "kept:after"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("waterfall order = %v, want %v", order, want)
	}

	unsubscribeNil := events.OnWaterfall("demo", nil)
	unsubscribeNil()
	unsubscribeNil()
}

func TestWaterfallRespectsClose(t *testing.T) {
	events := kernel.NewEvents()
	var layerCalls, terminalCalls int
	events.OnWaterfall("demo", func(ctx context.Context, payload any, next func(context.Context) (any, error)) (any, error) {
		layerCalls++
		return next(ctx)
	})
	events.Close()

	value, err := events.Waterfall(context.Background(), "demo", "payload", func(context.Context) (any, error) {
		terminalCalls++
		return "terminal", nil
	})
	if err != nil {
		t.Fatalf("Waterfall: %v", err)
	}
	if value != "terminal" {
		t.Fatalf("Waterfall value = %v, want terminal", value)
	}
	if layerCalls != 0 {
		t.Fatalf("layer ran %d times after Close, want 0", layerCalls)
	}
	if terminalCalls != 1 {
		t.Fatalf("terminal ran %d times, want 1", terminalCalls)
	}

	unsubscribe := events.OnWaterfall("demo", func(ctx context.Context, payload any, next func(context.Context) (any, error)) (any, error) {
		layerCalls++
		return next(ctx)
	})
	unsubscribe()
	unsubscribe()
	if _, err := events.Waterfall(context.Background(), "demo", nil, func(context.Context) (any, error) {
		terminalCalls++
		return "terminal", nil
	}); err != nil {
		t.Fatalf("Waterfall: %v", err)
	}
	if layerCalls != 0 {
		t.Fatalf("layer registered after Close ran %d times, want 0", layerCalls)
	}
	if terminalCalls != 2 {
		t.Fatalf("terminal ran %d times, want 2", terminalCalls)
	}
}

func TestDispatchModesDoNotSeePlainHandlers(t *testing.T) {
	events := kernel.NewEvents()
	var emitted int
	events.On("demo", func(kernel.Event) {
		emitted++
	})

	if _, err := events.Serial(context.Background(), "demo", nil); err != nil {
		t.Fatalf("Serial: %v", err)
	}
	if err := events.Parallel(context.Background(), "demo", nil); err != nil {
		t.Fatalf("Parallel: %v", err)
	}
	if _, err := events.Waterfall(context.Background(), "demo", nil, func(context.Context) (any, error) {
		return "terminal", nil
	}); err != nil {
		t.Fatalf("Waterfall: %v", err)
	}
	if emitted != 0 {
		t.Fatalf("plain handlers ran %d times during dispatch modes, want 0", emitted)
	}
}
