/**
 * `Nightgauge: Open Conversation` (#1571): opens the chat panel on the
 * hosted conversation contract, as the signed-in account.
 */

import * as vscode from "vscode";
import { TokenStorage } from "../platform/TokenStorage";
import type { IOnDemandTokenRefresher } from "../platform/TokenRefreshManager";
import { ConversationClient } from "../services/conversation/ConversationClient";
import { createConversationStreamOpener } from "../services/conversation/ConversationStream";
import { isSignedInToHostedService } from "../services/RunVisibility";
import type { Logger } from "../utils/logger";
import {
  ConversationPanel,
  OPEN_CONVERSATION_COMMAND,
} from "../views/conversation/ConversationPanel";

export function registerOpenConversationCommand(deps: {
  getPlatformUrl: () => string;
  tokenRefresher: IOnDemandTokenRefresher;
  getWorkspaceSlug: () => string | null;
  logger: Logger;
}): vscode.Disposable {
  return vscode.commands.registerCommand(OPEN_CONVERSATION_COMMAND, () => {
    const getAccessToken = async (): Promise<string | null> =>
      (await TokenStorage.getInstance()?.retrieve("accessToken")) ?? null;
    ConversationPanel.show({
      api: new ConversationClient(
        deps.getPlatformUrl,
        { retrieve: async () => getAccessToken() },
        deps.tokenRefresher
      ),
      openStream: createConversationStreamOpener({
        getPlatformUrl: deps.getPlatformUrl,
        getAccessToken,
        forceRefresh: () => deps.tokenRefresher.forceRefresh(),
        logger: deps.logger,
      }),
      signedIn: isSignedInToHostedService,
      getWorkspaceSlug: deps.getWorkspaceSlug,
    });
  });
}
