#!/usr/bin/env python3
"""Forbid piping a command into a reader that exits early (#2360).

WHY

`grep -q` stops reading at its first match, and `head` after its last line.
Whatever is still writing into the pipe then dies of SIGPIPE (or gets EPIPE),
and under `set -o pipefail` that death becomes the pipeline's status:

    $ bash -o pipefail -c 'seq 200000 | grep -q "^1$"; echo $?'
    141

So a match reads as a miss, and with `set -e` a `x="$(cmd | head -1)"` ends
the script. With a small input it is a race that only sometimes ends with the
writer still writing, which is why it surfaced as gate failures under load
with matching content (state-backstop and check-changelog, #2360 and #2362).
With a large input it fails every time.

The fix is to give the reader no concurrent writer:

    grep -q PAT <<<"$var"            a here-string: bash writes it first
    [[ $var == *text* ]]             no process at all
    grep -q PAT "$file"              grep reads the file itself
    out="$(cmd)"; grep -q PAT <<<"$out"
    first="${out%%$'\\n'*}"           or `cmd | sed -n 1p`, which drains

SCOPE

Every tracked shell script (`*.sh`, `*.bash`, a shell shebang, or a Git hook
under `.husky/`, which has none) and every shell `run:` block in
`.github/workflows/`. It applies whether or not the file sets pipefail
itself: a sourced library runs under its caller's options, a step with
`shell: bash` gets pipefail implicitly, and a script that does not set it
today is one line away from it. The replacement forms cost nothing.

Fenced shell in Markdown (skill bodies and their includes) is out of scope. A
fence is an instruction an agent runs in its own tool's shell, often with
placeholders filled in by hand, so it is not code this repository runs under
known options, and many fences are deliberately partial. A fence a test lifts
out and runs, as issue-create's scope gates are run under pipefail, is
written in the replacement forms by hand.

Early-exit readers, as the first command of the stage after a pipe, looking
inside a `{ }` or `( )` group or an `if` condition, and past wrappers such as
env, nice, nohup, stdbuf, sudo and timeout: grep (egrep, fgrep, ggrep) with
-q, -m, -l or -L or their long forms, or with its output sent to /dev/null,
which GNU grep treats as -q (so trading -q for that redirect is no fix); head
(ghead) unless it prints all but the last lines (`-n -N`); sed (gsed) with a q
or Q command; awk with an exit or nextfile outside END, or with only BEGIN
rules, which read no input; perl -n or -p with exit or last; read; and a
while or until loop whose body can break, exit or return. An awk, sed or perl
program holding an expansion is read with the expansion as an opaque word.
A pipeline that ends an input process substitution `<(...)` is exempt: no
one reads its status.

Not seen: a command named by an expansion (`$GREP -q`), jq, a loop condition
that can turn false before the end of input, and code in a quoted string
handed to another shell.

WHY THIS IS NOT A GREP

The shape has to be found in shell code, not in text that mentions it: a
quoted help string, a comment, a heredoc body, a `case` pattern list such as
`yes | no)`, a regex alternation inside `[[ ]]` and arithmetic all hold a `|`
that is no pipe, and in arithmetic `<<` is a shift, not a heredoc. Code in
`$(...)` or backticks is code even within double quotes, and a pipeline
continues across a line ending in `|` or a backslash. So this file carries a
small shell lexer. Code in a quoted string handed to another shell
(`sh -c '...'`) is out of its reach; it does not run under this file's
options anyway.

Exit 0 clean, 1 on a violation, 2 if the gate cannot run: a file it cannot
lex, or a path that does not exist, is a failure, never a silent pass. A
heredoc whose delimiter never comes, which bash reads to the end of the file,
and a `[[` with no `]]` are files it cannot lex.

Run: python3 scripts/check-pipefail-early-exit.py [--list-files] [path ...]
With no path it reads the repository's tracked files. Self-test:
scripts/test-pipefail-early-exit.sh. Also run by .github/workflows/lint.yml
and scripts/ci-local.sh.
"""

from __future__ import annotations

import re
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
SHELL_SUFFIXES = (".sh", ".bash")
SHEBANG = re.compile(rb"^#!\s*\S*(?:/|\s)(?:env\s+)?(?:ba|da|k|z)?sh\b")
WORKFLOW_DIR = ".github/workflows/"
YAML_SUFFIXES = (".yml", ".yaml")

GREP_COMMANDS = {"grep", "egrep", "fgrep", "ggrep"}
AWK_COMMANDS = {"awk", "gawk", "mawk", "nawk"}
HEAD_COMMANDS = {"head", "ghead"}
SED_COMMANDS = {"sed", "gsed"}
# Short grep options that take an argument: the rest of the cluster, or the
# next word, is that argument rather than more options.
GREP_ARG_OPTS = set("efmABCdD")
GREP_EARLY_SHORT = set("qmlL")
GREP_EARLY_LONG = (
    "--quiet",
    "--silent",
    "--max-count",
    "--files-with-matches",
    "--files-without-match",
)
# GNU grep's long options, to resolve an abbreviation such as `--quie` the
# way getopt does: a prefix of exactly one of them.
GREP_LONG = GREP_EARLY_LONG + (
    "--after-context", "--basic-regexp", "--before-context", "--binary", "--binary-files",
    "--byte-offset", "--color", "--colour", "--context", "--count", "--dereference-recursive",
    "--devices", "--directories", "--exclude", "--exclude-dir", "--exclude-from",
    "--extended-regexp", "--file", "--fixed-strings", "--group-separator", "--help",
    "--ignore-case", "--include", "--initial-tab", "--invert-match", "--label",
    "--line-buffered", "--line-number", "--line-regexp", "--no-filename",
    "--no-group-separator", "--no-ignore-case", "--no-messages", "--null", "--null-data",
    "--only-matching", "--perl-regexp", "--recursive", "--regexp", "--text", "--version",
    "--with-filename", "--word-regexp",
)
# Words a pipeline stage can start with before the command it runs.
PREFIX_WORDS = {"!"}
# Commands that run another command, and the options of each that take an
# argument in the next word. timeout also takes a duration before the command.
WRAPPERS = {
    "builtin": set(),
    "command": set(),
    "exec": {"-a"},
    "nohup": set(),
    "time": set(),
    "env": {"-u", "--unset", "-C", "--chdir", "-S", "--split-string"},
    "nice": {"-n", "--adjustment"},
    "stdbuf": {"-i", "-o", "-e", "--input", "--output", "--error"},
    "sudo": {"-u", "-g", "-h", "-p", "-C", "-D", "-r", "-t", "-U", "-T", "-R"},
    "timeout": {"-s", "--signal", "-k", "--kill-after"},
    "gtimeout": {"-s", "--signal", "-k", "--kill-after"},
}
TAKES_DURATION = {"timeout", "gtimeout"}
LOOP_WORDS = {"while", "until"}
# Inside a loop body, these end the loop before its input does.
LOOP_EXITS = {"break", "exit", "return"}

