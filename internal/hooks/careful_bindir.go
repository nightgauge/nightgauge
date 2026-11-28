package hooks

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// BinDirWriteVerbs are the commands that write, replace or remove a file named
// as an argument (dd through of=, curl -o, wget -O). The OpenCode permission
// globs (adapters/opencode_guard.go) and the careful gate's runtime resolver
// share this one list (#3622).
var BinDirWriteVerbs = []string{
	"cp", "mv", "ln", "install", "tee", "rsync",
	"dd", "curl", "wget", "rm", "unlink", "truncate", "chmod",
}

var binDirWriteVerbSet = func() map[string]bool {
	m := make(map[string]bool, len(BinDirWriteVerbs))
	for _, v := range BinDirWriteVerbs {
		m[v] = true
	}
	return m
}()

// binDirEnvVar is the variable that names the stage's nightgauge binary.
const binDirEnvVar = "NIGHTGAUGE_BIN"

// binDirGuardActive reports whether the OpenCode bin-dir rule applies: only in
// an OpenCode stage with an absolute NIGHTGAUGE_BIN. It is independent of the
// careful lock (#3622).
func binDirGuardActive() (string, bool) {
	if os.Getenv("NIGHTGAUGE_ADAPTER") != "opencode" {
		return "", false
	}
	bin := os.Getenv(binDirEnvVar)
	if bin == "" || !filepath.IsAbs(bin) {
		return "", false
	}
	return bin, true
}

// shVal is one expanded shell value: ok is false when it could not be resolved
// statically, tainted when it derives from NIGHTGAUGE_BIN.
type shVal struct {
	val     string
	ok      bool
	tainted bool
}

// shStmt is one simple command: its raw words and its output redirection
// targets (raw).
type shStmt struct {
	words  []string
	redirs []string
}

type binDirEval struct {
	vars     map[string]shVal
	cwd      string
	cwdOK    bool
	cwdTaint bool
	forms    []string
	mentions bool
	depth    int
	// xargsFed makes the next statement gain the stdin item xargs appends
	// (set on an `xargs env -S` sub-run, consumed by its first statement).
	xargsFed bool
	denyVerb string
	denyWhat string
}

// binDirWriteReason returns a deny reason when command writes inside the
// directory of binPath, or "" to allow. It is a conservative static evaluator:
// variables, quote concatenation, `cd`, static dirname/basename/pwd/realpath
// substitutions, redirections, `sh -c`, and heredoc bodies a shell reads as
// its script (`bash <<EOF`, `cat <<EOF | bash`) are resolved; bodies fed to
// other commands are ignored. Whatever it cannot
// resolve is denied only when it derives from NIGHTGAUGE_BIN (fail closed for
// that variable, open otherwise).
func binDirWriteReason(command, cwd, binPath string) string {
	dir := filepath.Clean(filepath.Dir(binPath))
	forms := []string{dir}
	if r, err := filepath.EvalSymlinks(dir); err == nil && r != dir {
		forms = append(forms, r)
	}
	e := &binDirEval{
		vars:     map[string]shVal{binDirEnvVar: {val: binPath, ok: true, tainted: true}},
		cwd:      cwd,
		cwdOK:    cwd != "" && filepath.IsAbs(cwd),
		forms:    forms,
		mentions: strings.Contains(command, binDirEnvVar),
	}
	if e.cwdOK {
		e.vars["PWD"] = shVal{val: cwd, ok: true}
	}
	if e.run(command) {
		return e.denyVerb + " writes " + e.denyWhat + " inside " + binDirEnvVar + "'s directory " + dir
	}
	return ""
}

// run evaluates a script; true means deny (denyVerb/denyWhat set).
//
// The script's command text (heredoc bodies removed) is lexed and evaluated as
// one unit, so a statement spanning a heredoc opened inside `$( … )` stays
// whole. Then every heredoc body opened by a chunk that runs a shell reading
// its script from stdin (readsStdinScript) is evaluated as a script, with the
// state reached at that chunk. Unquoted delimiters: the outer shell expands the
// body with that state, so this is exact. Quoted ones: the inner shell expands
// it; it inherits NIGHTGAUGE_BIN, and using outer non-exported variables only
// over-approximates (fails closed). Every body of such a chunk is evaluated,
// even one feeding another command on the same line.
func (e *binDirEval) run(script string) bool {
	if e.depth > 4 {
		if e.mentions {
			return e.deny("a deeply nested script", "a command that names "+binDirEnvVar)
		}
		return false
	}
	chunks := splitHeredocs(script)
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.text
	}
	for _, st := range lexShell(strings.Join(texts, "\n")) {
		if e.stmt(st) {
			return true
		}
	}
	for k, ch := range chunks {
		if len(ch.bodies) == 0 {
			continue
		}
		// State at this chunk: replay the text through it on a throwaway
		// clone (its decisions are already covered by the pass above).
		pre := e.clone()
		for _, st := range lexShell(strings.Join(texts[:k+1], "\n")) {
			pre.stmt(st)
		}
		stdinShell := false
		var chdirs []shVal
		for _, st := range lexShell(ch.text) {
			if stdinShell, chdirs = pre.readsStdinScript(st); stdinShell {
				break
			}
		}
		if !stdinShell {
			continue
		}
		for _, body := range ch.bodies {
			sub := pre.clone()
			for _, c := range chdirs {
				sub.chdirLiteral(c)
			}
			sub.depth++
			if sub.run(body) {
				e.denyVerb, e.denyWhat = sub.denyVerb, sub.denyWhat
				return true
			}
		}
	}
	return false
}

