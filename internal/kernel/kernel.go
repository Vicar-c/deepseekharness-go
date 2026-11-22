// Package kernel implements a small Cordis-inspired plugin kernel: everything
// the harness offers is composed as a plugin, plugins resolve shared services
// through the context, and lifecycle notifications flow through one event bus.
package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"time"
)

// Plugin is the unit of composition in the harness. Apply runs once during
// boot, after every earlier plugin has been applied, which gives plugins a
// deterministic dependency order.
type Plugin interface {
	// Name uniquely identifies the plugin inside one App.
	Name() string
	// Apply wires the plugin into the shared context.
	Apply(ctx *Context) error
}

// Starter is implemented by plugins that own goroutines or listeners and need
// to start accepting work after the whole boot graph has been applied.
type Starter interface {
	Start(ctx context.Context) error
}

// Stopper is implemented by plugins with resources that must be released.
type Stopper interface {
	Stop(ctx context.Context) error
}

// Context is the shared surface plugins use to register tools, subscribe to
// events and reach services provided by other plugins.
type Context struct {
	Logger   *slog.Logger
	Events   *Events
	Services *Services
}

// App owns one plugin graph and its lifecycle.
type App struct {
	mu       sync.Mutex
	logger   *slog.Logger
	events   *Events
	services *Services
	plugins  []Plugin
	names    map[string]struct{}
	active   []Plugin
	booted   bool
	closed   bool
}

// New creates an empty application with a fresh event bus and service registry.
func New(logger *slog.Logger) *App {
	if logger == nil {
		logger = slog.Default()
	}
	return &App{
		logger:   logger,
		events:   NewEvents(),
		services: NewServices(),
		names:    make(map[string]struct{}),
	}
}

// Use registers a plugin. Registration must happen before Boot.
func (a *App) Use(plugin Plugin) error {
	if plugin == nil {
		return errors.New("kernel: plugin is nil")
	}
	name := plugin.Name()
	if name == "" {
		return errors.New("kernel: plugin name is empty")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.booted {
		return fmt.Errorf("kernel: cannot register plugin %q after boot", name)
	}
	if _, dup := a.names[name]; dup {
		return fmt.Errorf("kernel: duplicate plugin %q", name)
	}
	a.names[name] = struct{}{}
	a.plugins = append(a.plugins, plugin)
	return nil
}

// Context returns the shared context handed to plugins during Apply.
func (a *App) Context() *Context {
	return &Context{Logger: a.logger, Events: a.events, Services: a.services}
}

// Events exposes the application event bus.
func (a *App) Events() *Events { return a.events }

// Services exposes the application service registry.
func (a *App) Services() *Services { return a.services }

// Boot applies every registered plugin in order and then starts the ones that
// implement Starter. A failing plugin aborts boot; already applied plugins are
// stopped again before Boot returns.
func (a *App) Boot(ctx context.Context) error {
	a.mu.Lock()
	if a.booted {
		a.mu.Unlock()
		return errors.New("kernel: already booted")
	}
	a.booted = true
	plugins := append([]Plugin(nil), a.plugins...)
	a.mu.Unlock()

	applyCtx := a.Context()
	for _, plugin := range plugins {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := plugin.Apply(applyCtx); err != nil {
			_ = a.stopActive(context.WithoutCancel(ctx))
			return fmt.Errorf("kernel: apply plugin %q: %w", plugin.Name(), err)
		}
		a.active = append(a.active, plugin)
		a.logger.Debug("plugin applied", "plugin", plugin.Name())
	}
	for _, plugin := range a.active {
		starter, ok := plugin.(Starter)
		if !ok {
			continue
		}
		if err := starter.Start(ctx); err != nil {
			_ = a.stopActive(context.WithoutCancel(ctx))
			return fmt.Errorf("kernel: start plugin %q: %w", plugin.Name(), err)
		}
	}
	a.logger.Info("kernel booted", "plugins", len(a.active))
	a.events.Emit("kernel/booted", len(a.active))
	return nil
}

// Shutdown stops applied plugins in reverse order and closes the event bus.
func (a *App) Shutdown(ctx context.Context) error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	a.mu.Unlock()
	err := a.stopActive(ctx)
	a.events.Close()
	a.logger.Info("kernel stopped")
	return err
}