PIPE_OPS = {"|", "|&"}
CASE_BREAKS = {";;", ";&", ";;&"}
# Operators that end a simple command.
COMMAND_END = {"|", "|&", "||", "&&", ";", "&", "nl", "(", ")", "$(", ")$"} | CASE_BREAKS
REDIRECTIONS = {">", ">>", "<", "<<<", ">&", "<&", "&>", "&>>", "<>", ">|", "<<", "<<-"}
ASSIGNMENT = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*(\[[^]]*\])?\+?=")
# After one of these words the next word starts a command.
COMMAND_KEYWORDS = {"then", "do", "else", "elif", "if", "while", "until", "!", "time", "{"}
# After one of these operators the next word starts a command.
COMMAND_START_OPS = {"|", "|&", "||", "&&", ";", "&", "nl", "(", "$("} | CASE_BREAKS


class LexError(Exception):
    pass


# Stands in for an expansion in a word's shape.
OPAQUE = "\x00"


@dataclass
class Tok:
    kind: str  # "word" or "op"
    text: str  # a word's source text, or the operator
    value: str | None  # a word's literal value; None when it holds an expansion
    line: int
    glued: bool = False  # a word that runs straight into `<` or `>`: `2>x`
    # The literal value with each expansion replaced by OPAQUE, so a program
    # such as awk "/$pat/ { exit }" can still be read.
    shape: str = ""


@dataclass
class Finding:
    line: int
    reader: str
    why: str


def starts_command(prev: Tok | None) -> bool:
    if prev is None:
        return True
    if prev.kind == "op":
        return prev.text in COMMAND_START_OPS
    return prev.value in COMMAND_KEYWORDS


