// The slice of the `ox usage --json` session report the pane draws. The ox
// binary owns the full shape (internal/usage/types.go); fields the pane does
// not read are left out, and every one it reads is treated as optional at
// parse time so an older or newer ox never breaks the drawing.

export type UsageRecentCall = {
  label: string
  command: string
  seq?: number
  tokens: number
  failed?: boolean
  ts?: string
}

export type UsageReport = {
  view?: string
  session: {
    name?: string
    agent_id?: string
    harness?: string
    recording: boolean
    turns?: number
  }
  trace: { available: boolean; source?: string; reason?: string }
  calls: { total: number; failed: number; recent: UsageRecentCall[] }
  injected: { total_tokens: number; estimated?: boolean }
  surfaced: unknown[]
  used: { status: string }
}

export type UsageStatus = {
  /** One short line saying why the last refresh failed; absent when it worked. */
  error?: string
  /** When the last successful refresh landed, in ms since the epoch. */
  refreshedAt?: number
  /** True while the person has not scrolled up: new rows keep the end in view. */
  follow: boolean
}

declare module 'claude-code' {
  interface PluginState {
    'ox-cli-usage-pane': {
      report: Shaped<UsageReport | null>
      status: Shaped<UsageStatus>
    }
  }
}
