# Spike #4042: Valid recommendations fixture

**Issue**: #4042
**Status**: Complete

## Executive Summary

Three recommendations covering adopt, defer, and skip actions.

## 1. Findings

Some prose findings here.

## Recommendations

```yaml recommendations
spike: 4042
recommendations:
  - id: alpha
    action: adopt
    title: "First adopted recommendation"
    type: feature
    priority: high
    size: M
    labels: ["component:scheduler"]
    body: |
      Implement the alpha follow-up.
    depends_on: []
  - id: beta
    action: defer
    title: "Deferred follow-up"
    type: chore
    priority: low
    size: S
    depends_on: []
  - id: gamma
    action: skip
    title: "Skipped recommendation — context only"
    type: docs
    priority: low
    size: XS
```