@dataclass
class Lexer:
    src: str
    line: int = 1
    i: int = 0
    toks: list[Tok] = field(default_factory=list)
    heredocs: list[tuple[str, bool]] = field(default_factory=list)

    def peek(self, k: int = 0) -> str:
        j = self.i + k
        return self.src[j] if j < len(self.src) else ""

    def startswith(self, s: str) -> bool:
        return self.src.startswith(s, self.i)

    def advance(self, n: int = 1) -> str:
        chunk = self.src[self.i : self.i + n]
        self.line += chunk.count("\n")
        self.i += n
        return chunk

    def lex(self) -> list[Tok]:
        self.code(until_paren=False)
        if self.heredocs:
            raise LexError(f"line {self.line}: a heredoc with no body before the end of the file")
        return self.toks

    # ---- code context ---------------------------------------------------
    OPERATORS = (";;&", "&>>", "<<<", "<<-", "||", "|&", "&&", ";;", ";&", ">>", ">&",
                 "<&", "&>", "<>", ">|", "<<", "|", "&", ";", "(", ")", "<", ">")

    def code(self, until_paren: bool) -> None:
        """Lex shell code up to EOF, or up to the `)` closing a `$(`."""
        depth = 0
        case: list[str] = []  # per open `case`: "word", "pattern" or "body"
        prev: Tok | None = None
        while self.i < len(self.src):
            c = self.peek()
            if self.startswith("\\\n"):
                self.advance(2)
                continue
            if c == "\n":
                prev = self.emit_op("nl")
                self.advance()
                self.heredoc_bodies()
                continue
            if c in " \t\r":
                self.advance()
                continue
            if c == "#":
                while self.i < len(self.src) and self.peek() != "\n":
                    self.advance()
                continue
            in_pattern = bool(case) and case[-1] == "pattern"
            if c == ")" and until_paren and depth == 0 and not in_pattern:
                self.advance()
                return
            if c in "<>" and self.peek(1) == "(":
                prev = self.word() or prev  # process substitution <(...) / >(...)
                continue
            if self.startswith("((") and not in_pattern and (
                starts_command(prev) or (prev is not None and prev.value == "for")
            ):
                prev = self.arithmetic()  # (( ... )): `<<` and `|` in it are operators on numbers
                continue
            op = next((o for o in self.OPERATORS if self.startswith(o)), None)
            if op is not None:
                self.advance(len(op))
                prev = self.emit_op(op)
                if op == "(" and not in_pattern:
                    depth += 1
                elif op == ")":
                    if in_pattern:
                        case[-1] = "body"
                        prev = Tok("op", "nl", None, self.line)
                    else:
                        depth -= 1
                elif op in CASE_BREAKS and case:
                    case[-1] = "pattern"
                elif op in ("<<", "<<-"):
                    self.heredoc_delimiter(strip_tabs=op == "<<-")
                continue
            tok = self.word()
            if tok is None:
                raise LexError(f"line {self.line}: cannot read {self.src[self.i:self.i + 20]!r}")
            if tok.value == "case" and starts_command(prev):
                case.append("word")
            elif tok.value == "in" and case and case[-1] == "word":
                case[-1] = "pattern"
            elif tok.value == "esac" and case and (case[-1] == "pattern" or starts_command(prev)):
                case.pop()
            prev = tok
        if until_paren:
            raise LexError(f"line {self.line}: unterminated $( or <(")

    def emit_op(self, op: str) -> Tok:
        tok = Tok("op", op, None, self.line)
        self.toks.append(tok)
        return tok

    def arithmetic(self) -> Tok:
        """An arithmetic command, `(( ... ))` or `for (( ... ))`, as one opaque word."""
        line = self.line
        start = self.i
        self.advance(2)
        self.skip_balanced("(", ")", depth=2)
        tok = Tok("word", self.src[start : self.i], None, line, shape=OPAQUE)
        self.toks.append(tok)
        return tok

    def heredoc_delimiter(self, strip_tabs: bool) -> None:
        while self.peek() in (" ", "\t"):
            self.advance()
        tok = self.word()
        if tok is None:
            raise LexError(f"line {self.line}: a heredoc operator without a delimiter")
        self.heredocs.append((re.sub(r"[\"'\\]", "", tok.text), strip_tabs))

    def heredoc_bodies(self) -> None:
        """Skip the bodies of the heredocs opened on the line just ended.

        A delimiter that never comes is a LexError, not the end of the scan:
        bash reads such a heredoc to the end of the file, and so would this
        gate, passing whatever followed unread. It is also how a `<<` this
        lexer misread as a heredoc would show up.
        """
        pending, self.heredocs = self.heredocs, []
        for delim, strip_tabs in pending:
            line = self.line
            while True:
                if self.i >= len(self.src):
                    raise LexError(f"line {line}: a heredoc never closed by {delim!r}")
                end = self.src.find("\n", self.i)
                end = len(self.src) if end < 0 else end
                text = self.src[self.i : end]
                self.advance(end - self.i + (1 if end < len(self.src) else 0))
                if (text.lstrip("\t") if strip_tabs else text) == delim:
                    break

    # ---- words ------------------------------------------------------------
    def word(self) -> Tok | None:
        line = self.line
        start = self.i
        literal: list[str] = []
        is_literal = True
        last_plain = ""  # the last character read outside any quoting
        while self.i < len(self.src):
            c = self.peek()
            if c == "(" and last_plain in ("@", "!", "+", "*", "?"):
                self.extglob(literal)  # @(a|b): a pattern, not a subshell
                last_plain = ""
                continue
            if c in " \t\r\n;&|()":
                break
            if c in "<>":
                if self.peek(1) != "(":
                    break
                self.advance(2)
                # Only an input substitution's status is never read; output
                # from a pipeline in `>(...)` is still what its writer feeds.
                self.substitution("<(" if c == "<" else "$(")
                literal.append(OPAQUE)
                is_literal = False
                last_plain = ""
                continue
            last_plain = ""
            if c == "\\":
                if self.peek(1) == "\n":
                    self.advance(2)
                    continue
                literal.append(self.peek(1))
                self.advance(2)
                continue
            if c == "'":
                self.advance()
                end = self.src.find("'", self.i)
                if end < 0:
                    raise LexError(f"line {line}: unterminated single quote")
                literal.append(self.src[self.i : end])
                self.advance(end - self.i + 1)
                continue
            if c == "$" and self.peek(1) == "'":
                self.advance(2)
                literal.append(self.ansi_c())
                is_literal = False
                continue
            if c == '"':
                self.advance()
                is_literal = self.double_quoted(literal) and is_literal
                continue
            if c in ("$", "`"):
                self.dollar_or_backtick()
                literal.append(OPAQUE)
                is_literal = False
                continue
            literal.append(c)
            last_plain = c
            self.advance()
        if self.i == start:
            return None
        shape = "".join(literal)
        value = shape if is_literal else None
        tok = Tok("word", self.src[start : self.i], value, line, shape=shape)
        tok.glued = self.peek() in ("<", ">") and self.peek(1) != "("
        self.toks.append(tok)
        return tok

    def extglob(self, literal: list[str]) -> None:
        line = self.line
        depth = 0
        while self.i < len(self.src):
            c = self.advance()
            literal.append(c)
            if c == "\\":
                literal.append(self.advance())
            elif c == "(":
                depth += 1
            elif c == ")":
                depth -= 1
                if depth == 0:
                    return
        raise LexError(f"line {line}: unterminated extended glob")

    def ansi_c(self) -> str:
        """Skip a $'...' string; its text, escapes left as written."""
        line = self.line
        start = self.i
        while self.i < len(self.src):
            c = self.advance()
            if c == "\\":
                self.advance()
            elif c == "'":
                return self.src[start : self.i - 1]
        raise LexError(f"line {line}: unterminated $'...'")

    def double_quoted(self, literal: list[str]) -> bool:
        is_literal = True
        line = self.line
        while self.i < len(self.src):
            c = self.peek()
            if c == '"':
                self.advance()
                return is_literal
            if c == "\\":
                nxt = self.peek(1)
                if nxt == "\n":
                    self.advance(2)
                    continue
                literal.append(nxt if nxt in '$`"\\' else "\\" + nxt)
                self.advance(2)
                continue
            if c in ("$", "`"):
                self.dollar_or_backtick()
                literal.append(OPAQUE)
                is_literal = False
                continue
            literal.append(c)
            self.advance()
        raise LexError(f"line {line}: unterminated double quote")

    def dollar_or_backtick(self) -> None:
        if self.startswith("$(("):
            self.advance(3)
            self.skip_balanced("(", ")", depth=2)
        elif self.startswith("$("):
            self.advance(2)
            self.substitution()
        elif self.startswith("${"):
            self.advance(2)
            self.skip_balanced("{", "}", depth=1)
        elif self.startswith("$["):  # the old arithmetic form, `$[x << 1]`
            self.advance(2)
            self.skip_balanced("[", "]", depth=1)
        elif self.startswith("`"):
            # Old-style command substitution: code too, once its own escapes
            # (\\, \` and \$) are undone.
            line = self.line
            self.advance()
            body: list[str] = []
            while self.i < len(self.src) and self.peek() != "`":
                if self.peek() == "\\" and self.peek(1) in "$`\\":
                    body.append(self.peek(1))
                    self.advance(2)
                else:
                    body.append(self.advance(2 if self.peek() == "\\" else 1))
            if self.i >= len(self.src):
                raise LexError(f"line {line}: unterminated backtick")
            self.advance()
            inner = Lexer("".join(body), line)
            inner.lex()
            self.emit_op("$(")
            self.toks.extend(inner.toks)
            self.emit_op(")$")
        else:
            self.advance()  # $name, $1, $?, or a lone $

    def substitution(self, opener: str = "$(") -> None:
        """$(...) or >(...) as "$(", <(...) as "<(", each closed by ")$"."""
        self.emit_op(opener)
        self.code(until_paren=True)
        self.emit_op(")$")

    def skip_balanced(self, open_: str, close: str, depth: int) -> None:
        """Skip ${...} or $((...)) as data, lexing any $( inside it as code."""
        line = self.line
        while self.i < len(self.src):
            c = self.peek()
            if c == "\\":
                self.advance(2)
                continue
            if c == "'" and open_ == "{":
                end = self.src.find("'", self.i + 1)
                if end < 0:
                    raise LexError(f"line {line}: unterminated single quote in ${{...}}")
                self.advance(end - self.i + 1)
                continue
            if c == '"':
                self.advance()
                self.double_quoted([])
                continue
            if self.startswith("$(") and not self.startswith("$(("):
                self.advance(2)
                self.substitution()
                continue
            self.advance()
            if c == open_:
                depth += 1
            elif c == close:
                depth -= 1
                if depth == 0:
                    return
        raise LexError(f"line {line}: unterminated {'$' + open_ if open_ != '(' else '(('}")


