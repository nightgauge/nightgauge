Claude Fable 5.1, this stage only. This stage is one of the two bash-and-editor
loops Anthropic's guidance names, where the next independent reads are implied
by the task rather than asked for. The block below ships as a skill-specific
fragment so the five stages that have nothing to batch never carry it.

### Batching tool requests

First privately list what you need next; then request every item that doesn't
depend on another's result in this one response.