// stdinScriptPaths are operands naming the shell's own standard input.
var stdinScriptPaths = map[string]bool{
	"-": true, "/dev/stdin": true, "/dev/fd/0": true, "/proc/self/fd/0": true,
}

// readsStdinScript reports whether st runs a shell (or source) that reads its
// script from standard input, so a heredoc on the line is executed, and the
// directories its wrappers move that shell to (`env -C DIR bash <<EOF`).
func (e *binDirEval) readsStdinScript(st shStmt) (bool, []shVal) {
	words := st.words
	i := 0
	for i < len(words) && assignRE.MatchString(words[i]) {
		i++
	}
	words = words[i:]
	argv := make([]shVal, len(words))
	for j, w := range words {
		argv[j] = e.expand(w)
	}
	argv, _, info := e.stripWrappers(argv, words)
	if s := info.split; s != nil {
		if !s.ok || strings.Contains(s.val, `\`) {
			return s.tainted || e.mentions, info.chdirs
		}
		for _, inner := range lexShell("env " + s.val + " " + strings.Join(info.splitRest, " ")) {
			if reads, cds := e.readsStdinScript(inner); reads {
				return true, append(slices.Clone(info.chdirs), cds...)
			}
		}
		return false, nil
	}
	if info.xargs && !info.hasReplace && len(argv) > 0 {
		argv = append(argv, shVal{}) // the stdin item xargs appends
	}
	return e.stdinScript(argv), info.chdirs
}

// stdinScript reports whether the stripped argv runs a shell (or source) that
// reads its script from standard input.
func (e *binDirEval) stdinScript(argv []shVal) bool {
	if len(argv) == 0 || !argv[0].ok {
		return false
	}
	verb := filepath.Base(argv[0].val)
	args := argv[1:]
	// An operand that cannot be resolved may be /dev/stdin: fail closed when
	// it derives from NIGHTGAUGE_BIN, open otherwise (ADR-002).
	unresolvedIsStdin := func(a shVal) bool { return a.tainted || e.mentions }
	if verb == "source" || verb == "." {
		if len(args) == 0 {
			return false
		}
		if !args[0].ok {
			return unresolvedIsStdin(args[0])
		}
		return stdinScriptPaths[args[0].val]
	}
	if !shellWrappers[verb] {
		return false
	}
	for j := 0; j < len(args); j++ {
		a := args[j]
		if !a.ok {
			return unresolvedIsStdin(a)
		}
		v := a.val
		switch {
		case v == "--":
			if j+1 >= len(args) {
				return true
			}
			if !args[j+1].ok {
				return unresolvedIsStdin(args[j+1])
			}
			return stdinScriptPaths[args[j+1].val]
		case v == "-":
			return true
		case v == "--rcfile" || v == "--init-file":
			j++ // takes a file value
		case strings.HasPrefix(v, "--"):
			continue // long option (--norc, --login, ...)
		case strings.HasPrefix(v, "-") || strings.HasPrefix(v, "+"):
			flags := v[1:]
			if strings.ContainsRune(flags, shellCommandLetter) {
				return false // -c script: stdin is data
			}
			if strings.ContainsRune(flags, shellStdinLetter) {
				return true
			}
			if strings.ContainsAny(flags, shellValueLetters) && j+1 < len(args) {
				j++ // -o option takes a value
			}
		default:
			return stdinScriptPaths[v]
		}
	}
	return true // no script operand: the shell reads its script from stdin
}

const (
	shellCommandLetter = 'c'  // -c SCRIPT: stdin is data, not the script
	shellStdinLetter   = 's'  // -s: read the script from stdin
	shellValueLetters  = "oO" // -o/-O OPTION take a value
)

func (e *binDirEval) deny(verb, what string) bool {
	e.denyVerb, e.denyWhat = verb, what
	return true
}

var assignRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

func (e *binDirEval) stmt(st shStmt) bool {
	fed := e.xargsFed
	e.xargsFed = false
	for _, r := range st.redirs {
		if e.checkTarget("a redirection", e.expand(r)) {
			return true
		}
	}
	words := st.words
	// Leading assignments: a bare assignment sets the variable; one that
	// prefixes a command only reaches that command's environment.
	i := 0
	for i < len(words) && assignRE.MatchString(words[i]) {
		i++
	}
	if i == len(words) {
		for _, w := range words {
			e.assign(w)
		}
		return false
	}
	words = words[i:]

	argv := make([]shVal, len(words))
	for j, w := range words {
		argv[j] = e.expand(w)
	}
	argv, rawArgs, info := e.stripWrappers(argv, words)
	return e.wrapped(argv, rawArgs, info, fed)
}

// wrapped applies what the stripped wrappers do to the inner command — their
// chdirs, output files, `env -S` string and the xargs stdin item — then
// evaluates it. Chdirs apply to a clone, so they never leak to later
// statements.
func (e *binDirEval) wrapped(argv []shVal, rawArgs []string, info wrapInfo, fed bool) bool {
	ev := e
	if len(info.chdirs) > 0 {
		ev = e.clone()
	}
	denied := func() bool {
		if ev != e {
			e.denyVerb, e.denyWhat = ev.denyVerb, ev.denyWhat
		}
		return true
	}
	applied := 0
	for _, o := range info.outputs {
		for ; applied < o.after; applied++ {
			ev.chdirLiteral(info.chdirs[applied])
		}
		if ev.checkTarget(o.verb, o.val) {
			return denied()
		}
	}
	for ; applied < len(info.chdirs); applied++ {
		ev.chdirLiteral(info.chdirs[applied])
	}
	xargsFed := fed || info.xargs && !info.hasReplace
	if info.split != nil {
		s := *info.split
		// env -S has its own escapes (`\_` separates words); a string using
		// them is not re-lexed as shell, so it is unresolved.
		if !s.ok || strings.Contains(s.val, `\`) {
			if s.tainted || e.mentions {
				return e.deny("env -S", "an unresolved split string")
			}
			return false
		}
		// env re-parses the split words as its own argv, then the rest.
		sub := ev.clone()
		sub.depth++
		sub.xargsFed = xargsFed
		if sub.run("env " + s.val + " " + strings.Join(info.splitRest, " ")) {
			e.denyVerb, e.denyWhat = sub.denyVerb, sub.denyWhat
			return true
		}
		return false
	}
	if len(argv) == 0 {
		return false
	}
	if xargsFed {
		// xargs appends its stdin items: one more operand nobody can resolve.
		argv = append(argv, shVal{})
		rawArgs = append(rawArgs, "")
	}
	if ev.dispatch(argv, rawArgs) {
		return denied()
	}
	return false
}

// dispatch evaluates one stripped simple command.
func (e *binDirEval) dispatch(argv []shVal, rawArgs []string) bool {
	if !argv[0].ok {
		// An unresolved verb could be any write verb: deny when the command
		// derives from NIGHTGAUGE_BIN, allow otherwise.
		for _, a := range argv {
			if a.tainted {
				return e.deny("an unresolved command", "an argument derived from "+binDirEnvVar)
			}
		}
		if e.mentions {
			return e.deny("an unresolved command", "a command that names "+binDirEnvVar)
		}
		return false
	}
	verb := filepath.Base(argv[0].val)
	args := argv[1:]
	switch verb {
	case "export", "local", "readonly", "declare", "typeset":
		for _, w := range rawArgs[1:] {
			if assignRE.MatchString(w) {
				e.assign(w)
			}
		}
		return false
	case "cd", "pushd":
		e.cd(args)
		return false
	case "popd":
		e.cwdOK = false
		return false
	case "sed":
		if !sedInPlace(args) {
			return false
		}
		for _, a := range args {
			if a.ok && strings.HasPrefix(a.val, "-") {
				continue
			}
			if e.checkTarget(verb+" -i", a) {
				return true
			}
		}
		return false
	}
	if shellWrappers[verb] {
		for j := 0; j < len(args); j++ {
			a := args[j].val
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsRune(a, 'c') && j+1 < len(args) {
				s := args[j+1]
				if !s.ok {
					if s.tainted || e.mentions {
						return e.deny(verb+" -c", "an unresolved script")
					}
					return false
				}
				sub := e.clone()
				sub.depth++
				if sub.run(s.val) {
					e.denyVerb, e.denyWhat = sub.denyVerb, sub.denyWhat
					return true
				}
				return false
			}
		}
		return false
	}
	switch verb {
	case "eval", "source", ".":
		for _, a := range args {
			if a.tainted || !a.ok && e.mentions {
				return e.deny(verb, "a script derived from "+binDirEnvVar)
			}
		}
		if verb == "eval" {
			var parts []string
			for _, a := range args {
				if !a.ok {
					return false
				}
				parts = append(parts, a.val)
			}
			sub := e.clone()
			sub.depth++
			if sub.run(strings.Join(parts, " ")) {
				e.denyVerb, e.denyWhat = sub.denyVerb, sub.denyWhat
				return true
			}
		}
		return false
	}
	if !binDirWriteVerbSet[verb] {
		return false
	}
	for _, t := range writeTargets(verb, args) {
		if e.checkTarget(verb, t) {
			return true
		}
	}
	return false
}

func (e *binDirEval) clone() *binDirEval {
	c := *e
	c.vars = make(map[string]shVal, len(e.vars))
	for k, v := range e.vars {
		c.vars[k] = v
	}
	return &c
}

func (e *binDirEval) assign(w string) {
	eq := strings.IndexByte(w, '=')
	e.vars[w[:eq]] = e.expand(w[eq+1:])
}

func (e *binDirEval) cd(args []shVal) {
	var ops []shVal
	for _, a := range args {
		if a.ok && strings.HasPrefix(a.val, "-") && a.val != "-" {
			continue
		}
		ops = append(ops, a)
	}
	if len(ops) == 0 {
		if _, home := e.lookup("HOME"); home.ok {
			e.cwd, e.cwdOK, e.cwdTaint = home.val, true, home.tainted
		} else {
			e.cwdOK = false
		}
		return
	}
	if t := ops[0]; t.ok && t.val == "-" {
		e.cwdOK = false
		return
	}
	e.chdirLiteral(ops[0])
}

// chdirLiteral moves the cwd to t taken as a literal path (no `cd` option
// parsing, no `-`): what `env -C DIR` and `sudo -D DIR` do.
func (e *binDirEval) chdirLiteral(t shVal) {
	if !t.ok {
		e.cwdOK = false
		e.cwdTaint = e.cwdTaint || t.tainted
		return
	}
	p := t.val
	if !filepath.IsAbs(p) {
		if !e.cwdOK {
			e.cwdTaint = e.cwdTaint || t.tainted
			return
		}
		p = filepath.Join(e.cwd, p)
	}
	e.cwd, e.cwdOK, e.cwdTaint = filepath.Clean(p), true, e.cwdTaint || t.tainted
}

// sedInPlace reports whether sed's arguments edit files in place.
func sedInPlace(args []shVal) bool {
	for _, a := range args {
		if a.ok && (strings.HasPrefix(a.val, "-i") || strings.HasPrefix(a.val, "--in-place") ||
			(strings.HasPrefix(a.val, "-") && !strings.HasPrefix(a.val, "--") && strings.ContainsRune(a.val, 'i'))) {
			return true
		}
	}
	return false
}

// shellReserved are words that precede a simple command without being one:
// reserved words and grouping (`! cp`, `{ cp`, `then cp`, `do cp`).
var shellReserved = map[string]bool{
	"!": true, "{": true, "}": true, "if": true, "then": true, "else": true,
	"elif": true, "while": true, "until": true, "do": true, "fi": true, "done": true,
}

// wrapperOpt is what one prefix wrapper's options mean, so stripping can skip
// their values exactly and apply the ones that change the inner command.
// Sources: env(1), sudo(8), doas(1), timeout(1), chrt(1), nice(1), ionice(1),
// stdbuf(1), GNU time(1), xargs(1) (GNU and BSD), bash(1) exec. A short
// letter missing here is skipped as a flag, which is no worse than before
// #3650; long options resolve as getopt_long does (see long).
type wrapperOpt struct {
	valueShort    string   // letters taking a value: rest of the token, else the next word
	optionalShort string   // letters taking an optional value from the rest of the token only
	valueLong     []string // long options taking a value: `--x=V` or `--x V`
	optionalLong  []string // long options whose value is only `--x=V`
	flagLong      []string // long options taking no value
	// completeLong marks the long tables as complete, so a long option that
	// matches none of them is not a real option and fails closed.
	completeLong bool
	// leadingOperands are positional words before the command, even after
	// `--` (timeout DURATION, chrt PRIORITY).
	leadingOperands int
	assignments     bool // NAME=value words before the command (env, sudo)
	appendsStdin    bool // xargs: the command gains the items read from stdin
	// Roles, by short letter or long name: chdir moves the inner command's
	// cwd, split is `env -S` (its value is the command), replace is xargs's
	// replace string, output a file the wrapper writes.
	chdir, split, replace, output []string
}

const xargsDefaultReplace = "{}" // xargs -i / --replace without a value

// wrapperOpts has a row for every prefixWrappers name plus exec and builtin.
var wrapperOpts = map[string]wrapperOpt{
	"env": {
		valueShort:   "uCSaPLU",
		valueLong:    []string{"unset", "chdir", "split-string", "argv0"},
		optionalLong: []string{"block-signal", "default-signal", "ignore-signal"},
		flagLong: []string{"ignore-environment", "null", "debug", "list-signal-handling",
			"help", "version"},
		completeLong: true, assignments: true,
		chdir: []string{"C", "chdir"}, split: []string{"S", "split-string"},
	},
	"sudo": {
		valueShort: "aCcDgpRrTtUu", optionalShort: "h",
		valueLong: []string{"user", "group", "close-from", "chdir", "prompt", "chroot", "role",
			"type", "other-user", "command-timeout", "login-class", "auth-type", "host"},
		optionalLong: []string{"preserve-env"},
		flagLong: []string{"askpass", "background", "bell", "edit", "set-home", "help", "login",
			"remove-timestamp", "reset-timestamp", "list", "non-interactive",
			"preserve-groups", "stdin", "shell", "validate", "version"},
		completeLong: true, assignments: true, chdir: []string{"D", "chdir"},
	},
	"doas": {valueShort: "uC"},
	"timeout": {
		valueShort: "sk", valueLong: []string{"signal", "kill-after"},
		flagLong:     []string{"preserve-status", "foreground", "verbose", "help", "version"},
		completeLong: true, leadingOperands: 1,
	},
	"chrt": {
		valueShort: "TPD",
		valueLong:  []string{"sched-runtime", "sched-period", "sched-deadline"},
		flagLong: []string{"all-tasks", "batch", "deadline", "fifo", "idle", "other", "rr",
			"reset-on-fork", "max", "pid", "verbose", "help", "version"},
		completeLong: true, leadingOperands: 1,
	},
	"nice": {
		valueShort: "n", valueLong: []string{"adjustment"},
		flagLong: []string{"help", "version"}, completeLong: true,
	},
	"ionice": {
		valueShort: "cnpPu", valueLong: []string{"class", "classdata", "pid", "pgid", "uid"},
		flagLong: []string{"ignore", "help", "version"}, completeLong: true,
	},
	"stdbuf": {
		valueShort: "ioe", valueLong: []string{"input", "output", "error"},
		flagLong: []string{"help", "version"}, completeLong: true,
	},
	"time": {
		valueShort: "fo", valueLong: []string{"format", "output"},
		flagLong:     []string{"append", "portability", "verbose", "quiet", "help", "version"},
		completeLong: true, output: []string{"o", "output"},
	},
	"exec":    {valueShort: "a"},
	"command": {},
	"nohup":   {},
	"setsid":  {},
	"builtin": {},
	"xargs": {
		valueShort: "adEILnPsJRS", optionalShort: "iel",
		valueLong: []string{"arg-file", "delimiter", "max-args", "max-procs", "max-chars",
			"process-slot-var"},
		optionalLong: []string{"eof", "replace", "max-lines"},
		flagLong: []string{"interactive", "no-run-if-empty", "null", "open-tty", "show-limits",
			"verbose", "exit", "help", "version"},
		completeLong: true, appendsStdin: true, replace: []string{"I", "J", "i", "replace"},
	},
}

// longKind is how a long option takes its value.
type longKind int

const (
	longFlag longKind = iota
	longValue
	longOptional
	longUnknown // ambiguous, or unknown to a complete table: fail closed
)

// long resolves a long option name as getopt_long does: an exact name, else
// a unique prefix. An ambiguous prefix, or a name a complete table lacks, is
// longUnknown; with an incomplete table an unknown name is skipped as a flag.
func (o wrapperOpt) long(name string) (string, longKind) {
	kinds := []struct {
		names []string
		kind  longKind
	}{{o.valueLong, longValue}, {o.optionalLong, longOptional}, {o.flagLong, longFlag}}
	for _, k := range kinds {
		if slices.Contains(k.names, name) {
			return name, k.kind
		}
	}
	canon, kind, matches := "", longUnknown, 0
	for _, k := range kinds {
		for _, c := range k.names {
			if strings.HasPrefix(c, name) {
				canon, kind, matches = c, k.kind, matches+1
			}
		}
	}
	switch {
	case matches == 1:
		return canon, kind
	case matches == 0 && !o.completeLong:
		return name, longFlag
	}
	return name, longUnknown
}

// wrapOutput is a file a wrapper writes, after the first `after` chdirs.
type wrapOutput struct {
	verb  string
	val   shVal
	after int
}

// wrapInfo is what the stripped wrappers do to the inner command.
type wrapInfo struct {
	xargs      bool
	hasReplace bool
	replace    string
	chdirs     []shVal
	outputs    []wrapOutput
	split      *shVal   // env -S STRING: the command is STRING then splitRest
	splitRest  []string // raw words after the -S option
}

// role records what option key (a short letter or canonical long name) of
// wrapper name does with its value.
func (info *wrapInfo) role(name string, o wrapperOpt, key string, v shVal, have bool) {
	opt := "-" + key
	if len(key) > 1 {
		opt = "--" + key
	}
	switch {
	case slices.Contains(o.chdir, key) && have:
		info.chdirs = append(info.chdirs, v)
	case slices.Contains(o.output, key) && have:
		info.outputs = append(info.outputs,
			wrapOutput{verb: name + " " + opt, val: v, after: len(info.chdirs)})
	case slices.Contains(o.split, key) && have:
		info.split = &v
	case slices.Contains(o.replace, key):
		info.hasReplace = true
		info.replace = xargsDefaultReplace
		if have {
			info.replace = v.val
			if !v.ok {
				info.replace = "" // unknown: every word may contain it
			}
		}
	}
}

// unresolved returns a copy of vs with every value unresolved (taint kept).
func unresolved(vs []shVal) []shVal {
	out := make([]shVal, len(vs))
	for i, v := range vs {
		out[i] = shVal{tainted: v.tainted}
	}
	return out
}

// stripWrappers drops reserved words and prefix wrappers (sudo, env A=b,
// command, timeout N, …) so the real command is argv[0], skipping each
// wrapper's options with their values (wrapperOpts). It returns the stripped
// argv, the raw words and what the wrappers do to the command (#3650).
func (e *binDirEval) stripWrappers(argv []shVal, raw []string) ([]shVal, []string, wrapInfo) {
	var info wrapInfo
	for len(argv) > 0 && argv[0].ok {
		if shellReserved[argv[0].val] {
			argv, raw = argv[1:], raw[1:]
			continue
		}
		name := filepath.Base(argv[0].val)
		o, ok := wrapperOpts[name]
		if !ok {
			return argv, raw, info
		}
		j := 1
	opts:
		for j < len(argv) && argv[j].ok {
			a := argv[j]
			v := a.val
			switch {
			case v == "--":
				j++
				break opts
			case o.assignments && assignRE.MatchString(v):
				j++
				continue
			case !strings.HasPrefix(v, "-"):
				break opts
			case v == "-":
				j++
				continue
			}
			j++
			var key string
			var val shVal
			have := false
			if strings.HasPrefix(v, "--") {
				nm, inTok, hasEq := strings.Cut(v[2:], "=")
				canon, kind := o.long(nm)
				switch kind {
				case longUnknown:
					return unresolved(argv[j-1:]), raw[j-1:], info
				case longValue:
					if hasEq {
						val, have = shVal{val: inTok, ok: true, tainted: a.tainted}, true
					} else if j < len(argv) {
						val, have = argv[j], true
						j++
					}
				case longOptional:
					if hasEq {
						val, have = shVal{val: inTok, ok: true, tainted: a.tainted}, true
					}
				}
				key = canon
			} else {
				letter, inTok, needNext, _ := scanShortCluster(v, o.valueShort+o.optionalShort)
				if letter == 0 {
					continue
				}
				switch {
				case strings.IndexByte(o.optionalShort, letter) >= 0:
					if inTok != "" {
						val, have = shVal{val: inTok, ok: true, tainted: a.tainted}, true
					}
				case !needNext:
					val, have = shVal{val: inTok, ok: true, tainted: a.tainted}, true
				case j < len(argv):
					val, have = argv[j], true
					j++
				}
				key = string(letter)
			}
			info.role(name, o, key, val, have)
			if info.split != nil {
				info.splitRest = raw[j:]
				return argv[j:], raw[j:], info
			}
		}
		j = min(j+o.leadingOperands, len(argv))
		argv, raw = argv[j:], raw[j:]
		if o.appendsStdin {
			info.xargs = true
			if info.hasReplace {
				marked := slices.Clone(argv)
				for k, w := range marked {
					if !w.ok || strings.Contains(w.val, info.replace) {
						marked[k] = shVal{tainted: w.tainted}
					}
				}
				argv = marked
			}
		}
	}
	return argv, raw, info
}

// Short options that take a value, per `curl --help all` and `wget --help`.
// A letter after one of these in a cluster belongs to its value, never to the
// option set, so `curl -do x` is `-d o` and writes nothing (#3649).
const (
	curlValueLetters = "AbcCdDeEFHKmoPQrtTuUwxXYyz"
	wgetValueLetters = "aABDeiIlnoOPQRtTUwX"

	curlTargetLetters    = "o"  // -o <file>
	curlRemoteNameLetter = 'O'  // -O writes into the cwd
	wgetTargetLetters    = "OP" // -O <file>, -P <dir>
)

// scanShortCluster parses a single-dash option cluster such as `-sSLo<path>`
// with getopt semantics. flags holds the letters before the first
// value-taking letter; letter is that letter (0 when none), val the rest of
// the token, and needNext reports that the value is the next argument.
func scanShortCluster(v, valueLetters string) (letter byte, val string, needNext bool, flags string) {
	for i := 1; i < len(v); i++ {
		if strings.IndexByte(valueLetters, v[i]) >= 0 {
			return v[i], v[i+1:], i+1 == len(v), v[1:i]
		}
	}
	return 0, "", false, v[1:]
}

// clusterValue records a scanned cluster's value: a target letter's value is
// a write target (in-token, or the next argument via takeNext); any other
// value-taking letter's next-argument value is skipped (skipNext).
func clusterValue(letter byte, val string, needNext bool, targetLetters string, tainted bool, out []shVal) (_ []shVal, takeNext, skipNext bool) {
	if letter == 0 {
		return out, false, false
	}
	if strings.IndexByte(targetLetters, letter) >= 0 {
		if needNext {
			return out, true, false
		}
		return append(out, shVal{val: val, ok: true, tainted: tainted}), false, false
	}
	return out, false, needNext
}

// writeTargets returns the operands verb writes to.
func writeTargets(verb string, args []shVal) []shVal {
	var ops, out []shVal
	opts := true
	takeNext, skipNext := false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if skipNext {
			skipNext = false
			continue
		}
		if takeNext {
			out = append(out, a)
			takeNext = false
			continue
		}
		v := a.val
		if opts && a.ok && v == "--" {
			opts = false
			continue
		}
		if opts && a.ok && strings.HasPrefix(v, "-") && v != "-" {
			switch verb {
			case "cp", "mv", "ln", "install": // rsync -t is --times
				if v == "-t" || v == "--target-directory" {
					takeNext = true
				} else if strings.HasPrefix(v, "--target-directory=") {
					out = append(out, shVal{val: strings.TrimPrefix(v, "--target-directory="), ok: true, tainted: a.tainted})
				} else if strings.HasPrefix(v, "-t") && !strings.HasPrefix(v, "--") {
					out = append(out, shVal{val: v[2:], ok: true, tainted: a.tainted})
				}
			case "curl":
				switch {
				case v == "--output" || v == "--output-dir":
					takeNext = true
				case strings.HasPrefix(v, "--output="):
					out = append(out, shVal{val: strings.TrimPrefix(v, "--output="), ok: true, tainted: a.tainted})
				case v == "--remote-name" || v == "--remote-name-all":
					out = append(out, shVal{val: ".", ok: true})
				case !strings.HasPrefix(v, "--"):
					letter, val, needNext, flags := scanShortCluster(v, curlValueLetters)
					if strings.ContainsRune(flags, curlRemoteNameLetter) {
						out = append(out, shVal{val: ".", ok: true})
					}
					out, takeNext, skipNext = clusterValue(letter, val, needNext, curlTargetLetters, a.tainted, out)
				}
			case "wget":
				switch {
				case v == "--output-document" || v == "--directory-prefix":
					takeNext = true
				case strings.HasPrefix(v, "--output-document="):
					out = append(out, shVal{val: strings.TrimPrefix(v, "--output-document="), ok: true, tainted: a.tainted})
				case strings.HasPrefix(v, "--directory-prefix="):
					out = append(out, shVal{val: strings.TrimPrefix(v, "--directory-prefix="), ok: true, tainted: a.tainted})
				case !strings.HasPrefix(v, "--"):
					letter, val, needNext, _ := scanShortCluster(v, wgetValueLetters)
					out, takeNext, skipNext = clusterValue(letter, val, needNext, wgetTargetLetters, a.tainted, out)
				}
			}
			continue
		}
		ops = append(ops, a)
	}
	switch verb {
	case "cp", "ln", "install", "rsync":
		if len(ops) >= 2 {
			out = append(out, ops[len(ops)-1])
		} else if verb == "ln" && len(ops) == 1 {
			out = append(out, shVal{val: ".", ok: true})
		}
	case "dd":
		for _, o := range ops {
			if strings.HasPrefix(o.val, "of=") {
				out = append(out, shVal{val: strings.TrimPrefix(o.val, "of="), ok: o.ok, tainted: o.tainted})
			} else if !o.ok && (o.tainted || strings.Contains(o.val, "of=")) {
				out = append(out, o)
			}
		}
	case "curl":
		// operands are URLs
	case "wget":
		if len(out) == 0 {
			out = append(out, shVal{val: ".", ok: true})
		}
	default: // mv, rm, unlink, truncate, chmod, tee
		out = append(out, ops...)
	}
	filtered := out[:0]
	for _, t := range out {
		if t.ok && (t.val == "-" || t.val == "/dev/null") {
			continue
		}
		filtered = append(filtered, t)
	}
	return filtered
}

// checkTarget decides one write target; true denies.
func (e *binDirEval) checkTarget(verb string, t shVal) bool {
	if !t.ok {
		if t.tainted || e.mentions {
			return e.deny(verb, "an unresolved target derived from "+binDirEnvVar)
		}
		return false
	}
	p := t.val
	if !filepath.IsAbs(p) {
		if !e.cwdOK {
			if t.tainted || e.cwdTaint || e.mentions {
				return e.deny(verb, p+" (relative to an unresolved directory)")
			}
			return false
		}
		p = filepath.Join(e.cwd, p)
	}
	if e.inside(p) {
		return e.deny(verb, filepath.Clean(p))
	}
	return false
}

func (e *binDirEval) inside(p string) bool {
	cands := []string{filepath.Clean(p), resolveExisting(p)}
	for _, c := range cands {
		for _, f := range e.forms {
			if c == f || strings.HasPrefix(c, f+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

// resolveExisting resolves the symlinks of p's longest existing ancestor and
// re-joins the rest.
func resolveExisting(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func (e *binDirEval) lookup(name string) (string, shVal) {
	if v, ok := e.vars[name]; ok {
		return v.val, v
	}
	if v, ok := os.LookupEnv(name); ok {
		return v, shVal{val: v, ok: true}
	}
	return "", shVal{}
}

// expand evaluates one raw word.
func (e *binDirEval) expand(raw string) shVal {
	res := shVal{ok: true}
	var b strings.Builder
	add := func(v shVal) {
		b.WriteString(v.val)
		res.ok = res.ok && v.ok
		res.tainted = res.tainted || v.tainted
	}
	for i := 0; i < len(raw); {
		c := raw[i]
		switch {
		case c == '\'':
			j := strings.IndexByte(raw[i+1:], '\'')
			if j < 0 {
				b.WriteString(raw[i+1:])
				i = len(raw)
				continue
			}
			b.WriteString(raw[i+1 : i+1+j])
			i += j + 2
		case c == '"':
			i++
			for i < len(raw) && raw[i] != '"' {
				if raw[i] == '\\' && i+1 < len(raw) && strings.IndexByte("$`\"\\\n", raw[i+1]) >= 0 {
					b.WriteByte(raw[i+1])
					i += 2
					continue
				}
				if raw[i] == '$' || raw[i] == '`' {
					v, n := e.dollar(raw[i:])
					add(v)
					i += n
					continue
				}
				b.WriteByte(raw[i])
				i++
			}
			i++
		case c == '\\':
			if i+1 < len(raw) {
				b.WriteByte(raw[i+1])
			}
			i += 2
		case c == '$' || c == '`':
			v, n := e.dollar(raw[i:])
			add(v)
			i += n
		case c == '*' || c == '?' || c == '[':
			res.ok = false
			b.WriteByte(c)
			i++
		case c == '~' && i == 0:
			j := 1
			for j < len(raw) && raw[j] != '/' {
				j++
			}
			if j == 1 {
				_, h := e.lookup("HOME")
				add(h)
			} else {
				res.ok = false
			}
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	res.val = b.String()
	return res
}

var varNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)