# ---- analysis ---------------------------------------------------------------
OPENERS = {"$(", "<("}


def skip_block(toks: list[Tok], j: int) -> int:
    """Index just past the `)$` matching the `$(` or `<(` at toks[j]."""
    depth = 0
    while j < len(toks):
        if toks[j].kind == "op" and toks[j].text in OPENERS:
            depth += 1
        elif toks[j].kind == "op" and toks[j].text == ")$":
            depth -= 1
            if depth == 0:
                return j + 1
        j += 1
    return j


def command_words(toks: list[Tok], j: int) -> tuple[list[Tok], set[str | None], int]:
    """The words of the simple command at toks[j], where its stdout goes, and
    the index of the operator that ends it.

    Substitutions are skipped, and so are redirections, whose targets for
    file descriptor 1 are returned instead: `None` stands for a target this
    gate cannot read.
    """
    words: list[Tok] = []
    stdout: set[str | None] = set()
    fd = None
    while j < len(toks):
        t = toks[j]
        if t.kind == "word":
            if t.glued and t.value is not None and t.value.isdigit():
                fd = t.value  # `2>file`: the number names the descriptor
            else:
                words.append(t)
            j += 1
        elif t.text in OPENERS:
            j = skip_block(toks, j)
        elif t.text in REDIRECTIONS:
            j += 1
            while j < len(toks) and toks[j].kind == "op" and toks[j].text in OPENERS:
                j = skip_block(toks, j)
            target = None
            if j < len(toks) and toks[j].kind == "word":
                target = toks[j].value
                j += 1
            writes_stdout = t.text in ("&>", "&>>") or (
                t.text in (">", ">>", ">|", ">&") and (fd or "1") == "1"
            )
            if writes_stdout:
                stdout.add(target)
            fd = None
        elif t.text in COMMAND_END:
            break
        else:
            j += 1
    return words, stdout, j


def skip_wrappers(words: list[Tok]) -> int:
    """Index of the command a stage runs, past assignments and wrappers."""
    k = 0
    while k < len(words):
        v = words[k].value
        if v is not None and (ASSIGNMENT.match(v) or v in PREFIX_WORDS):
            k += 1
            continue
        name = v.rsplit("/", 1)[-1] if v is not None else None
        if name not in WRAPPERS:
            break
        k += 1
        while k < len(words):
            w = words[k].value
            if w == "--":
                k += 1
                break
            if w is not None and name == "env" and (w == "-" or ASSIGNMENT.match(w)):
                k += 1
            elif w is not None and w.startswith("-") and w != "-":
                k += 2 if w in WRAPPERS[name] else 1
            else:
                break
        if name in TAKES_DURATION and k < len(words):
            k += 1  # the duration
    return k


