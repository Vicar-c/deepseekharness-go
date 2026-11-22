package kernel

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Hook is one listener for a value-returning dispatch mode. It receives the
// event payload and reports a value, which Serial and Bail may treat as a bail
// value, or an error, which stops the dispatch.
type Hook func(ctx context.Context, payload any) (value any, err error)

// OnHook subscribes hook to one event name and returns an idempotent
// unsubscribe function. Serial, Bail and Parallel invoke hooks in registration
// order. A nil hook is ignored, and subscribing on a closed bus does nothing;
// both cases return a no-op unsubscribe.
func (e *Events) OnHook(name string, hook Hook) (unsubscribe func()) {
	if hook == nil {
		return func() {}
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return func() {}
	}
	e.nextID++
	id := e.nextID
	e.hooks[name] = append(e.hooks[name], hookSubscription{id: id, hook: hook})
	e.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() { e.removeHook(name, id) })
	}
}

// Serial runs the hooks registered for name one at a time in registration
// order. It returns the first bail value with a nil error; a bail value is any
// value that is neither nil nor the boolean false, so a typed nil pointer, zero
// and the empty string all count. A non-nil error stops the dispatch and is
// returned. When every hook runs without bailing, Serial returns (nil, nil). A
// panicking hook is recovered and returned as an error. Serial does not inspect
// ctx itself; it passes the context through to each hook unchanged.
func (e *Events) Serial(ctx context.Context, name string, payload any) (any, error) {
	for _, hook := range e.snapshotHooks(name) {
		value, err := callHook(ctx, hook, payload)
		if err != nil {
			return nil, err
		}
		if isBailValue(value) {
			return value, nil
		}
	}
	return nil, nil
}

// Bail is an alias of Serial. The JavaScript/Cordis vocabulary distinguishes
// bail (synchronous) from serial (awaits), a distinction Go does not have
// because listeners are plain function calls: both modes run the hooks one at a
// time in registration order, stop at the first non-nil error, and stop at the
// first bail value.
func (e *Events) Bail(ctx context.Context, name string, payload any) (any, error) {
	return e.Serial(ctx, name, payload)
}

// Parallel runs every hook registered for name concurrently and waits for all
// of them. It returns errors.Join of every hook error in registration order, or
// nil when all hooks succeed. Return values, including bail values, are
// discarded. A panicking hook is recovered and joins the returned error.
// Parallel does not inspect ctx itself; it passes the context through to every
// hook unchanged.
func (e *Events) Parallel(ctx context.Context, name string, payload any) error {
	hooks := e.snapshotHooks(name)
	errs := make([]error, len(hooks))
	var wg sync.WaitGroup
	wg.Add(len(hooks))
	for i, hook := range hooks {
		go func(index int, hook Hook) {
			defer wg.Done()
			if _, err := callHook(ctx, hook, payload); err != nil {
				errs[index] = err
			}
		}(i, hook)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// WaterfallHandler is one layer of a waterfall dispatch. It receives the
// original payload and next, a continuation that runs the rest of the chain.
// Returning without calling next vetoes the remaining layers and the terminal
// function, in which case the layer's return value becomes the waterfall
// result.
type WaterfallHandler func(ctx context.Context, payload any, next func(context.Context) (any, error)) (any, error)

// OnWaterfall registers one waterfall layer for name and returns an idempotent
// unsubscribe function. Layers run outermost-first in registration order: the
// first registered layer wraps every later layer and the terminal function. A
// nil handler is ignored, and registering on a closed bus does nothing; both
// cases return a no-op unsubscribe.
func (e *Events) OnWaterfall(name string, handler WaterfallHandler) (unsubscribe func()) {
	if handler == nil {
		return func() {}
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return func() {}
	}
	e.nextID++
	id := e.nextID
	e.waterfalls[name] = append(e.waterfalls[name], waterfallSubscription{id: id, handler: handler})
	e.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() { e.removeWaterfall(name, id) })
	}
}

// Waterfall composes every layer registered for name around terminal and
// invokes the resulting chain. Layers run outermost-first in registration
// order, and each layer receives the rest of the chain as next. A layer that
// does not call next vetoes the remaining layers and terminal, and its return
// value is the waterfall result. Calling next more than once makes the second
// and every later call return the error "kernel: next called more than once"
// without running the rest of the chain again. next receives a context, so a
// layer may replace or cancel the context seen by the layers below it.
//
// A nil terminal is an error, reported before any layer runs. A nil payload is
// passed to every layer unchanged; the terminal function only receives a
// context and may capture the payload from its closure. A panicking layer or
// terminal is recovered and returned as an error. On a closed bus no layers are
// delivered, so terminal runs directly, exactly as it would with no registered
// layers.
func (e *Events) Waterfall(ctx context.Context, name string, payload any, terminal func(context.Context) (any, error)) (any, error) {
	if terminal == nil {
		return nil, errWaterfallTerminalNil
	}

	next := func(ctx context.Context) (value any, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				value = nil
				err = &PanicError{Mode: "waterfall terminal", Value: recovered}
			}
		}()
		return terminal(ctx)
	}
	layers := e.snapshotWaterfalls(name)
	for i := len(layers) - 1; i >= 0; i-- {
		layer := layers[i]
		downstream := next
		next = func(ctx context.Context) (any, error) {
			return callWaterfallLayer(layer, ctx, payload, downstream)
		}
	}
	return next(ctx)
}