// dollar expands the `$…` or backtick form at the start of s, returning the
// value and the bytes consumed.
func (e *binDirEval) dollar(s string) (shVal, int) {
	if s[0] == '`' {
		j := strings.IndexByte(s[1:], '`')
		if j < 0 {
			return e.subst(s[1:]), len(s)
		}
		return e.subst(s[1 : 1+j]), j + 2
	}
	if len(s) == 1 {
		return shVal{val: "$", ok: true}, 1
	}
	switch s[1] {
	case '(':
		end := matchParen(s, 1)
		if strings.HasPrefix(s, "$((") || end < 3 || s[end-1] != ')' {
			return shVal{ok: false, tainted: strings.Contains(s[:end], binDirEnvVar)}, end
		}
		return e.subst(s[2 : end-1]), end
	case '{':
		j := strings.IndexByte(s, '}')
		if j < 0 {
			return shVal{tainted: strings.Contains(s, binDirEnvVar)}, len(s)
		}
		body := s[2:j]
		name := varNameRE.FindString(body)
		rest := body[len(name):]
		_, v := e.lookup(name)
		switch {
		case name == "":
			return shVal{tainted: strings.Contains(body, binDirEnvVar)}, j + 1
		case rest == "":
			return v, j + 1
		case strings.HasPrefix(rest, ":-") || strings.HasPrefix(rest, "-"):
			if v.ok && v.val != "" {
				return v, j + 1
			}
			return e.expand(strings.TrimLeft(strings.TrimPrefix(rest, ":"), "-")), j + 1
		default:
			return shVal{tainted: v.tainted || strings.Contains(rest, binDirEnvVar)}, j + 1
		}
	}
	name := varNameRE.FindString(s[1:])
	if name == "" {
		return shVal{}, 2 // special parameter ($1, $?, $@ …)
	}
	_, v := e.lookup(name)
	return v, 1 + len(name)
}

