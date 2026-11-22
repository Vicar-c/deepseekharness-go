package prompt_test

import (
	"strings"
	"testing"

	"github.com/Vicar-c/deepseekharness-go/internal/prompt"
)

func TestRenderJoinsNonEmptySections(t *testing.T) {
	assembled := prompt.New(
		prompt.Section{Name: "base", Text: "base text"},
		prompt.Section{Name: "empty", Text: "   \n"},
		prompt.Section{Name: "runtime", Text: "runtime text"},
	)
	if got, want := assembled.Render(), "base text\n\nruntime text"; got != want {
		t.Fatalf("Render = %q, want %q", got, want)
	}
	if len(assembled.Sections()) != 3 {
		t.Fatalf("sections = %d", len(assembled.Sections()))
	}
}

func TestRenderTrimsSectionEdges(t *testing.T) {
	assembled := prompt.New(prompt.Section{Name: "base", Text: "\n  padded  \n"})
	if got := assembled.Render(); got != "padded" {
		t.Fatalf("Render = %q", got)
	}
}

func TestAddKeepsOrder(t *testing.T) {
	assembled := prompt.New().Add(prompt.Section{Name: "a", Text: "first"}).Add(prompt.Section{Name: "b", Text: "second"})
	if got, want := assembled.Render(), "first\n\nsecond"; got != want {
		t.Fatalf("Render = %q, want %q", got, want)
	}
}

func TestHarnessDescribesTheRuntime(t *testing.T) {
	rendered := prompt.Harness(prompt.Runtime{
		Model:     "deepseek-chat",
		Workspace: `C:\work`,
		Tools:     []string{"read", "grep", " "},
	}).Render()
	for _, want := range []string{
		prompt.Base,
		"Runtime context:",
		"- model: deepseek-chat",
		`- workspace: C:\work`,
		"- tools: read, grep",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered prompt is missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "- tools: read, grep, ") {
		t.Fatalf("blank tool names leaked into the prompt:\n%s", rendered)
	}
}

func TestHarnessOmitsEmptyRuntimeFacts(t *testing.T) {
	rendered := prompt.Harness(prompt.Runtime{}).Render()
	if strings.Contains(rendered, "- model:") || strings.Contains(rendered, "- tools:") {
		t.Fatalf("empty runtime facts leaked into the prompt:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Runtime context:") {
		t.Fatalf("runtime block missing:\n%s", rendered)
	}
}

func TestHarnessIsDeterministic(t *testing.T) {
	runtime := prompt.Runtime{Model: "m", Workspace: "/w", Tools: []string{"a", "b"}}
	first := prompt.Harness(runtime).Render()
	second := prompt.Harness(runtime).Render()
	if first != second {
		t.Fatal("Harness is not deterministic")
	}
}