def early_exit(words: list[Tok], stdout: set[str | None] = frozenset()) -> tuple[str, str] | None:
    """(reader, why) when the pipeline stage `words` stops reading before EOF."""
    k = skip_wrappers(words)
    if k >= len(words) or words[k].value is None:
        return None
    name = words[k].value.rsplit("/", 1)[-1]
    rest = words[k + 1 :]
    args = [w.value for w in rest]
    if name in GREP_COMMANDS:
        flag = grep_early_flag(args)
        if flag:
            return (f"{name} {flag}", "stops reading at its first match")
        if "/dev/null" in stdout:
            # GNU grep (2.27 and later) treats an stdout of /dev/null as -q,
            # so trading -q for a redirect is not a fix.
            return (f"{name} >/dev/null", "stops reading at its first match (GNU grep reads /dev/null output as -q)")
        return None
    # A program holding an expansion is read by its shape: the expansion is an
    # opaque word, and a q or exit around it is still a q or exit.
    shapes = [w.value if w.value is not None else w.shape for w in rest]
    if name in HEAD_COMMANDS and not head_reads_all(shapes):
        return (name, "stops reading after the lines it prints")
    if name in SED_COMMANDS and sed_quits(shapes):
        return (f"{name} q", "stops reading at its q command")
    if name in AWK_COMMANDS:
        why = awk_stops(shapes)
        if why:
            return (f"{name} {why[0]}", why[1])
    if name == "perl" and perl_stops(shapes):
        return ("perl exit", "stops reading at its exit or last")
    if name == "read":
        return ("read", "reads one line and leaves the rest unread")
    return None


def grep_early_flag(args: list[str | None]) -> str | None:
    i = 0
    while i < len(args):
        a = args[i]
        i += 1
        if a is None or a == "-" or not a.startswith("-"):
            continue
        if a == "--":
            return None
        if a.startswith("--"):
            name = a.split("=", 1)[0]
            if name not in GREP_LONG:
                matches = [o for o in GREP_LONG if o.startswith(name)]
                name = matches[0] if len(matches) == 1 else name
            if name in GREP_EARLY_LONG:
                return name
            continue
        for pos, ch in enumerate(a[1:], start=1):
            if ch in GREP_EARLY_SHORT:
                return f"-{ch}"
            if ch in GREP_ARG_OPTS:
                if pos == len(a) - 1:
                    i += 1  # its argument is the next word
                break
    return None


def head_reads_all(args: list[str | None]) -> bool:
    """GNU `head -n -N` (or -c -N) prints all but the last N, so reads to EOF."""
    i = 0
    while i < len(args):
        a = args[i]
        i += 1
        if a is None:
            continue
        if a in ("-n", "-c", "--lines", "--bytes"):
            count = args[i] if i < len(args) else None
            i += 1
        elif a.startswith(("--lines=", "--bytes=")):
            count = a.split("=", 1)[1]
        elif a.startswith(("-n", "-c")) and len(a) > 2:
            count = a[2:]
        else:
            continue
        if count is not None and count.startswith("-"):
            return True
    return False


def sed_scripts(args: list[str | None]) -> list[str | None]:
    scripts: list[str | None] = []
    operands: list[str | None] = []
    i = 0
    while i < len(args):
        a = args[i]
        i += 1
        if a in ("-e", "--expression"):
            scripts.append(args[i] if i < len(args) else None)
            i += 1
        elif a is not None and a.startswith("--expression="):
            scripts.append(a.split("=", 1)[1])
        elif a is not None and a.startswith("-e") and len(a) > 2:
            scripts.append(a[2:])
        elif a in ("-f", "--file") or (a is not None and a.startswith("--file=")):
            return []  # the script is in a file this gate does not read
        elif a is not None and a.startswith("-") and a != "-":
            continue
        else:
            operands.append(a)
    if not scripts and operands:
        scripts.append(operands[0])
    return scripts


def sed_quits(args: list[str | None]) -> bool:
    for script in sed_scripts(args):
        if script is None:
            continue
        i, n = 0, len(script)
        while i < n:
            c = script[i]
            if c in " \t\n;{}!" or c.isdigit() or c in "$,+~" + OPAQUE:
                i += 1
            elif c in "/\\":  # an address regex: /re/ or \cREc
                delim = "/" if c == "/" else (script[i + 1] if i + 1 < n else "")
                i += 1 if c == "/" else 2
                while i < n and script[i] != delim:
                    i += 2 if script[i] == "\\" else 1
                i += 1
                while i < n and script[i] in "IM":
                    i += 1
            elif c in "qQ":
                return True
            elif c in "sy" and i + 1 < n:
                delim = script[i + 1]
                i += 2
                for _ in range(2):
                    while i < n and script[i] != delim:
                        i += 2 if script[i] == "\\" else 1
                    i += 1
                while i < n and script[i] not in ";\n}":
                    i += 1
            else:  # every other command runs to the end of its command
                while i < n and script[i] not in ";\n}":
                    i += 1
    return False


def awk_program(args: list[str | None]) -> str | None:
    i = 0
    while i < len(args):
        a = args[i]
        i += 1
        if a in ("-f", "--file"):
            return None  # the program is in a file this gate does not read
        if a in ("-F", "-v", "--assign", "--field-separator"):
            i += 1
        elif a is not None and a.startswith("-") and a != "-":
            continue
        else:
            return a
    return None


AWK_REGEX_AFTER = set("(,;{}!~&|=?:<>+-*%^\n") | {""}
AWK_REGEX_AFTER_WORDS = {"print", "printf", "return", "in", "case"}