var (
	// errWaterfallTerminalNil is returned when Waterfall is called without a
	// terminal function.
	errWaterfallTerminalNil = errors.New("kernel: waterfall terminal is nil")
	// errWaterfallNextCalledTwice is returned by the second and every later
	// call of the next continuation handed to one waterfall layer.
	errWaterfallNextCalledTwice = errors.New("kernel: next called more than once")
)

// PanicError reports a listener that panicked. The dispatch modes recover the
// panic so one bad subscriber cannot bring down the harness, and this error is
// what the caller sees instead; callers that need to tell a panic apart from an
// ordinary listener failure match it with errors.As.
type PanicError struct {
	// Mode names the listener kind: "hook", "waterfall layer" or "waterfall
	// terminal".
	Mode string
	// Value is the recovered panic value.
	Value any
}

// Error renders the panic the way a listener failure is reported.
func (e *PanicError) Error() string {
	return fmt.Sprintf("kernel: %s panicked: %v", e.Mode, e.Value)
}

// hookSubscription is one registered Hook. The id makes unsubscribe precise
// even when the same function was registered more than once.
type hookSubscription struct {
	id   uint64
	hook Hook
}

// waterfallSubscription is one registered WaterfallHandler.
type waterfallSubscription struct {
	id      uint64
	handler WaterfallHandler
}

// dispatchState keeps the subscriptions of the value-returning dispatch modes.
// It is folded into Events itself: the bus owns one mutex and one id space for
// every dispatch style, so no subscriber outlives the bus that registered it.

// snapshotHooks returns the hooks currently registered for name, or nil when
// the bus is closed. The copy lets a dispatch keep running even if a hook
// unsubscribes itself or another hook meanwhile.
func (e *Events) snapshotHooks(name string) []Hook {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return nil
	}
	list := e.hooks[name]
	if len(list) == 0 {
		return nil
	}
	hooks := make([]Hook, len(list))
	for i := range list {
		hooks[i] = list[i].hook
	}
	return hooks
}

// snapshotWaterfalls returns the waterfall layers currently registered for
// name, or nil when the bus is closed.
func (e *Events) snapshotWaterfalls(name string) []WaterfallHandler {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return nil
	}
	list := e.waterfalls[name]
	if len(list) == 0 {
		return nil
	}
	layers := make([]WaterfallHandler, len(list))
	for i := range list {
		layers[i] = list[i].handler
	}
	return layers
}

// removeHook drops the hook with the given id from name's registration list.
func (e *Events) removeHook(name string, id uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	list := e.hooks[name]
	for i, sub := range list {
		if sub.id != id {
			continue
		}
		e.hooks[name] = append(append([]hookSubscription(nil), list[:i]...), list[i+1:]...)
		return
	}
}

// removeWaterfall drops the waterfall layer with the given id from name's
// registration list.
func (e *Events) removeWaterfall(name string, id uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	list := e.waterfalls[name]
	for i, sub := range list {
		if sub.id != id {
			continue
		}
		e.waterfalls[name] = append(append([]waterfallSubscription(nil), list[:i]...), list[i+1:]...)
		return
	}
}

// callHook invokes one hook and turns a panic into an ordinary error.
func callHook(ctx context.Context, hook Hook, payload any) (value any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			value = nil
			err = &PanicError{Mode: "hook", Value: recovered}
		}
	}()
	return hook(ctx, payload)
}

// callWaterfallLayer invokes one waterfall layer with a next continuation that
// may be called only once. The call is marked before the downstream chain runs,
// so a second call while the first is still in flight cannot re-run it either.
func callWaterfallLayer(layer WaterfallHandler, ctx context.Context, payload any, next func(context.Context) (any, error)) (value any, err error) {
	var mu sync.Mutex
	called := false
	guardedNext := func(nextCtx context.Context) (any, error) {
		mu.Lock()
		if called {
			mu.Unlock()
			return nil, errWaterfallNextCalledTwice
		}
		called = true
		mu.Unlock()
		return next(nextCtx)
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			value = nil
			err = &PanicError{Mode: "waterfall layer", Value: recovered}
		}
	}()
	return layer(ctx, payload, guardedNext)
}

// isBailValue reports whether value stops a serial dispatch. Any value except
// nil and the boolean false is a bail value.
func isBailValue(value any) bool {
	if value == nil {
		return false
	}
	if b, ok := value.(bool); ok {
		return b
	}
	return true
}
