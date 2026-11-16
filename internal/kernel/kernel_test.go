package kernel_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/kernel"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type testPlugin struct {
	name    string
	order   *[]string
	started *[]string
	stopped *[]string
	fail    bool
}

func (p *testPlugin) Name() string { return p.name }

func (p *testPlugin) Apply(*kernel.Context) error {
	if p.order != nil {
		*p.order = append(*p.order, p.name)
	}
	if p.fail {
		return errors.New("apply failed")
	}
	return nil
}

func (p *testPlugin) Start(context.Context) error {
	if p.started != nil {
		*p.started = append(*p.started, p.name)
	}
	return nil
}

func (p *testPlugin) Stop(context.Context) error {
	if p.stopped != nil {
		*p.stopped = append(*p.stopped, p.name)
	}
	return nil
}

func TestBootAppliesAndStartsInOrderThenStopsInReverse(t *testing.T) {
	app := kernel.New(testLogger())
	var applied, started, stopped []string
	for _, name := range []string{"a", "b", "c"} {
		if err := app.Use(&testPlugin{name: name, order: &applied, started: &started, stopped: &stopped}); err != nil {
			t.Fatalf("Use(%s): %v", name, err)
		}
	}
	if err := app.Boot(context.Background()); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(applied, want) {
		t.Fatalf("apply order = %v, want %v", applied, want)
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(started, want) {
		t.Fatalf("start order = %v, want %v", started, want)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if want := []string{"c", "b", "a"}; !reflect.DeepEqual(stopped, want) {
		t.Fatalf("stop order = %v, want %v", stopped, want)
	}
}

func TestUseRejectsDuplicatesAndLateRegistration(t *testing.T) {
	app := kernel.New(testLogger())
	if err := app.Use(&testPlugin{name: "dup"}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if err := app.Use(&testPlugin{name: "dup"}); err == nil {
		t.Fatal("expected duplicate plugin error")
	}
	if err := app.Boot(context.Background()); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if err := app.Use(&testPlugin{name: "late"}); err == nil {
		t.Fatal("expected error registering plugin after boot")
	}
}

func TestBootFailureStopsAlreadyAppliedPlugins(t *testing.T) {
	app := kernel.New(testLogger())
	var applied, stopped []string
	if err := app.Use(&testPlugin{name: "a", order: &applied, stopped: &stopped}); err != nil {
		t.Fatal(err)
	}
	if err := app.Use(&testPlugin{name: "b", order: &applied, stopped: &stopped, fail: true}); err != nil {
		t.Fatal(err)
	}
	if err := app.Boot(context.Background()); err == nil {
		t.Fatal("expected boot error")
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(applied, want) {
		t.Fatalf("applied = %v, want %v", applied, want)
	}
	if want := []string{"a"}; !reflect.DeepEqual(stopped, want) {
		t.Fatalf("stopped = %v, want %v", stopped, want)
	}
}

func TestEventBusSubscribeEmitUnsubscribe(t *testing.T) {
	events := kernel.NewEvents()
	var first, second []string
	unsubscribeFirst := events.On("demo", func(event kernel.Event) {
		value, _ := event.Payload.(string)
		first = append(first, value)
	})
	events.On("demo", func(event kernel.Event) {
		value, _ := event.Payload.(string)
		second = append(second, value)
	})
	events.Emit("demo", "one")
	unsubscribeFirst()
	events.Emit("demo", "two")
	if want := []string{"one"}; !reflect.DeepEqual(first, want) {
		t.Fatalf("first = %v, want %v", first, want)
	}
	if want := []string{"one", "two"}; !reflect.DeepEqual(second, want) {
		t.Fatalf("second = %v, want %v", second, want)
	}
	events.Close()
	events.Emit("demo", "three")
	if len(second) != 2 {
		t.Fatalf("events delivered after Close: %v", second)
	}
}

func TestEventBusIsolatesPanickingHandlers(t *testing.T) {
	events := kernel.NewEvents()
	events.On("demo", func(kernel.Event) { panic("boom") })
	var received int
	events.On("demo", func(kernel.Event) { received++ })
	events.Emit("demo", nil)
	if received != 1 {
		t.Fatalf("received = %d, want 1", received)
	}
}

type greeter interface{ Greet() string }

type englishGreeter struct{}

func (englishGreeter) Greet() string { return "hello" }

func TestServicesProvideAndLookup(t *testing.T) {
	services := kernel.NewServices()
	kernel.Provide(services, 42)
	kernel.Provide(services, greeter(englishGreeter{}))

	if value, ok := kernel.Lookup[int](services); !ok || value != 42 {
		t.Fatalf("Lookup[int] = %v, %v", value, ok)
	}
	if got := kernel.Must[greeter](services).Greet(); got != "hello" {
		t.Fatalf("Greet() = %q", got)
	}
	if _, ok := kernel.Lookup[string](services); ok {
		t.Fatal("unexpected service for string")
	}
}

func TestContextExposesSharedFacilities(t *testing.T) {
	app := kernel.New(testLogger())
	kernel.Provide(app.Services(), "value")
	context := app.Context()
	if context.Events == nil || context.Services == nil || context.Logger == nil {
		t.Fatal("context facilities are not wired")
	}
	if got := kernel.Must[string](context.Services); got != "value" {
		t.Fatalf("service = %q", got)
	}
}
