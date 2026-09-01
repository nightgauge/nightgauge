# The Unobserved-Mechanism Rule — Worked Example

## Contents

- [The rule](#the-rule)
- [Why both examples passed review](#why-both-examples-passed-review)
- [Validating a diagnostic before you trust it](#validating-a-diagnostic-before-you-trust-it)
- [Shipping mitigation deliberately](#shipping-mitigation-deliberately)

## The rule

**UNOBSERVED-MECHANISM RULE — do not land a retry, a fallback, a widened
timeout, or added tolerance for a failure whose mechanism you have not directly
observed.**

Not "prefer not to". Not "unless you are fairly confident". The rule binds on
whether the mechanism was _observed_, because confidence is exactly the signal
that fails here — both examples below were shipped by sessions that were sure.

## Why both examples passed review

During an investigation into a nightly E2E sweep that had been red for five
weeks, two pieces of code shipped as "instrumentation". Each encoded an
unverified hypothesis about a failure nobody had yet observed. Neither was
caught, and the reason they were not is the useful part.

**The magic-link redelivery retry.** Deliver the sign-in link; if nothing
happens within 25 seconds, deliver it again. It was documented as diagnostic
rather than a cure — the reasoning being that a second delivery would reveal
whether the first had been lost.

It was persuasive because it was framed as a question rather than an answer, and
because a redelivery is cheap and harmless in itself. What that framing hid is
that the retry does not merely observe: it changes the outcome. The moment
anyone fixed the real bug, the retry would have re-hidden it, converting a
genuine regression of "one tap signs you in" into a green run. A diagnostic that
can turn a failing case green is not a diagnostic.

**The cold-restart probe.** It reported, in the run log,
`no session was persisted, so verification genuinely never completed`.

Verification had in fact succeeded, and the session _had_ persisted. The probe
reported the exact opposite of the truth, in a confident declarative sentence,
and sent the following session down the wrong path for its entire duration.

It was persuasive because it was read-only — it changed nothing, so it looked
like it could not do harm. But a probe that is wrong does not merely fail to
help. It authoritatively misdirects whoever reads it next, and it does so with
more force than a bare unknown would, because a stated finding stops the search.
The cost of that probe was a whole session, which is more than the bug had cost
to that point.

The common shape: **both were framed as diagnostics rather than as fixes, and
neither was ever exercised against a case whose answer was already known.**

## Validating a diagnostic before you trust it

Diagnostic code added to a test harness is code you are shipping, and it makes
claims. Before you rely on a word it says, exercise it against **two cases whose
answer you already know**:

- a **known-good** case, where the thing it detects is definitely absent — it
  must stay quiet. A probe that fires on a healthy system tells you nothing when
  it fires on a sick one.
- a **known-bad** case, where the thing it detects is definitely present — it
  must fire. A probe that cannot detect the condition it was written for is a
  guaranteed false all-clear.

The cold-restart probe would have died on either one.

**If running both cases is impractical — do not ship it.** That is the whole
instruction, and it is deliberately not "ship it with a caveat in a doc
comment". An unvalidated probe left in the tree is read as a measurement by
everyone who comes after, including you in three weeks. A caveat does not
survive the copy-paste into a summary; the sentence the probe prints does.

## Shipping mitigation deliberately

There are real cases for landing a retry or a widened tolerance with the
mechanism still unknown — an external dependency you do not control, a release
that has to go out. When that is the call, it carries **a machine-readable
marker and a tracking issue**, adjacent to the code:

```
NIGHTGAUGE-MITIGATION: issue=owner/repo#123 mechanism=unobserved
```

Prose in a doc comment is not sufficient and is not accepted. Prose cannot be
swept, cannot be counted, and cannot be found again by anyone who does not
already know it is there — which is the same property that let both examples
above survive review. `nightgauge preflight mitigation-rule` fails on a marker
that carries no `issue=`, so an undertaking to come back is recorded somewhere
that outlives the session that made it.