func (a *App) stopActive(ctx context.Context) error {
	a.mu.Lock()
	active := append([]Plugin(nil), a.active...)
	a.active = nil
	a.mu.Unlock()
	var errs []error
	for i := len(active) - 1; i >= 0; i-- {
		stopper, ok := active[i].(Stopper)
		if !ok {
			continue
		}
		if err := stopper.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("kernel: stop plugin %q: %w", active[i].Name(), err))
		}
	}
	return errors.Join(errs...)
}

// Event is a named notification published on the application event bus.
type Event struct {
	Name    string
	Time    time.Time
	Payload any
}

// Handler receives every event of the subscribed name.
type Handler func(Event)

type subscription struct {
	id      uint64
	handler Handler
}

// Events is a synchronous, ordered pub/sub bus. Handlers run in subscription
// order on the emitting goroutine; a panicking handler is recovered so one bad
// subscriber cannot bring down the harness.
//
// Besides the fire-and-forget Emit listeners, the bus keeps the listeners of the
// value-returning dispatch modes (Serial, Bail, Parallel and Waterfall), which
// live in dispatch.go.
type Events struct {
	mu         sync.RWMutex
	handlers   map[string][]subscription
	nextID     uint64
	hooks      map[string][]hookSubscription
	waterfalls map[string][]waterfallSubscription
	closed     bool
}

// NewEvents creates an open event bus.
func NewEvents() *Events {
	return &Events{
		handlers:   make(map[string][]subscription),
		hooks:      make(map[string][]hookSubscription),
		waterfalls: make(map[string][]waterfallSubscription),
	}
}

// On subscribes to one event name and returns an idempotent unsubscribe func.
func (e *Events) On(name string, handler Handler) (unsubscribe func()) {
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
	e.handlers[name] = append(e.handlers[name], subscription{id: id, handler: handler})
	e.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			e.mu.Lock()
			defer e.mu.Unlock()
			list := e.handlers[name]
			for i, sub := range list {
				if sub.id != id {
					continue
				}
				e.handlers[name] = append(append([]subscription(nil), list[:i]...), list[i+1:]...)
				return
			}
		})
	}
}

// Emit publishes an event synchronously to the handlers subscribed at emit time.
func (e *Events) Emit(name string, payload any) {
	e.mu.RLock()
	if e.closed {
		e.mu.RUnlock()
		return
	}
	subs := append([]subscription(nil), e.handlers[name]...)
	e.mu.RUnlock()

	event := Event{Name: name, Time: time.Now().UTC(), Payload: payload}
	for _, sub := range subs {
		func() {
			defer func() { _ = recover() }()
			sub.handler(event)
		}()
	}
}

// Close stops delivering events and drops every subscription, including the
// listeners of the value-returning dispatch modes.
func (e *Events) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	e.handlers = make(map[string][]subscription)
	e.hooks = make(map[string][]hookSubscription)
	e.waterfalls = make(map[string][]waterfallSubscription)
}

// Services is a typed service locator: plugins provide values by their static
// type and consumers look them up with the same type.
type Services struct {
	mu    sync.RWMutex
	items map[reflect.Type]any
}

// NewServices creates an empty service registry.
func NewServices() *Services {
	return &Services{items: make(map[reflect.Type]any)}
}

// Provide registers value under its static type, replacing any previous value.
func Provide[T any](s *Services, value T) {
	if s == nil {
		panic("kernel: Provide on nil Services")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[typeOf[T]()] = value
}

// Lookup returns the value registered for type T.
func Lookup[T any](s *Services) (T, bool) {
	var zero T
	if s == nil {
		return zero, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.items[typeOf[T]()]
	if !ok {
		return zero, false
	}
	typed, ok := value.(T)
	return typed, ok
}

// Must returns the value registered for type T or panics. It is intended for
// wiring code where the dependency is a programming error.
func Must[T any](s *Services) T {
	value, ok := Lookup[T](s)
	if !ok {
		panic(fmt.Sprintf("kernel: service %s is not provided", typeOf[T]()))
	}
	return value
}

func typeOf[T any]() reflect.Type {
	return reflect.TypeOf((*T)(nil)).Elem()
}