def awk_code(program: str) -> str:
    """The program with comments, strings and regex literals blanked, so
    `exit`, braces and `END` are read only where they are code."""
    out: list[str] = []
    i, n = 0, len(program)
    prev = ""  # the last significant character of code
    word = ""  # the identifier that character ended, if any
    gap = False  # whitespace since that character
    while i < n:
        c = program[i]
        if c == "#":
            while i < n and program[i] != "\n":
                i += 1
            continue
        if c == '"' or (c == "/" and (prev in AWK_REGEX_AFTER or word in AWK_REGEX_AFTER_WORDS)):
            quote = c
            i += 1
            bracket = False
            while i < n and (program[i] != quote or bracket):
                ch = program[i]
                if ch == "\\":
                    i += 2
                    continue
                if quote == "/" and ch == "[":
                    bracket = True
                elif quote == "/" and ch == "]":
                    bracket = False
                i += 1
            i += 1
            out.append(" " + quote + quote + " ")
            prev, word, gap = quote, "", False
            continue
        out.append(c)
        if c.isalnum() or c == "_":
            word = word + c if (prev.isalnum() or prev == "_") and not gap else c
            prev, gap = c, False
        elif c == "\n" or not c.isspace():
            prev, word, gap = c, "", False
        else:
            gap = True
        i += 1
    return "".join(out)


AWK_END = re.compile(r"\bEND\s*\{")
AWK_BEGIN = re.compile(r"\bBEGIN\s*\{")
AWK_FUNCTION = re.compile(r"\bfunc(?:tion)?\s+\w+\s*\([^)]*\)\s*\{")


def awk_blocks(code: str, header: re.Pattern[str]) -> list[tuple[int, int]]:
    """(start, end) of each block in code whose header ends in its `{`,
    braces matched at any depth."""
    spans = []
    for m in header.finditer(code):
        depth, j = 0, m.end() - 1
        while j < len(code):
            if code[j] == "{":
                depth += 1
            elif code[j] == "}":
                depth -= 1
                if depth == 0:
                    break
            j += 1
        spans.append((m.start(), j + 1))
    return spans


def without(code: str, spans: list[tuple[int, int]]) -> str:
    for start, end in sorted(spans, reverse=True):
        code = code[:start] + " " + code[end:]
    return code


def awk_stops(args: list[str | None]) -> tuple[str, str] | None:
    program = awk_program(args)
    if program is None:
        return None
    code = awk_code(program)
    ends = awk_blocks(code, AWK_END)
    # An exit inside END runs once the input has been read.
    main = without(code, ends)
    for verb in ("exit", "nextfile"):
        if re.search(rf"\b{verb}\b", main):
            return (verb, f"stops reading at its {verb} statement")
    # With only BEGIN rules (and functions), awk reads no input, or only the
    # lines a getline takes. A getline in a loop may read it all.
    rules = without(main, awk_blocks(main, AWK_BEGIN) + awk_blocks(main, AWK_FUNCTION))
    if not ends and not re.sub(r"[\s;]", "", rules):
        if not re.search(r"\bgetline\b", code):
            return ("BEGIN", "has only BEGIN rules, which read no input")
        if not re.search(r"\b(?:while|for|do)\b", code):
            return ("BEGIN", "has only BEGIN rules, which read only the lines its getline takes")
    return None


def perl_stops(args: list[str | None]) -> bool:
    """perl -n or -p (an implicit read loop) whose program can exit or last."""
    loops = False
    programs: list[str] = []
    i = 0
    while i < len(args):
        a = args[i]
        i += 1
        if a is None or a == "--" or not a.startswith("-") or a.startswith("--"):
            break
        cluster = a[1:]
        pos = 0
        while pos < len(cluster):
            ch = cluster[pos]
            pos += 1
            if ch in "np":
                loops = True
            elif ch in "eE":
                arg = cluster[pos:] or (args[i] if i < len(args) else None)
                if not cluster[pos:]:
                    i += 1
                if arg is not None:
                    programs.append(arg)
                break
            elif ch in "l0":
                while pos < len(cluster) and cluster[pos] in "0123456789abcdefABCDEFx":
                    pos += 1
            elif ch in "iMmICdDxVF":
                break  # the rest of the cluster is this option's argument
    return loops and any(re.search(r"\b(exit|last)\b", perl_code(prog)) for prog in programs)


def perl_code(program: str) -> str:
    """A perl program with its strings, variables and END blocks blanked: an
    `exit` there is text, a name such as $exit, or runs after the input."""
    code = re.sub(r"'(?:[^'\\]|\\.)*'|\"(?:[^\"\\]|\\.)*\"", '""', program)
    code = re.sub(r"[$@%]\{?\w+", "$v", code)
    return without(code, awk_blocks(code, AWK_END))


def starts_at(toks: list[Tok], j: int, prev: Tok | None) -> bool:
    return toks[j].kind == "word" and starts_command(prev)


def loop_exit(toks: list[Tok], j: int) -> Tok | None:
    """The break, exit or return that can end the while/until loop at toks[j]
    before its input does, or None.

    One after `read ... ||` is the loop's own end of input, not an early one.
    """
    depth = 0  # loops open around this point, counted by their `do`
    prev: Tok | None = None
    name: str | None = None  # the command the current simple command runs
    or_left: str | None = None  # the command on the left of a `||` just read
    after_assignment = False
    k = j
    while k < len(toks):
        t = toks[k]
        if t.kind == "op" and t.text in OPENERS:
            k = skip_block(toks, k)
            prev = Tok("word", "$()", None, t.line)
            continue
        if t.kind == "op":
            or_left = name if t.text == "||" else None
            after_assignment = False
        # A `)` here ends a case pattern; a subshell's `)` is never followed
        # by a command word.
        elif starts_command(prev) or after_assignment or (prev is not None and prev.text == ")"):
            after_assignment = t.value is not None and bool(ASSIGNMENT.match(t.value))
            if not after_assignment:
                name = t.value
            if t.value == "do":
                depth += 1
            elif t.value == "done":
                depth -= 1
                if depth == 0:
                    return None
            elif depth > 0 and t.value in LOOP_EXITS and not (
                prev is not None and prev.text == "||" and or_left == "read"
            ):
                if t.value != "break":
                    return t
                levels = 1
                nxt = toks[k + 1] if k + 1 < len(toks) else None
                if nxt is not None and nxt.kind == "word" and (nxt.value or "").isdigit():
                    levels = int(nxt.value)
                if levels >= depth:
                    return t
        prev = t
        k += 1
    return None


