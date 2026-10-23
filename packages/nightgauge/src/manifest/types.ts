/**
 * TypeScript interfaces for VSCode extension manifest contribution points.
 *
 * These types mirror the VSCode extension manifest schema and are used
 * exclusively at build time by the manifest generator. They are NOT
 * imported at runtime.
 */

export interface CommandContribution {
  command: string;
  title: string;
  icon?: string;
  category?: string;
  shortTitle?: string;
  enablement?: string;
}

export interface ViewContribution {
  id: string;
  name: string;
  icon?: string;
  contextualTitle?: string;
  when?: string;
  visibility?: "visible" | "collapsed" | "hidden";
  type?: string;
}

export interface ViewsWelcomeContribution {
  view: string;
  contents: string;
  when?: string;
}

export interface MenuItemContribution {
  command: string;
  when?: string;
  group?: string;
  alt?: string;
}

export interface KeybindingContribution {
  command: string;
  key: string;
  mac?: string;
  when?: string;
}

export interface ViewsContainerContribution {
  id: string;
  title: string;
  icon: string;
}

export interface ManifestContributes {
  viewsContainers: {
    activitybar: ViewsContainerContribution[];
  };
  views: Record<string, ViewContribution[]>;
  viewsWelcome: ViewsWelcomeContribution[];
  commands: CommandContribution[];
  menus: Record<string, MenuItemContribution[]>;
  keybindings: KeybindingContribution[];
}
