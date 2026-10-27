// The slice of the `ox brief --json` session report the pane draws. The ox
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

/** One metered source of the context ox put in front of the model. */
export type UsageContextSource = {
  source: string
  tokens: number
  count?: number
}

/** Evidence that the AI coworker used a surfaced item. */
export type UsageUseEvidence = {
  evidence: string
  seq?: number
  grade?: string
}

/** Something ox surfaced: a discussion, plan, doc, skill, murmur, … */
export type UsageSurfaced = {
  ref: string
  ref_type: string
  from?: string
  from_name?: string
  topic?: string
  via?: string
  title?: string
  url?: string
  seq?: number
  used?: UsageUseEvidence
}

export type UsageReport = {
  view?: string
  /** Who the session is (name, harness) is not drawn; only whether it records. */
  session: { recording: boolean }
  trace: { available: boolean; source?: string; reason?: string }
  calls: { total: number; failed: number; recent: UsageRecentCall[] }
  context: { tokens: number; estimated?: boolean; by_source: UsageContextSource[] }
  surfaced: UsageSurfaced[]
  relevant?: { count: number; surfaced_count: number }
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
    'ox-cli-brief-pane': {
      report: Shaped<UsageReport | null>
      status: Shaped<UsageStatus>
    }
  }
}