def stage_hazard(toks: list[Tok], j: int) -> tuple[Tok, str, str, int] | None:
    """(token, reader, why, end) when the pipeline stage at toks[j] stops
    reading before the end of its input; `end` indexes the operator after it."""
    while j < len(toks) and toks[j].kind == "op" and toks[j].text == "nl":
        j += 1
    if j >= len(toks):
        return None
    t = toks[j]
    # A group, a subshell or an `if` reads first with its first command.
    if (t.kind == "word" and t.value in ("{", "if")) or (t.kind == "op" and t.text == "("):
        hit = stage_hazard(toks, j + 1)
        return (hit[0], hit[1], hit[2], len(toks)) if hit else None
    if t.kind == "word" and t.value in LOOP_WORDS:
        stop = loop_exit(toks, j)
        if stop is None:
            return None
        why = f"can {stop.value} (line {stop.line}) before the end of its input"
        return (t, f"{t.value} loop", why, len(toks))
    words, stdout, end = command_words(toks, j)
    hit = early_exit(words, stdout)
    return (words[0], hit[0], hit[1], end) if hit else None


def ends_substitution(toks: list[Tok], j: int) -> bool:
    """True when the pipeline running on from toks[j] is the last command
    before the `)$` that closes the substitution it is in."""
    depth = 0
    while j < len(toks):
        t = toks[j]
        if t.kind == "op" and t.text in OPENERS:
            j = skip_block(toks, j)
            continue
        if t.kind == "op":
            if t.text == "(":
                depth += 1
            elif t.text == ")":
                depth -= 1
            elif t.text == ")$":
                return depth == 0
            elif depth == 0 and t.text in (";", "nl"):
                # Only line ends and a `;` may sit between it and the `)`.
                k = j
                while k < len(toks) and toks[k].kind == "op" and toks[k].text in (";", "nl"):
                    k += 1
                return k < len(toks) and toks[k].kind == "op" and toks[k].text == ")$"
            elif depth == 0 and t.text not in PIPE_OPS:
                return False
        j += 1
    return False


@dataclass
class Scope:
    prev: Tok | None = None
    case: list[str] = field(default_factory=list)
    in_test: bool = False  # inside [[ ... ]]
    test_line: int = 0
    input_sub: bool = False  # inside <(...), whose status no one reads


def scan_tokens(toks: list[Tok]) -> list[Finding]:
    findings: list[Finding] = []
    stack = [Scope()]
    for i, t in enumerate(toks):
        st = stack[-1]
        if t.kind == "op" and t.text in OPENERS:
            stack.append(Scope(input_sub=t.text == "<("))
            continue
        if t.kind == "op" and t.text == ")$":
            if st.in_test:
                raise LexError(f"line {st.test_line}: a [[ never closed by ]]")
            if len(stack) > 1:
                stack.pop()
            continue
        if st.in_test:  # a `|` inside [[ ]] is a regex alternation, on any line of it
            if t.kind == "word" and t.text == "]]":  # unquoted: a quoted "]]" is an operand
                st.in_test = False
            st.prev = t
            continue
        if st.case and st.case[-1] in ("word", "pattern"):  # a `|` here separates patterns
            if t.kind == "word" and t.value == "in" and st.case[-1] == "word":
                st.case[-1] = "pattern"
            elif t.kind == "word" and t.value == "esac" and st.case[-1] == "pattern":
                st.case.pop()
            elif t.kind == "op" and t.text == ")" and st.case[-1] == "pattern":
                st.case[-1] = "body"
                st.prev = Tok("op", "nl", None, t.line)
                continue
            st.prev = t
            continue
        if t.kind == "word" and starts_command(st.prev):
            if t.value == "case":
                st.case.append("word")
            elif t.value == "esac" and st.case:
                st.case.pop()
            elif t.text == "[[":
                st.in_test = True
                st.test_line = t.line
        elif t.kind == "op" and t.text in CASE_BREAKS and st.case:
            st.case[-1] = "pattern"
        elif t.kind == "op" and t.text in PIPE_OPS:
            hit = stage_hazard(toks, i + 1)
            if hit and not (st.input_sub and ends_substitution(toks, hit[3])):
                findings.append(Finding(hit[0].line, hit[1], hit[2]))
        st.prev = t
    if stack[-1].in_test:
        raise LexError(f"line {stack[-1].test_line}: a [[ never closed by ]]")
    return findings


def scan_shell(text: str, first_line: int = 1) -> list[Finding]:
    return scan_tokens(Lexer(text, first_line).lex())


# ---- workflows ----------------------------------------------------------------
RUN_KEY = re.compile(r"^(?P<lead>\s*(?:-\s+)?)run:\s*(?P<rest>.*)$")
SHELL_KEY = re.compile(r"^(?P<lead>\s*(?:-\s+)?)shell:\s*(?P<value>\S+)")


def indent_of(line: str) -> int:
    return len(line) - len(line.lstrip(" "))


def run_blocks(text: str) -> list[tuple[int, str]]:
    """(first line number, script) for each shell `run:` of a workflow."""
    lines = text.split("\n")
    blocks: list[tuple[int, str]] = []
    for idx, line in enumerate(lines):
        m = RUN_KEY.match(line)
        if not m:
            continue
        col = len(m.group("lead"))
        if not runs_in_sh(lines, idx, col):
            continue
        rest = m.group("rest").strip()
        if rest[:1] in ("|", ">"):
            body: list[str] = []
            indent = None
            for nxt in lines[idx + 1 :]:
                if not nxt.strip():
                    body.append("")
                    continue
                width = indent_of(nxt)
                if indent is None:
                    if width <= col:
                        break
                    indent = width
                if width < indent:
                    break
                body.append(nxt[indent:])
            blocks.append((idx + 2, "\n".join(body)))
        elif rest:
            if len(rest) > 1 and rest[0] in "'\"" and rest[-1] == rest[0]:
                rest = rest[1:-1]
            blocks.append((idx + 1, rest))
    return blocks


