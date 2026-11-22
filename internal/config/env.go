package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Layer sources recorded in EnvLayer.Source.
const (
	envSourceProject = "project-env"
	envSourceUser    = "user-env"
)

// envFileName is the file name read from each layer directory.
const envFileName = ".env"

// bootstrapNames are the exact variable names no .env file may set: they decide
// how the process starts, where its code and instructions load from, or how it
// reaches the network. Names are matched after upper-casing them, so the set is
// case-insensitive.
var bootstrapNames = map[string]struct{}{
	// Process launch and module resolution.
	"PATH": {}, "HOME": {}, "USERPROFILE": {}, "SHELL": {},
	"NODE_OPTIONS": {}, "NODE_PATH": {}, "NODE_EXTRA_CA_CERTS": {},
	"LD_PRELOAD": {}, "LD_LIBRARY_PATH": {}, "LD_AUDIT": {},
	// Interpreter startup hooks.
	"BASH_ENV": {}, "ENV": {}, "SHELLOPTS": {}, "BASHOPTS": {},
	"PERL5OPT": {}, "PERL5LIB": {}, "PYTHONSTARTUP": {}, "PYTHONPATH": {},
	"RUBYOPT": {}, "RUBYLIB": {},
	"JAVA_TOOL_OPTIONS": {}, "_JAVA_OPTIONS": {}, "JDK_JAVA_OPTIONS": {},
	"PYTHONHOME": {},
	// Version-control hooks, config redirects, and ambient command selectors.
	"GIT_SSH": {}, "GIT_SSH_COMMAND": {}, "GIT_EXTERNAL_DIFF": {}, "GIT_PAGER": {},
	"GIT_EDITOR": {}, "GIT_ASKPASS": {}, "SSH_ASKPASS": {},
	"GIT_CONFIG_GLOBAL": {}, "GIT_CONFIG_SYSTEM": {}, "GIT_CONFIG_COUNT": {},
	"EDITOR": {}, "VISUAL": {}, "PAGER": {}, "BROWSER": {},
	// Network reach and trust.
	"DEEPSEEK_BASE_URL": {}, "DEEPSEEK_SEARCH_BASE_URL": {},
	"SSL_CERT_FILE": {}, "SSL_CERT_DIR": {},
	"HTTP_PROXY": {}, "HTTPS_PROXY": {}, "ALL_PROXY": {}, "NO_PROXY": {},
	"REQUESTS_CA_BUNDLE": {}, "CURL_CA_BUNDLE": {},
	"NODE_TLS_REJECT_UNAUTHORIZED": {},
}

// bootstrapPrefixes are the name prefixes no .env file may set. They are
// matched case-insensitively against the upper-cased name.
var bootstrapPrefixes = []string{"DSH_", "XDG_", "DYLD_", "BASH_FUNC_"}

// homeLayerProxyNames are the proxy names the home layer may set in any letter
// case: proxy clients read both spellings.
var homeLayerProxyNames = map[string]struct{}{
	"HTTP_PROXY": {}, "HTTPS_PROXY": {}, "ALL_PROXY": {}, "NO_PROXY": {},
}

// homeLayerEndpointNames are the DeepSeek endpoint names the home layer may set,
// and only as spelled here: consumers look them up by exact name on POSIX, so
// another spelling would load without effect.
var homeLayerEndpointNames = map[string]struct{}{
	"DEEPSEEK_BASE_URL": {}, "DEEPSEEK_SEARCH_BASE_URL": {},
}

// EnvLayer is one .env file that was read. A layer is reported only when its
// file existed and could be read, even when the file declared no variables.
type EnvLayer struct {
	// Path is the path of the file that was read.
	Path string
	// Source identifies the layer: "project-env" for <cwd>/.env, or "user-env"
	// for <home>/.env.
	Source string
	// Names lists the variables the file declared, in file order, each once.
	Names []string
}

