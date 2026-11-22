// Package prompt assembles the system prompt from ordered sections. The
// assembly is static: sections are rendered once per loop and recorded as a
// system/message event, so the model-visible prefix can be rebuilt from the log.
package prompt

import "strings"

// Base is the core instruction block of the harness.
const Base = `You are DeepSeek Harness (dsh), a Go-rewritten agent harness.
Work inside the configured workspace. Prefer tools over guessing: read files before editing,
run commands to verify your work, and keep answers concise. Tool results arrive as user-visible
tool messages; when you are done, reply with the final answer only.`

// Section is one named block of the system prompt.
type Section struct {
	Name string
	Text string
}

// Prompt is an ordered assembly of sections.
type Prompt struct {
	sections []Section
}

// New assembles a prompt from sections.
func New(sections ...Section) *Prompt {
	return &Prompt{sections: append([]Section{}, sections...)}
}

// Add appends a section and returns the prompt, so calls can chain.
func (p *Prompt) Add(section Section) *Prompt {
	p.sections = append(p.sections, section)
	return p
}

// Sections returns a copy of the sections in assembly order.
func (p *Prompt) Sections() []Section {
	return append([]Section{}, p.sections...)
}

// Render joins the non-empty sections with a blank line between them. Whitespace
// only sections are skipped, so an omitted optional block leaves no gap.
func (p *Prompt) Render() string {
	parts := make([]string, 0, len(p.sections))
	for _, section := range p.sections {
		text := strings.TrimSpace(section.Text)
		if text == "" {
			continue
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n\n")
}

// Runtime is the environment the prompt describes to the model.
type Runtime struct {
	Model     string
	Workspace string
	Tools     []string
}

// Harness assembles the default prompt: the base instructions plus one block of
// runtime facts, so the model knows where it runs and which tools it can call.
func Harness(runtime Runtime) *Prompt {
	lines := []string{"Runtime context:"}
	if model := strings.TrimSpace(runtime.Model); model != "" {
		lines = append(lines, "- model: "+model)
	}
	if workspace := strings.TrimSpace(runtime.Workspace); workspace != "" {
		lines = append(lines, "- workspace: "+workspace)
	}
	tools := make([]string, 0, len(runtime.Tools))
	for _, tool := range runtime.Tools {
		if name := strings.TrimSpace(tool); name != "" {
			tools = append(tools, name)
		}
	}
	if len(tools) > 0 {
		lines = append(lines, "- tools: "+strings.Join(tools, ", "))
	}
	return New(
		Section{Name: "base", Text: Base},
		Section{Name: "runtime", Text: strings.Join(lines, "\n")},
	)
}
