// How Oxy is drawn: a PNG over the kitty graphics protocol, or half-block cells.
export type Renderer = 'image' | 'cells'

// How big Oxy is drawn; a fixed height in rows, as wide as the art needs.
export type Size = 'small' | 'medium' | 'large'

declare module 'claude-code' {
  interface PluginState {
    'ox-cli-oxy': {
      // This session's choices; null until made. A /clear or an in-app resume
      // starts a session with none, and the mod falls back to what the
      // terminal and $.store say.
      /** `/hatch-pet off` hides Oxy and restores Claude Code's own spinner. */
      hidden: boolean | null
      renderer: Renderer | null
      size: Size | null
      /** `/hatch-pet <animation>` pins one animation; null follows Claude. */
      pinned: string | null
    }
  }
}
