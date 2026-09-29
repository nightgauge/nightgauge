# Decisions: Retry failed harbour-fee webhooks

## Capped exponential backoff

Retries wait 1s, 2s, 4s, 8s and 16s. The cap keeps a slow endpoint from
holding a worker for minutes.