// EnvLoadOptions configures LoadEnvLayers. The zero value reads the process
// environment and writes through to it.
type EnvLoadOptions struct {
	// BinName prefixes the diagnostics and the bootstrap-only error.
	BinName string
	// Cwd is the directory holding the project .env. An empty value uses the
	// process working directory.
	Cwd string
	// Home is the directory holding the user .env. An empty value disables the
	// user layer.
	Home string
	// Lookup reports a variable from the current environment. A nil lookup means
	// os.LookupEnv.
	Lookup func(name string) (string, bool)
	// Setenv stores a variable in the environment. A nil setter means
	// os.Setenv.
	Setenv func(name, value string) error
	// Warn receives one complete diagnostic line, newline included. A nil sink
	// writes to os.Stderr.
	Warn func(line string)
}

// LoadEnvLayers reads <cwd>/.env and <home>/.env, refuses bootstrap-only names,
// then applies the remaining values without replacing a name the environment
// already has. The inherited environment outranks the project layer, which
// outranks the user layer.
//
// Both files are read and checked before either is applied, so a refused name
// leaves the environment untouched. A missing file is not an error; a file that
// cannot be read for another reason is reported through Warn and skipped. When
// Cwd and Home resolve to the same directory the single file is read once, as
// the project layer, and is exempt from bootstrap-only names exactly as the user
// layer would be: the exemption follows the directory, not the layer name.
//
// The returned layers are the files that were read, project first. On error the
// layer list is nil.
func LoadEnvLayers(options EnvLoadOptions) ([]EnvLayer, error) {
	lookup := options.Lookup
	if lookup == nil {
		lookup = os.LookupEnv
	}
	setenv := options.Setenv
	if setenv == nil {
		setenv = os.Setenv
	}
	warn := options.Warn
	if warn == nil {
		warn = func(line string) {
			_, _ = fmt.Fprint(os.Stderr, line)
		}
	}

	cwd := resolveEnvDir(options.Cwd)
	if cwd == "" {
		if workingDirectory, err := os.Getwd(); err == nil {
			cwd = workingDirectory
		}
	}
	home := resolveEnvDir(options.Home)
	cwdIsHome := home != "" && sameEnvDirectory(cwd, home)

	loader := envLoader{
		binName: options.BinName,
		homeEnv: filepath.Join(home, envFileName),
		lookup:  lookup,
		setenv:  setenv,
		warn:    warn,
	}

	// Read and validate each layer before applying either one, so a refusal in
	// the user layer cannot leave a project-layer value behind.
	files := make([]envFile, 0, 2)
	project, ok, err := loader.read(cwd, envSourceProject, cwdIsHome)
	if err != nil {
		return nil, err
	}
	if ok {
		if err := loader.validate(project); err != nil {
			return nil, err
		}
		files = append(files, project)
	}
	if home != "" && !cwdIsHome {
		user, ok, err := loader.read(home, envSourceUser, true)
		if err != nil {
			return nil, err
		}
		if ok {
			if err := loader.validate(user); err != nil {
				return nil, err
			}
			files = append(files, user)
		}
	}

	applied := make(map[string]bool)
	for _, file := range files {
		if err := loader.apply(file, applied); err != nil {
			return nil, err
		}
	}

	layers := make([]EnvLayer, 0, len(files))
	for _, file := range files {
		layers = append(layers, EnvLayer{Path: file.path, Source: file.source, Names: file.names})
	}
	return layers, nil
}

// ParseEnv parses the content of one .env file. It returns the values keyed by
// name and the declared names in first-declaration order.
//
// It understands blank lines, comment lines, an optional "export " prefix,
// unquoted values, single- or double-quoted values, a trailing comment after an
// unquoted value when whitespace precedes the "#", \n, \t, \" and \\ escapes
// inside double quotes, an empty value, and a name without "=". A backslash
// inside single quotes is literal and no $NAME expansion happens. A duplicate
// name keeps the last value and its first position. A name must match
// [A-Za-z_][A-Za-z0-9_]*; a line that does not yield such a name is an error
// naming the line number.
func ParseEnv(content string) (map[string]string, []string, error) {
	values := make(map[string]string)
	names := make([]string, 0)
	for index, rawLine := range strings.Split(content, "\n") {
		lineNumber := index + 1
		line := strings.TrimLeft(strings.TrimSuffix(rawLine, "\r"), " \t")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := trimExportPrefix(line); ok {
			line = rest
		}
		name, rawValue, hasValue := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !isEnvName(name) {
			return nil, nil, fmt.Errorf("line %d: invalid variable name %q", lineNumber, name)
		}
		value := ""
		if hasValue {
			parsed, err := parseEnvValue(rawValue, lineNumber)
			if err != nil {
				return nil, nil, err
			}
			value = parsed
		}
		if _, seen := values[name]; !seen {
			names = append(names, name)
		}
		values[name] = value
	}
	return values, names, nil
}

