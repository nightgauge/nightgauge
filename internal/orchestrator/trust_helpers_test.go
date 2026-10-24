package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/nightgauge/nightgauge/internal/trust"
)

// fakeTrustForge answers the eligibility gate's reads from maps (#2130) and
// counts permission calls. A login absent from levels gets defaultLevel; an
// issue absent from signals is authored by defaultAuthor.
type fakeTrustForge struct {
	mu            sync.Mutex
	levels        map[string]trust.Level
	defaultLevel  trust.Level
	signals       map[string]trust.Signals // "owner/repo#N"
	defaultAuthor string
	permCalls     int
}

func (f *fakeTrustForge) RepoPermission(_ context.Context, _, _, login string) (trust.Level, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.permCalls++
	if l, ok := f.levels[login]; ok {
		return l, nil
	}
	if f.defaultLevel != "" {
		return f.defaultLevel, nil
	}
	return "", fmt.Errorf("REST 404: %s is not a collaborator", login)
}

func (f *fakeTrustForge) IssueSignals(_ context.Context, owner, repo string, number int) (trust.Signals, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.signals[strings.ToLower(fmt.Sprintf("%s/%s#%d", owner, repo, number))]; ok {
		return s, nil
	}
	if f.defaultAuthor != "" {
		return trust.Signals{Author: f.defaultAuthor}, nil
	}
	return trust.Signals{}, fmt.Errorf("no such issue %s/%s#%d", owner, repo, number)
}

func (f *fakeTrustForge) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.permCalls
}

// allWritersGate passes every issue: every author has write access. For tests
// about something other than eligibility.
func allWritersGate() *trust.Gate {
	return trust.NewGate(&fakeTrustForge{defaultLevel: trust.LevelWrite, defaultAuthor: "maintainer"}, nil)
}

// levelsGate answers from levels; any other login is not a collaborator.
func levelsGate(levels map[string]trust.Level) (*trust.Gate, *fakeTrustForge) {
	f := &fakeTrustForge{levels: levels, signals: map[string]trust.Signals{}}
	return trust.NewGate(f, nil), f
}
