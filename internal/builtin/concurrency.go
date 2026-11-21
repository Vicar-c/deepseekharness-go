package builtin

// Concurrency-safe classification of the built-in tools.
//
// Only read-only tools overlap inside one model step. Everything that mutates
// state stays exclusive, so a batch can never interleave a write with a read of
// the same path, a shell command with a directory change, or two todo edits.
// The zero value for "no method" is exclusive, so a new tool is serial unless
// it explicitly opts in.

func (*readFileTool) ConcurrencySafe() bool  { return true }
func (*listDirTool) ConcurrencySafe() bool   { return true }
func (*globTool) ConcurrencySafe() bool      { return true }
func (*grepTool) ConcurrencySafe() bool      { return true }
func (*readImageTool) ConcurrencySafe() bool { return true }
func (*clockTool) ConcurrencySafe() bool     { return true }

// Exclusive on purpose (no ConcurrencySafe method):
//   - writeFileTool, editFileTool   mutate workspace files
//   - workingDirectoryTool          moves the shell's working directory
//   - shellTool                     runs arbitrary commands
//   - todoWriteTool                 rewrites the shared todo list