// envFile is one parsed layer that passed validation and is ready to apply.
type envFile struct {
	path   string
	source string
	values map[string]string
	names  []string
	isHome bool
}

// envLoader carries the resolved options of one LoadEnvLayers call.
type envLoader struct {
	binName string
	homeEnv string
	lookup  func(name string) (string, bool)
	setenv  func(name, value string) error
	warn    func(line string)
}

// read parses one layer directory. A missing .env yields ok=false without a
// diagnostic; any other read failure is reported through the warn sink and also
// yields ok=false. A parse failure is returned as an error for the call to
// abort.
func (l envLoader) read(dir, source string, isHome bool) (envFile, bool, error) {
	path := filepath.Join(dir, envFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			l.warn(fmt.Sprintf("%s: failed to load .env: %v\n", l.binName, err))
		}
		return envFile{}, false, nil
	}
	values, names, err := ParseEnv(string(StripBOM(data)))
	if err != nil {
		return envFile{}, false, fmt.Errorf("%s: parse %s: %w", l.binName, path, err)
	}
	return envFile{path: path, source: source, values: values, names: names, isHome: isHome}, true, nil
}

// validate refuses a bootstrap-only name. A file in the home directory may
// additionally set the route names a user owns; the project layer may not,
// because it travels with a repository.
func (l envLoader) validate(file envFile) error {
	for _, name := range file.names {
		if !isBootstrapOnlyName(name) {
			continue
		}
		routeName := isHomeLayerRouteName(name)
		if file.isHome && routeName {
			continue
		}
		upper := strings.ToUpper(name)
		remedy := fmt.Sprintf("export %s instead of putting it in a .env file", name)
		if routeName {
			remedy = fmt.Sprintf("export %s, or put it in %s, which does not travel with a repository", name, l.homeEnv)
		} else if _, isEndpoint := homeLayerEndpointNames[upper]; isEndpoint {
			remedy = fmt.Sprintf("spell it %s, then export it or put it in %s, which does not travel with a repository", upper, l.homeEnv)
		}
		return fmt.Errorf(
			`%s: %s sets %q, which only the launching environment may set `+
				`(it decides how this process starts, where its code and instructions load from, `+
				`or how it reaches the network); %s`,
			l.binName, file.path, name, remedy,
		)
	}
	return nil
}

// apply materializes one layer's values without replacing a name that is
// already in the environment or that a higher-ranked layer supplied.
func (l envLoader) apply(file envFile, applied map[string]bool) error {
	for _, name := range file.names {
		if applied[name] {
			continue
		}
		if _, present := l.lookup(name); present {
			continue
		}
		if err := l.setenv(name, file.values[name]); err != nil {
			return fmt.Errorf("%s: apply %s from %s: %w", l.binName, name, file.path, err)
		}
		applied[name] = true
	}
	return nil
}