def runs_in_sh(lines: list[str], idx: int, col: int) -> bool:
    """False when the step's own `shell:` names something other than sh or bash."""
    # The step's mapping runs from its `- ` line (this one, for `- run:`) to
    # the next line indented less than its keys.
    start = idx
    if not lines[idx].lstrip().startswith("- "):
        start -= 1
        while start >= 0:
            above = lines[start]
            if above.strip():
                if indent_of(above) == col - 2 and above.lstrip().startswith("- "):
                    break
                if indent_of(above) < col:
                    return True  # not a sequence item: the default shell
            start -= 1
        if start < 0:
            return True
    end = idx + 1
    while end < len(lines) and (not lines[end].strip() or indent_of(lines[end]) >= col):
        end += 1
    for k in range(start, end):
        m = SHELL_KEY.match(lines[k])
        if m and len(m.group("lead")) == col:
            return m.group("value").strip("'\"") in ("bash", "sh")
    return True


# ---- files ----------------------------------------------------------------------
HOOK_DIR = ".husky/"


def is_hook(rel: str) -> bool:
    """A Git hook husky runs with sh: `.husky/<hook>`, no suffix, no shebang."""
    name = rel[len(HOOK_DIR) :] if rel.startswith(HOOK_DIR) else ""
    return bool(name) and "/" not in name and "." not in name


def is_shell(path: Path) -> bool:
    if path.suffix in SHELL_SUFFIXES:
        return True
    try:
        with path.open("rb") as fh:
            return bool(SHEBANG.match(fh.readline(200)))
    except OSError:
        return False


def tracked_files() -> list[Path]:
    """Tracked files, and untracked ones not ignored: the local gate runs on
    uncommitted work, and a new script must not wait for CI to be read."""
    out = subprocess.run(
        ["git", "-C", str(REPO_ROOT), "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
        capture_output=True,
        check=True,
    ).stdout
    files = []
    for raw in out.split(b"\0"):
        if not raw:
            continue
        rel = raw.decode("utf-8", "surrogateescape")
        path = REPO_ROOT / rel
        if path.is_symlink() or not path.is_file():
            continue
        if rel.startswith(WORKFLOW_DIR) and path.suffix in YAML_SUFFIXES:
            files.append(path)
        elif path.suffix not in YAML_SUFFIXES and (is_hook(rel) or is_shell(path)):
            files.append(path)
    return files


def scan_file(path: Path) -> list[Finding]:
    text = path.read_text(encoding="utf-8", errors="surrogateescape")
    if path.suffix in YAML_SUFFIXES:
        return [f for first, script in run_blocks(text) for f in scan_shell(script, first)]
    return scan_shell(text)


def display(path: Path) -> str:
    try:
        return str(path.resolve().relative_to(REPO_ROOT))
    except ValueError:
        return str(path)


GUIDANCE = """\
A reader that stops before the end of its input leaves the command writing
into it to die of SIGPIPE, and under pipefail that death is the pipeline's
status: a match reads as a miss, or `set -e` ends the script. It is a race,
so it fails under load, and every time on a large input (#2360). Give the
reader no concurrent writer:
  grep -q PAT <<<"$var"                  a here-string
  [[ $var == *text* ]]                   no process at all
  grep -q PAT "$file"                    grep reads the file itself
  out="$(cmd)"; grep -q PAT <<<"$out"    capture, then a here-string
  first="${out%%$'\\n'*}"  or  cmd | sed -n 1p   a first line that drains
`cmd | grep PAT >/dev/null` is no fix: GNU grep reads that redirect as -q.
"""


def main(argv: list[str]) -> int:
    list_only = "--list-files" in argv
    paths = [a for a in argv if a != "--list-files"]
    if paths:
        files = []
        for p in paths:
            path = Path(p)
            if not path.is_file():
                print(f"ERROR: {p}: no such file", file=sys.stderr)
                return 2
            files.append(path)
    else:
        try:
            files = tracked_files()
        except (OSError, subprocess.CalledProcessError) as exc:
            print(f"ERROR: cannot list the tracked files under {REPO_ROOT}: {exc}", file=sys.stderr)
            return 2
        if not files:
            print(f"ERROR: no shell scripts or workflows found under {REPO_ROOT}", file=sys.stderr)
            return 2
    if list_only:
        for path in files:
            print(display(path))
        return 0

    hits: list[tuple[Path, Finding]] = []
    for path in files:
        try:
            hits.extend((path, f) for f in scan_file(path))
        except (LexError, OSError) as exc:
            print(f"ERROR: cannot read {display(path)} as shell: {exc}", file=sys.stderr)
            return 2

    if not hits:
        print(f"OK: no pipe into an early-exit reader in {len(files)} shell scripts and workflows")
        return 0
    print(f"FAIL: {len(hits)} pipe(s) into a reader that exits early:", file=sys.stderr)
    for path, f in hits:
        lines = path.read_text(encoding="utf-8", errors="surrogateescape").split("\n")
        src = lines[f.line - 1].strip() if 0 < f.line <= len(lines) else ""
        print(f"  {display(path)}:{f.line}: {f.reader} {f.why}", file=sys.stderr)
        print(f"      {src}", file=sys.stderr)
    print("\n" + GUIDANCE, file=sys.stderr, end="")
    return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
