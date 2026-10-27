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
  /** Every surfaced item counted by ref_type; surfaced[] is capped for display. */
  surfaced_by_type?: Record<string, number>
  relevant?: UsageRelevant
  /** Whether SageOx would be credited as a co-author of this session's work. */
  attribution?: UsageAttribution
}

/** How many used items rest on each kind of evidence. */
export type UsageEvidenceCounts = {
  read: number
  cited: number
  reported: number
  judged: number
}

/** One surfaced item the AI coworker used, strongest evidence per ref, in seq order. */
export type UsageUsedItem = {
  ref: string
  ref_type?: string
  title?: string
  /** Only discussions, plans and sessions carry one. */
  url?: string
  /** read | cited | reported | judged */
  evidence: string
  /** provable | reported | judged (parsed, never drawn) */
  grade: string
  /** read | requery | cited | cited_commit | reported | judged */
  detail?: string
  seq?: number
  /** Short sha of the commit that cited the item (detail cited_commit). */
  commit?: string
  /** The AI coworker's reason (evidence reported). */
  why?: string
}

export type UsageRelevant = {
  count: number
  surfaced_count: number
  by_evidence?: UsageEvidenceCounts
  items?: UsageUsedItem[]
  /** The AI coworker reported it used nothing. */
  reported_none?: boolean
  derived?: { category: string; reason?: string }
}

export type UsageAttribution = {
  would_attribute: boolean
  /** none | minor | moderate | significant | critical */
  category: string
  reason?: string
  /** self-reported | derived */
  source: string
  threshold: number
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