// isBootstrapOnlyName reports whether only the launching environment may set
// the name. Matching is case-insensitive for both exact names and prefixes.
func isBootstrapOnlyName(name string) bool {
	upper := strings.ToUpper(name)
	if _, ok := bootstrapNames[upper]; ok {
		return true
	}
	for _, prefix := range bootstrapPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

// isHomeLayerRouteName reports whether the user layer may set the name: any
// letter case for a proxy name, the exact spelling for an endpoint name.
func isHomeLayerRouteName(name string) bool {
	if _, ok := homeLayerProxyNames[strings.ToUpper(name)]; ok {
		return true
	}
	_, ok := homeLayerEndpointNames[name]
	return ok
}

// sameEnvDirectory reports whether two resolved directories are the same
// directory. Windows compares case-insensitively because its file systems do.
func sameEnvDirectory(a, b string) bool {
	if a == b {
		return true
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return false
}

// resolveEnvDir returns dir as a cleaned absolute path. An empty dir stays empty
// so a caller can tell "no home directory" from the process working directory.
func resolveEnvDir(dir string) string {
	if dir == "" {
		return ""
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Clean(dir)
	}
	return filepath.Clean(absolute)
}

// trimExportPrefix removes an optional "export " prefix from a line whose
// leading whitespace is already removed. ok is false when the word is part of a
// longer name, such as exportKEY=1.
func trimExportPrefix(line string) (string, bool) {
	const prefix = "export"
	if !strings.HasPrefix(line, prefix) {
		return line, false
	}
	rest := line[len(prefix):]
	if rest == "" {
		return "", true
	}
	if rest[0] != ' ' && rest[0] != '\t' {
		return line, false
	}
	return strings.TrimLeft(rest, " \t"), true
}

// parseEnvValue turns the text after the first "=" into a value.
func parseEnvValue(raw string, lineNumber int) (string, error) {
	trimmed := strings.TrimLeft(raw, " \t")
	if trimmed == "" {
		return "", nil
	}
	switch trimmed[0] {
	case '"':
		value, rest, err := parseDoubleQuoted(trimmed[1:], lineNumber)
		if err != nil {
			return "", err
		}
		if err := checkQuotedRemainder(rest, lineNumber); err != nil {
			return "", err
		}
		return value, nil
	case '\'':
		end := strings.IndexByte(trimmed[1:], '\'')
		if end < 0 {
			return "", fmt.Errorf("line %d: unterminated single-quoted value", lineNumber)
		}
		if err := checkQuotedRemainder(trimmed[1+end+1:], lineNumber); err != nil {
			return "", err
		}
		return trimmed[1 : 1+end], nil
	default:
		return parseUnquotedValue(raw), nil
	}
}

// parseDoubleQuoted decodes the body of a double-quoted value, stopping at the
// closing quote. It returns the value and the unconsumed text after the quote.
func parseDoubleQuoted(raw string, lineNumber int) (string, string, error) {
	var builder strings.Builder
	for index := 0; index < len(raw); index++ {
		switch raw[index] {
		case '"':
			return builder.String(), raw[index+1:], nil
		case '\\':
			if index+1 == len(raw) {
				return "", "", fmt.Errorf("line %d: unterminated double-quoted value", lineNumber)
			}
			index++
			switch raw[index] {
			case 'n':
				builder.WriteByte('\n')
			case 't':
				builder.WriteByte('\t')
			case '"':
				builder.WriteByte('"')
			case '\\':
				builder.WriteByte('\\')
			default:
				// An escape the dotenv subset does not define keeps its
				// backslash, so a Windows path survives unchanged.
				builder.WriteByte('\\')
				builder.WriteByte(raw[index])
			}
		default:
			builder.WriteByte(raw[index])
		}
	}
	return "", "", fmt.Errorf("line %d: unterminated double-quoted value", lineNumber)
}

// checkQuotedRemainder rejects unexpected text after a closing quote while
// allowing whitespace and a trailing comment.
func checkQuotedRemainder(rest string, lineNumber int) error {
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" || strings.HasPrefix(rest, "#") {
		return nil
	}
	return fmt.Errorf("line %d: unexpected text after quoted value", lineNumber)
}

// parseUnquotedValue trims an unquoted value and drops a trailing comment. A "#"
// starts a comment only when whitespace precedes it, so a fragment such as
// value#anchor stays part of the value.
func parseUnquotedValue(raw string) string {
	if comment := unquotedCommentStart(raw); comment >= 0 {
		raw = raw[:comment]
	}
	return strings.TrimSpace(raw)
}

// unquotedCommentStart returns the index of the "#" that starts a trailing
// comment, or -1 when the value has none.
func unquotedCommentStart(raw string) int {
	for index := 1; index < len(raw); index++ {
		if raw[index] == '#' && (raw[index-1] == ' ' || raw[index-1] == '\t') {
			return index
		}
	}
	return -1
}

// isEnvName reports whether name matches [A-Za-z_][A-Za-z0-9_]*.
func isEnvName(name string) bool {
	if name == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		switch {
		case character == '_',
			character >= 'A' && character <= 'Z',
			character >= 'a' && character <= 'z':
		case index > 0 && character >= '0' && character <= '9':
		default:
			return false
		}
	}
	return true
}
