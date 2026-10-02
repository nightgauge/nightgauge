/**
 * CommandRedeliveryGuard — carry out each platform command once, however many
 * times it is delivered, and acknowledge it once (#2334).
 *
 * The platform's command stream delivers at least once. A command published
 * while a (re)connecting stream replays its backlog arrives twice, and a
 * command that is still unacknowledged is delivered again on every reconnect.
 * A consumer that acted on every copy would apply a verb twice, and the second
 * copy's ack, a no-op such as `already-paused`, could reach the platform
 * before the first copy's `applied` and become the outcome it records.
 *
 * The guard reaches one decision per command id. A later copy waits for that
 * decision to settle: when its ack reached the platform the copy is dropped,
 * and when it did not, the same ack is sent again. A copy never re-applies the
 * command and never decides anything of its own.
 *
 * @see RunVerbCommandHandler — the run verbs
 * @see AgentCommandDispatcher — the refusal of an unsupported type
 */

/** How many command ids are remembered: far more than are ever live at once. */
export const REMEMBERED_COMMAND_IDS = 512;

interface Consumption<D> {
  /** Settles once the decision is made and its ack sent or failed. */
  settled: Promise<void>;
  decision: D | null;
  /** Whether the platform accepted the ack. */
  delivered: boolean;
}

export class CommandRedeliveryGuard<D> {
  private readonly consumptions = new Map<string, Consumption<D>>();

  constructor(private readonly capacity = REMEMBERED_COMMAND_IDS) {}

  /**
   * Consume one delivered copy of command `id`.
   *
   * `decide` carries the command out and returns the ack to send, or null when
   * there is none; `send` sends that ack and resolves whether the platform
   * accepted it. Neither may throw: each handles and logs its own failures.
   *
   * The first copy calls `decide` before this returns, so commands handled in
   * one tick are applied in the order they arrived. A command with no id
   * cannot be recognised again and is consumed as it comes.
   */
  consume(
    id: string,
    decide: () => Promise<D | null>,
    send: (ack: D) => Promise<boolean>
  ): Promise<void> {
    const earlier = id ? this.consumptions.get(id) : undefined;
    if (earlier) {
      earlier.settled = earlier.settled.then(async () => {
        if (earlier.delivered || earlier.decision === null) return;
        earlier.delivered = await send(earlier.decision);
      });
      return earlier.settled;
    }

    const consumption: Consumption<D> = {
      settled: Promise.resolve(),
      decision: null,
      delivered: false,
    };
    if (id) this.remember(id, consumption);
    consumption.settled = (async () => {
      consumption.decision = await decide();
      if (consumption.decision !== null) {
        consumption.delivered = await send(consumption.decision);
      }
    })();
    return consumption.settled;
  }

  private remember(id: string, consumption: Consumption<D>): void {
    this.consumptions.set(id, consumption);
    while (this.consumptions.size > this.capacity) {
      const oldest = this.consumptions.keys().next().value;
      if (oldest === undefined) break;
      this.consumptions.delete(oldest);
    }
  }
}
