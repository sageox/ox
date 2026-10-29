// The slice of the `ox session report --json` session report the pane draws. The ox
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

/** The counts `ox statusline --json` reports; zero when omitted. */
export type StatuslineCounts = {
  discussions: number
  plans: number
  docs: number
  sessions: number
  murmurs: number
  used: number
}

/** `ox statusline --json`: what the desktop band above the prompt draws. */
export type Statusline = {
  /** False: the statusline setting is off, or this is not a SageOx project. */
  show: boolean
  counts: StatuslineCounts
  recording: boolean
}


// --- Data flow: what moved between SageOx and this session -----------------
//
// One FlowEvent per ox call (a Bash `ox …` command or a SageOx MCP tool) and
// one per prompt whose hooks brought team context in (prime, murmurs, ledger
// recall). The pure classifier in hooks/flow.ts makes them; hooks/scene.ts
// draws one as an SVG lane under the transcript row it belongs to.

/** Where the data came from or went. */
export type FlowLane = 'team-context' | 'ledger' | 'murmurs' | 'code' | 'cloud'

/** `in`: SageOx → session (filled dots). `out`: session → SageOx (hollow rings). */
export type FlowDirection = 'in' | 'out'

export type FlowEvent = {
  /** The tool_use_id of the call, or `att:<message id>` for a prompt's attachments. */
  id: string
  lane: FlowLane
  direction: FlowDirection
  /** oxactivity wording, past tense: "loaded team context", "searched team", "posted murmur". */
  label: string
  /** Present participle + object while open: "searching team discussions". */
  step: string
  /** Estimated tokens moved (bytes/4, or the prime's context-budget total). */
  tokens: number
  /** An `out` call whose payload the call did not hold (a `--file`, a session upload): its size is unknown, `tokens` is 0. */
  unsized?: boolean
  /** Items moved, when known ("5 discussions"): count and its noun. */
  items?: number
  itemsNoun?: string
  /** ms since the epoch, from $.clock.now(). */
  startedAt: number
  endedAt?: number
  failed?: boolean
  interrupted?: boolean
}

/** Every event of the session, newest last; capped at 60. */
export type FlowState = {
  byId: Record<string, FlowEvent>
  order: string[]
}

declare module 'claude-code' {
  interface PluginState {
    'ox-cli-brief-pane': {
      report: Shaped<UsageReport | null>
      status: Shaped<UsageStatus>
      flow: Shaped<FlowState>
    }
  }
}
