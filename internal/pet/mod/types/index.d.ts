// How Oxy is drawn: a PNG over the kitty graphics protocol, or half-block cells.
export type Renderer = 'image' | 'cells'

// How big Oxy is drawn; a fixed height in rows, as wide as the art needs.
export type Size = 'small' | 'medium' | 'large'

declare module 'claude-code' {
  interface PluginState {
    'ox-cli-oxy': {
      /** `/hatch-pet off` hides Oxy and restores Claude Code's own spinner. */
      hidden: boolean
      renderer: Renderer
      size: Size
      /** `/hatch-pet <animation>` pins one animation; null follows Claude. */
      pinned: string | null
    }
  }
}
