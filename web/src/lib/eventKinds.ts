/**
 * Icon + label metadata for every `Kind` (SPEC §1.5.1) — the Argus-normalised,
 * closed vocabulary that Argus itself computes (as opposed to `decision`,
 * `decision_source`, `tool_source`, `query_source`, `permission_mode`, which
 * are vendor-supplied free-form strings and must never be switched over —
 * SPEC §4.4/§6.1, see `RawValue.vue`).
 *
 * `Record<Kind, EventKindMeta>` makes a missing `Kind` a **compile error**:
 * the PLAN.md P4-04 AC ("an icon/label for every Kind including the three
 * hook.* kinds and unknown") becomes structural rather than a test that can
 * rot when `Kind` grows. `eventKinds.test.ts` additionally asserts every
 * entry has a non-empty label at runtime, because a `Record` can still be
 * satisfied by a placeholder (e.g. `label: ''`) without failing the
 * compiler.
 */
import {
  AlertCircle,
  AlertTriangle,
  Ban,
  Bell,
  BellRing,
  Bot,
  Braces,
  CheckCircle,
  ClipboardCheck,
  ClipboardList,
  Cpu,
  FileEdit,
  FileText,
  FolderMinus,
  FolderOpen,
  FolderPlus,
  FolderTree,
  GitBranch,
  HelpCircle,
  Layers,
  ListTree,
  LogIn,
  MessageSquare,
  Play,
  Plug,
  Puzzle,
  Send,
  Settings,
  ShieldAlert,
  ShieldQuestion,
  Square,
  UserCheck,
  Wrench,
  XCircle,
  Zap,
  type LucideIcon,
} from '@lucide/vue'

import type { components } from '@/api/schema'

export type Kind = components['schemas']['Kind']

export interface EventKindMeta {
  /** Short, human label for chips/rows — never blank (enforced by eventKinds.test.ts). */
  label: string
  /** lucide-vue icon component. */
  icon: LucideIcon
}

export const EVENT_KIND_META: Record<Kind, EventKindMeta> = {
  'session.start': { label: 'Session start', icon: Play },
  'session.end': { label: 'Session end', icon: Square },
  'turn.start': { label: 'Turn start', icon: MessageSquare },
  'turn.end': { label: 'Turn end', icon: MessageSquare },
  'turn.prompt_expanded': { label: 'Prompt expanded', icon: FileText },
  'llm.request': { label: 'LLM request', icon: Send },
  'llm.error': { label: 'LLM error', icon: AlertCircle },
  'llm.refusal': { label: 'LLM refusal', icon: Ban },
  'llm.request_body': { label: 'LLM request body', icon: Braces },
  'llm.response_body': { label: 'LLM response body', icon: Braces },
  'assistant.message': { label: 'Assistant message', icon: MessageSquare },
  'tool.pre': { label: 'Tool pre-call', icon: Wrench },
  'tool.decision': { label: 'Tool decision', icon: ShieldQuestion },
  'tool.permission_request': { label: 'Permission request', icon: ShieldAlert },
  'tool.result': { label: 'Tool result', icon: CheckCircle },
  'tool.batch': { label: 'Tool batch', icon: Layers },
  'subagent.start': { label: 'Subagent start', icon: Bot },
  'subagent.stop': { label: 'Subagent stop', icon: Bot },
  'task.created': { label: 'Task created', icon: ClipboardList },
  'task.completed': { label: 'Task completed', icon: ClipboardCheck },
  'permission.mode_changed': { label: 'Permission mode changed', icon: Settings },
  'hook.registered': { label: 'Hook registered', icon: Plug },
  'hook.execution_start': { label: 'Hook execution start', icon: Zap },
  'hook.execution_end': { label: 'Hook execution end', icon: Zap },
  'fs.file_changed': { label: 'File changed', icon: FileEdit },
  'workspace.cwd_changed': { label: 'Working dir changed', icon: FolderOpen },
  'workspace.directory_added': { label: 'Directory added', icon: FolderPlus },
  'workspace.config_changed': { label: 'Workspace config changed', icon: Settings },
  'workspace.instructions_loaded': { label: 'Instructions loaded', icon: FileText },
  'workspace.worktree_created': { label: 'Worktree created', icon: GitBranch },
  'workspace.worktree_removed': { label: 'Worktree removed', icon: FolderMinus },
  'context.compact_start': { label: 'Context compact start', icon: FolderTree },
  'context.compact_end': { label: 'Context compact end', icon: FolderTree },
  'mcp.connection': { label: 'MCP connection', icon: Plug },
  'mcp.elicitation': { label: 'MCP elicitation', icon: HelpCircle },
  'mcp.elicitation_result': { label: 'MCP elicitation result', icon: HelpCircle },
  'agent.auth': { label: 'Agent auth', icon: LogIn },
  'agent.setup': { label: 'Agent setup', icon: Cpu },
  'agent.plugin': { label: 'Agent plugin', icon: Puzzle },
  'agent.internal_error': { label: 'Agent internal error', icon: AlertTriangle },
  'agent.notification': { label: 'Agent notification', icon: BellRing },
  'agent.idle': { label: 'Agent idle', icon: Bell },
  'unknown': { label: 'Unknown', icon: XCircle },
}

/** Stable list of every `Kind`, in `EVENT_KIND_META` declaration order — the filter-chip source of truth. */
export const ALL_KINDS = Object.keys(EVENT_KIND_META) as Kind[]

export function eventKindMeta(kind: Kind): EventKindMeta {
  return EVENT_KIND_META[kind]
}

/** Icon shown for `tool.pre`-derived "no turn" grouping and generic fallbacks; kept separate so callers don't reach for `ListTree` by accident. */
export const NO_TURN_ICON = ListTree
export const UNKNOWN_DECISION_SOURCE_ICON = UserCheck