// matchParen returns the index just past the `)` closing the `(` at s[open].
func matchParen(s string, open int) int {
	depth := 0
	var q byte
	for i := open; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			} else if c == '\\' && q == '"' {
				i++
			}
		case c == '\'' || c == '"':
			q = c
		case c == '\\':
			i++
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(s)
}

// subst statically evaluates a command substitution: dirname, basename, pwd,
// realpath and readlink -f; anything else is unresolved.
func (e *binDirEval) subst(inner string) shVal {
	taint := strings.Contains(inner, binDirEnvVar)
	stmts := lexShell(inner)
	if len(stmts) != 1 || len(stmts[0].redirs) > 0 || len(stmts[0].words) == 0 {
		return shVal{tainted: taint}
	}
	var argv []shVal
	for _, w := range stmts[0].words {
		v := e.expand(w)
		taint = taint || v.tainted
		argv = append(argv, v)
	}
	if !argv[0].ok {
		return shVal{tainted: taint}
	}
	var ops []shVal
	for _, a := range argv[1:] {
		if a.ok && strings.HasPrefix(a.val, "-") {
			if filepath.Base(argv[0].val) == "readlink" && a.val != "-f" && a.val != "-e" && a.val != "-m" {
				return shVal{tainted: taint}
			}
			continue
		}
		if !a.ok {
			return shVal{tainted: taint}
		}
		ops = append(ops, a)
	}
	cmd := filepath.Base(argv[0].val)
	if cmd == "pwd" {
		return shVal{val: e.cwd, ok: e.cwdOK, tainted: taint || e.cwdTaint}
	}
	if len(ops) != 1 {
		return shVal{tainted: taint}
	}
	p := ops[0].val
	switch cmd {
	case "dirname":
		return shVal{val: filepath.Dir(p), ok: true, tainted: taint}
	case "basename":
		return shVal{val: filepath.Base(p), ok: true, tainted: taint}
	case "realpath", "readlink":
		if cmd == "readlink" && len(argv)-1 == len(ops) {
			return shVal{tainted: taint} // plain readlink reads a link's text
		}
		if !filepath.IsAbs(p) {
			if !e.cwdOK {
				return shVal{tainted: taint}
			}
			p = filepath.Join(e.cwd, p)
		}
		return shVal{val: resolveExisting(p), ok: true, tainted: taint}
	}
	return shVal{tainted: taint}
}

