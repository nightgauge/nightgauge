# PRD: Retry failed harbour-fee webhooks

Fictional demo content for the `harbor-api` service of the Lanternworks demo.

## Problem

A harbour-fee webhook that fails once is dropped, so berth invoices go out
late. Failed deliveries need a bounded retry.

## Requirements

- Retry a failed delivery with capped exponential backoff.
- Stop after five attempts and record the failure.
