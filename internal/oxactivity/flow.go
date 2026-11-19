package oxactivity

import "strings"

// Direction is which way an ox call moved team data: into the AI coworker's
// session, or out of it to SageOx.
type Direction string

const (
	// DirectionIn is context arriving: a search, a read, a loaded plan.
	DirectionIn Direction = "in"
	// DirectionOut is something sent back: a murmur, a saved plan, an upload.
	DirectionOut Direction = "out"
)

// Flow is how an ox call moves team data, for the flow lane: its direction
// and the words shown while it is still running ("searching team
// discussions"). The finished wording is the Activity's Label.
type Flow struct {
	Direction Direction
	Step      string
}

// flows maps an Activity.Command to its flow. It mirrors the rule table of
// the Claude Code plugin (internal/usagepane/mod/hooks/flow.ts, RULES and
// MCP_TOOLS) so a lane reads the same in the plugin, in `ox statusline
// --watch` and in the turn-end notice. A command absent here moved no team
// data (doctor, status, session report, config) and draws no lane.
var flows = map[string]Flow{
	"agent prime":                 {DirectionIn, "loading team context"},
	"agent team-ctx":              {DirectionIn, "loading team context"},
	"agent session stop":          {DirectionOut, "uploading session"},
	"agent session context-trace": {DirectionOut, "recording context use"},
	"agent context used":          {DirectionOut, "recording context use"},
	"agent session score":         {DirectionOut, "scoring session"},

	"murmur list": {DirectionIn, "reading murmurs"},
	"murmur":      {DirectionOut, "posting murmur"},

	"plan save":   {DirectionOut, "saving plan"},
	"plan render": {DirectionOut, "rendering plan"},
	"plan view":   {DirectionIn, "pulling plan"},
	"plan show":   {DirectionIn, "pulling plan"},
	"plan get":    {DirectionIn, "pulling plan"},
	"plan list":   {DirectionIn, "listing plans"},
	"plan enrich": {DirectionIn, "enriching plan"},

	"query": {DirectionIn, "searching team discussions"},

	"code search":   {DirectionIn, "searching code"},
	"code defs":     {DirectionIn, "searching code"},
	"code callers":  {DirectionIn, "searching code"},
	"code callees":  {DirectionIn, "searching code"},
	"code refs":     {DirectionIn, "searching code"},
	"code log":      {DirectionIn, "searching code"},
	"code insights": {DirectionIn, "checking code hotspots"},
	"code prs":      {DirectionIn, "checking pull requests"},
	"code activity": {DirectionIn, "checking repo activity"},

	"session list":  {DirectionIn, "reading sessions"},
	"session view":  {DirectionIn, "reading sessions"},
	"session show":  {DirectionIn, "reading sessions"},
	"session score": {DirectionOut, "scoring session"},

	"conversation show":        {DirectionIn, "reading discussion"},
	"conversation search":      {DirectionIn, "reading discussion"},
	"conversation transcript":  {DirectionIn, "reading discussion"},
	"conversation topics":      {DirectionIn, "reading discussion"},
	"conversation topic":       {DirectionIn, "reading discussion"},
	"conversation walkthrough": {DirectionIn, "reading discussion"},
	"conversation list":        {DirectionIn, "listing discussions"},

	"decision enrich": {DirectionIn, "checking decisions"},
	"coworker load":   {DirectionIn, "loading coworker"},
	"kb":              {DirectionIn, "reading knowledge base"},
	"recap":           {DirectionIn, "reading recap"},

	"mcp Prime":                  {DirectionIn, "loading team context"},
	"mcp Search":                 {DirectionIn, "searching team discussions"},
	"mcp ListMurmurs":            {DirectionIn, "reading murmurs"},
	"mcp AttentionTray":          {DirectionIn, "checking attention tray"},
	"mcp SessionContext":         {DirectionIn, "reading session context"},
	"mcp GetPlan":                {DirectionIn, "pulling plan"},
	"mcp Conversation":           {DirectionIn, "reading discussion"},
	"mcp ConversationDigest":     {DirectionIn, "reading discussion"},
	"mcp ConversationTranscript": {DirectionIn, "reading discussion"},
	"mcp ExpertiseMap":           {DirectionIn, "reading expertise map"},
	"mcp OrgMemory":              {DirectionIn, "reading org memory"},
	"mcp Provenance":             {DirectionIn, "tracing provenance"},
	"mcp ListBubbles":            {DirectionIn, "listing knowledge bubbles"},
	"mcp ListBubbleFiles":        {DirectionIn, "listing bubble files"},
	"mcp ReadBubbleFile":         {DirectionIn, "reading bubble file"},
	"mcp ListRecordings":         {DirectionIn, "listing recordings"},
	"mcp RecordingChapters":      {DirectionIn, "reading recording"},
	"mcp RecordingDecisions":     {DirectionIn, "reading recording"},
	"mcp RecordingInsights":      {DirectionIn, "reading recording"},
	"mcp ListTeams":              {DirectionIn, "listing teams"},
	"mcp ListPlanTargets":        {DirectionIn, "listing plan targets"},
	"mcp GetTeamSkill":           {DirectionIn, "loading team skill"},
	"mcp GetStarted":             {DirectionIn, "reading getting started"},
	"mcp Help":                   {DirectionIn, "reading SageOx help"},
	"mcp EnrichPlan":             {DirectionIn, "enriching plan"},

	"mcp PublishMurmur":            {DirectionOut, "posting murmur"},
	"mcp SavePlan":                 {DirectionOut, "saving plan"},
	"mcp RevisePlan":               {DirectionOut, "saving plan"},
	"mcp SaveSessionSummary":       {DirectionOut, "saving session summary"},
	"mcp CaptureTurns":             {DirectionOut, "uploading session"},
	"mcp KeepDecision":             {DirectionOut, "keeping decision"},
	"mcp UndoKeptDecision":         {DirectionOut, "undoing kept decision"},
	"mcp ReportBulletinUsefulness": {DirectionOut, "reporting bulletin usefulness"},
}

// FlowOf reports how the ox call named by an Activity.Command (or a trace
// event's Command) moves team data. It reports false for a call that moves
// none.
func FlowOf(command string) (Flow, bool) {
	command = strings.TrimSpace(command)
	if f, ok := flows[command]; ok {
		return f, true
	}
	if strings.HasPrefix(command, "mcp Conversation") {
		return Flow{DirectionIn, "reading discussion"}, true
	}
	return Flow{}, false
}
