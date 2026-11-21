package profiles

import "sort"

// bundle is one compile-time composition layer. Bundles are the Go equivalent
// of the upstream `packages/bundle/*` patches: an ordered insert over the empty
// root, addressable by row id from later layers.
type bundle struct {
	name   string
	insert []patchEntry
}

// BaseBundle, WebBundle and HeadlessBundle are the implemented bundles. The
// upstream sdk-app / sdk-minimal / acp-app bundles arrive with their transports
// in P4 and are intentionally absent rather than silently empty.
const (
	BaseBundle     = "base"
	WebBundle      = "web-app"
	HeadlessBundle = "headless"
)

// Row ids of the transport declarations the CLI gates on.
const (
	TransportWeb = TransportRowPrefix + "web"
	TransportCLI = TransportRowPrefix + "cli"
)

var bundlesByName = map[string]bundle{
	BaseBundle: {
		name: BaseBundle,
		insert: []patchEntry{{Insert: []rowSpec{
			{ID: "tools.fs", Name: "builtin/fs"},
			{ID: "tools.shell", Name: "builtin/shell"},
			{ID: "tools.time", Name: "builtin/time"},
			{ID: "tools.todo", Name: "builtin/todo"},
		}}},
	},
	WebBundle: {
		name: WebBundle,
		insert: []patchEntry{{Insert: []rowSpec{
			{ID: TransportWeb, Name: "server/web"},
		}}},
	},
	HeadlessBundle: {
		name: HeadlessBundle,
		insert: []patchEntry{{Insert: []rowSpec{
			{ID: TransportCLI, Name: "cli/headless"},
		}}},
	},
}

// builtinProfiles are the profiles available without a user profile directory.
// The default profile keeps every implemented transport, so an existing command
// line keeps working; selecting `web` or `headless` narrows the composition.
var builtinProfiles = map[string][]string{
	"default":  {BaseBundle, WebBundle, HeadlessBundle},
	"web":      {BaseBundle, WebBundle},
	"headless": {BaseBundle, HeadlessBundle},
}

// BundleNames lists the implemented bundles in alphabetical order.
func BundleNames() []string {
	names := make([]string, 0, len(bundlesByName))
	for name := range bundlesByName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ProfileNames lists the built-in profiles in alphabetical order.
func ProfileNames() []string {
	names := make([]string, 0, len(builtinProfiles))
	for name := range builtinProfiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