// lexShell splits a script into simple commands at top-level `;`, `&`, `|`,
// `&&`, `||`, newlines and subshell parentheses, without breaking inside
// quotes, `$( … )` or backticks. Output redirection targets are collected per
// command; input redirections are dropped.
func lexShell(s string) []shStmt {
	var out []shStmt
	var cur shStmt
	var w strings.Builder
	inWord := false
	redirNext := 0 // 1 = next word is an output target, 2 = input (dropped)
	endWord := func() {
		if !inWord {
			return
		}
		word := w.String()
		w.Reset()
		inWord = false
		switch redirNext {
		case 1:
			cur.redirs = append(cur.redirs, word)
		case 2:
		default:
			cur.words = append(cur.words, word)
		}
		redirNext = 0
	}
	endStmt := func() {
		endWord()
		if len(cur.words) > 0 || len(cur.redirs) > 0 {
			out = append(out, cur)
		}
		cur = shStmt{}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(s) && s[j] != c {
				if c == '"' && s[j] == '\\' {
					j++
				} else if c == '"' && s[j] == '$' && j+1 < len(s) && s[j+1] == '(' {
					j = matchParen(s, j+1) - 1
				}
				j++
			}
			if j >= len(s) {
				j = len(s) - 1
			}
			w.WriteString(s[i : j+1])
			inWord = true
			i = j
		case c == '\\':
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
				continue
			}
			w.WriteByte(c)
			if i+1 < len(s) {
				w.WriteByte(s[i+1])
				i++
			}
			inWord = true
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			end := matchParen(s, i+1)
			w.WriteString(s[i:end])
			inWord = true
			i = end - 1
		case c == '`':
			j := strings.IndexByte(s[i+1:], '`')
			end := len(s)
			if j >= 0 {
				end = i + 2 + j
			}
			w.WriteString(s[i:end])
			inWord = true
			i = end - 1
		case c == '#' && !inWord:
			for i < len(s) && s[i] != '\n' {
				i++
			}
			endStmt()
		case c == '>' || c == '<':
			// A pending all-digit word (2>) or & (&>) is an fd prefix.
			if inWord {
				pre := w.String()
				if strings.Trim(pre, "0123456789") == "" {
					w.Reset()
					inWord = false
				} else {
					endWord()
				}
			}
			kind := 1
			if c == '<' {
				kind = 2
			}
			for i+1 < len(s) && (s[i+1] == '>' || s[i+1] == '|' || s[i+1] == '<') {
				i++
			}
			if i+1 < len(s) && s[i+1] == '&' {
				i++ // >&2 duplicates an fd; a word target after >& still counts
				j := i + 1
				for j < len(s) && s[j] >= '0' && s[j] <= '9' {
					j++
				}
				if j > i+1 || (j < len(s) && s[j] == '-') {
					i = j
					if j < len(s) && s[j] == '-' {
						i++
					}
					i--
					continue
				}
			}
			redirNext = kind
		case c == '&' && i+1 < len(s) && s[i+1] == '>':
			endWord()
			// &> target
		case c == ';' || c == '&' || c == '|' || c == '\n' || c == '(' || c == ')':
			endStmt()
		case c == ' ' || c == '\t' || c == '\r':
			endWord()
		default:
			w.WriteByte(c)
			inWord = true
		}
	}
	endStmt()
	return out
}
