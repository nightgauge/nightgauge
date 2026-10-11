package adapters

import (
	"slices"
	"strings"
	"testing"
)

// Only the adapters spike #1568 adopted host conversation turns, and none of
// the pipeline builders is one: a turn never dispatches through BuildCommand.
func TestConversationBuilders_OnlyTheAdoptedAdapters(t *testing.T) {
	r := NewRegistry()
	if got := r.ConversationAdapters(); !slices.Equal(got, []string{"claude-headless"}) {
		t.Errorf("conversation adapters = %v, want [claude-headless]", got)
	}
	for _, name := range []string{"claude-sdk", "codex", "gemini", "gemini-sdk", "copilot", "grok", "opencode"} {
		if _, ok := r.ConversationBuilder(name); ok {
			t.Errorf("%s has a conversational builder; spike #1568 deferred it", name)
		}
	}
	if _, ok := r.ConversationBuilder("claude"); !ok {
		t.Error("the claude alias does not resolve to claude-headless's builder")
	}
}

// The argv contract (spike #1568 § 13, condition 2): every argv a registered
// conversational builder can produce carries its persistence-off control and
// an explicit permission posture, and no forbidden flag, whatever the
// options. The options carry no prompt and no permission setting, so the
// message can never reach argv and no workspace setting can widen the
// posture.
func TestConversationBuilders_ArgvContract(t *testing.T) {
	r := NewRegistry()
	flags, modes := ConversationForbiddenFlags()
	for _, name := range r.ConversationAdapters() {
		b, _ := r.ConversationBuilder(name)
		for _, model := range []string{"", "claude-sonnet-5-5", "--resume"} {
			for _, effort := range []string{"", "low", "max"} {
				for _, thinkingOff := range []bool{false, true} {
					for _, tools := range [][]string{nil, {"Read", "Grep"}} {
						cmd, args, env := b.BuildConversationTurn(ConversationTurnOptions{
							Model: model, Effort: effort, ThinkingOff: thinkingOff, Tools: tools,
						})
						if cmd != "claude" {
							t.Fatalf("%s: command %q", name, cmd)
						}
						for i, a := range args {
							if slices.Contains(flags, a) && !(i > 0 && args[i-1] == "--model") {
								t.Errorf("%s: argv carries %s: %v", name, a, args)
							}
							if slices.Contains(modes, a) {
								t.Errorf("%s: argv carries permission mode %s: %v", name, a, args)
							}
						}
						for _, want := range [][2]string{{"--permission-mode", "dontAsk"}, {"--tools", strings.Join(tools, ",")}} {
							if i := slices.Index(args, want[0]); i < 0 || i+1 >= len(args) || args[i+1] != want[1] {
								t.Errorf("%s: argv lacks %s %q: %v", name, want[0], want[1], args)
							}
						}
						for _, want := range []string{"-p", "--no-session-persistence", "--restricted", "--strict-mcp-config", "--include-partial-messages"} {
							if !slices.Contains(args, want) {
								t.Errorf("%s: argv lacks %s: %v", name, want, args)
							}
						}
						if got := slices.Contains(args, "--effort"); got != (effort != "") {
							t.Errorf("%s: --effort present %v for effort %q", name, got, effort)
						}
						if (env["CLAUDE_CODE_DISABLE_THINKING"] == "1") != thinkingOff {
							t.Errorf("%s: thinking env %v for ThinkingOff %v", name, env, thinkingOff)
						}
					}
				}
			}
		}
	}
}
