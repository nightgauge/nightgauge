package conversation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nightgauge/nightgauge/internal/credshape"
	"github.com/nightgauge/nightgauge/internal/platform"
)

const (
	maxDeltaBytes       = platform.TurnMaxDeltaBytes
	maxFramesPerRequest = platform.TurnMaxFramesPerRequest
	// defaultFlushInterval is the shortest gap between two delta posts:
	// the reply streams in pieces a reader sees grow, without one request
	// per CLI event.
	defaultFlushInterval = 250 * time.Millisecond
	// maxFlushInterval bounds the gap the delta budget can stretch it to.
	maxFlushInterval = 2 * time.Second
	// postAttempts and postBackoff retry a post that failed in transit.
	// Deltas are idempotent on their sequence number, so a retry that
	// repeats a stored delta is a no-op.
	postAttempts = 3
)

var postBackoff = []time.Duration{250 * time.Millisecond, time.Second}

// errTurnOver is a turn the hosted service reports ended, or one this host
// can no longer write to.
var errTurnOver = errors.New("the turn is over on the hosted service")

// poster streams one turn's reply. Every piece of text passes the redacting
// stream before it is queued, so only redacted text is ever posted. Text is
// coalesced and sent at most every flush interval as deltas of at most
// max_delta_bytes, and the interval stretches so the turn cannot run out of
// deltas before its deadline. The poster is the only writer of the turn's
// frames: the deltas in order, then at most one terminal report.
type poster struct {
	post     func(ctx context.Context, frames []platform.TurnFrame) (platform.TurnFramesResult, error)
	limits   Limits
	deadline time.Time
	interval time.Duration
	// onOver runs once, on its own goroutine, when the hosted service
	// reports the turn ended or a post failed for good: the process stops.
	onOver func(error)

	redact *credshape.Stream

	mu      sync.Mutex
	pending strings.Builder
	seq     int
	sent    int
	over    error

	wake chan struct{}
	quit chan struct{}
	done chan struct{}
}

func newPoster(ctx context.Context, post func(context.Context, []platform.TurnFrame) (platform.TurnFramesResult, error),
	limits Limits, deadline time.Time, interval time.Duration, onOver func(error)) *poster {
	if interval <= 0 {
		interval = defaultFlushInterval
	}
	p := &poster{
		post: post, limits: limits, deadline: deadline, interval: interval, onOver: onOver,
		redact: credshape.NewStream(),
		wake:   make(chan struct{}, 1),
		quit:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go p.loop(ctx)
	return p
}

// write queues reply text. It is redacted here, before anything is queued.
func (p *poster) write(text string) {
	safe := p.redact.Write(text)
	if safe == "" {
		return
	}
	p.mu.Lock()
	p.pending.WriteString(safe)
	full := p.pending.Len() >= p.limits.MaxDeltaBytes
	p.mu.Unlock()
	if full {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
}

// overErr is the reason the turn is over on the hosted service, or nil.
func (p *poster) overErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.over
}

func (p *poster) loop(ctx context.Context) {
	defer close(p.done)
	timer := time.NewTimer(p.nextInterval())
	defer timer.Stop()
	for {
		select {
		case <-p.quit:
			return
		case <-ctx.Done():
			return
		case <-p.wake:
		case <-timer.C:
		}
		p.flush(ctx, nil)
		if p.overErr() != nil {
			return
		}
		timer.Reset(p.nextInterval())
	}
}

// nextInterval is the configured interval while the turn has used less than
// half its deltas. Past that it spreads the deltas left over the time left,
// so a long reply slows its posts instead of running out before its
// deadline; never above maxFlushInterval.
func (p *poster) nextInterval() time.Duration {
	p.mu.Lock()
	left := p.limits.MaxDeltas - p.sent
	p.mu.Unlock()
	d := p.interval
	if left > 1 && left <= p.limits.MaxDeltas/2 {
		if spread := time.Until(p.deadline) / time.Duration(left); spread > d {
			d = spread
		}
	}
	if d > maxFlushInterval {
		d = maxFlushInterval
	}
	return d
}

// finish stops the loop, sends whatever text is held, and then end when it is
// not nil. A nil end sends the held text and no terminal report: the hosted
// service already ended the turn, or the host cannot say how it ended.
func (p *poster) finish(ctx context.Context, end *platform.TurnEnd) error {
	close(p.quit)
	<-p.done
	if tail := p.redact.Flush(); tail != "" {
		p.mu.Lock()
		p.pending.WriteString(tail)
		p.mu.Unlock()
	}
	if p.overErr() != nil {
		return p.overErr()
	}
	p.flush(ctx, end)
	return p.overErr()
}

// flush posts the held text as deltas, then end when it is not nil, in
// batches of at most max_frames_per_request.
func (p *poster) flush(ctx context.Context, end *platform.TurnEnd) {
	p.mu.Lock()
	if p.over != nil {
		p.mu.Unlock()
		return
	}
	text := p.pending.String()
	p.pending.Reset()
	var frames []platform.TurnFrame
	for _, chunk := range splitUTF8(text, p.limits.MaxDeltaBytes) {
		p.seq++
		frames = append(frames, platform.TurnDelta{Seq: p.seq, Text: chunk})
	}
	p.mu.Unlock()
	if end != nil {
		frames = append(frames, *end)
	}
	for len(frames) > 0 {
		n := min(len(frames), p.limits.MaxFramesPerRequest)
		if err := p.send(ctx, frames[:n]); err != nil {
			p.setOver(err)
			return
		}
		frames = frames[n:]
	}
}

// send posts one batch, retrying a failure in transit. A refusal that ends
// the turn, or a reply that reports the turn over, ends the stream.
func (p *poster) send(ctx context.Context, batch []platform.TurnFrame) error {
	var err error
	for attempt := 0; attempt < postAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(postBackoff[min(attempt-1, len(postBackoff)-1)]):
			}
		}
		var res platform.TurnFramesResult
		res, err = p.post(ctx, batch)
		if err == nil {
			p.mu.Lock()
			for _, f := range batch {
				if _, ok := f.(platform.TurnDelta); ok {
					p.sent++
				}
			}
			p.mu.Unlock()
			if platform.TurnStateTerminal(res.Turn.State) && !hasEnd(batch) {
				return errTurnOver
			}
			return nil
		}
		var refused *platform.TurnRequestError
		if errors.As(err, &refused) && refused.Terminal() {
			return err
		}
	}
	return err
}

func hasEnd(batch []platform.TurnFrame) bool {
	for _, f := range batch {
		if _, ok := f.(platform.TurnEnd); ok {
			return true
		}
	}
	return false
}

func (p *poster) setOver(err error) {
	p.mu.Lock()
	first := p.over == nil
	if first {
		p.over = err
	}
	p.mu.Unlock()
	if first && p.onOver != nil {
		go p.onOver(err)
	}
}

// splitUTF8 cuts s into pieces of at most max bytes, never inside a rune.
func splitUTF8(s string, max int) []string {
	var out []string
	for len(s) > 0 {
		if len(s) <= max {
			out = append(out, s)
			break
		}
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 {
			cut = max
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return out
}
