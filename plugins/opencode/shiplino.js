// shiplino-opencode-plugin v1
//
// Shiplino for OpenCode: records what OpenCode does (sessions, subagents,
// prompts, tool calls, permission prompts, token usage and cost) on the
// local Shiplino board. `shiplino setup` copies this file into OpenCode's
// global plugin folder (~/.config/opencode/plugins/) with the absolute path
// of the shiplino binary filled in below.
//
// Observe only. The plugin uses nothing but OpenCode's `event` hook, which
// can't change what the agent does: it never throws, never returns a
// value, never touches tool input or output and never prints. For each
// event worth recording it starts `shiplino hook --agent opencode`, writes
// one JSON object to its stdin and moves on without waiting. Shiplino's hook
// writes one line to a local spool file and exits; nothing leaves the
// machine. Every error is swallowed.

import { spawn } from "node:child_process"

const BIN = "__SHIPLINO_BIN__" // replaced with an absolute path on install

const MAX_TEXT = 8 * 1024 // prompts, titles, errors
const MAX_PATCH = 64 * 1024 // diffs
const MAX_STRING = 256 * 1024 // any one tool argument
const MAX_KEYS = 4096 // remembered ids per kind

function binary() {
  return BIN.startsWith("__") ? process.env.SHIPLINO_BIN || "shiplino" : BIN
}

function send(payload) {
  try {
    const child = spawn(binary(), ["hook", "--agent", "opencode"], {
      stdio: ["pipe", "ignore", "ignore"],
      detached: true,
      windowsHide: true,
    })
    child.on("error", () => {})
    child.stdin.on("error", () => {})
    child.stdin.end(JSON.stringify(payload))
    child.unref()
  } catch {}
}

// remember returns true the first time key is seen. Old keys are dropped
// once MAX_KEYS is reached, so memory stays bounded in long sessions.
function remember(map, key, value = true) {
  if (map.has(key)) return false
  map.set(key, value)
  if (map.size > MAX_KEYS) map.delete(map.keys().next().value)
  return true
}

function cap(s, n) {
  return typeof s === "string" && s.length > n ? s.slice(0, n) : s
}

function capInput(input) {
  if (!input || typeof input !== "object") return input
  const out = {}
  for (const [k, v] of Object.entries(input)) out[k] = cap(v, MAX_STRING)
  return out
}

function errorInfo(e) {
  if (!e || typeof e !== "object") return undefined
  return { name: e.name, message: cap(e.data?.message ?? e.message, MAX_TEXT) }
}

// build turns one OpenCode bus event into the payload Shiplino records, or
// undefined for events that carry nothing to record.
function build(st, event, directory) {
  if (!event || typeof event.type !== "string") return
  const p = event.properties || {}
  const base = (sessionID) => {
    if (typeof sessionID !== "string" || !sessionID) return
    const out = { session_id: sessionID, hook_event_name: event.type, timestamp: Date.now() }
    if (directory) out.cwd = directory
    const s = st.sessions.get(sessionID)
    if (s?.lineage.length) out.lineage = s.lineage
    if (s?.version) out.version = s.version
    return out
  }

  switch (event.type) {
    case "session.created":
    case "session.updated":
    case "session.deleted": {
      const info = p.info
      if (!info?.id) return
      const prev = st.sessions.get(info.id)
      const parent = info.parentID ? st.sessions.get(info.parentID) : undefined
      const lineage = info.parentID ? [...(parent?.lineage ?? []), info.parentID] : []
      st.sessions.delete(info.id)
      remember(st.sessions, info.id, { lineage, version: info.version, title: info.title })
      // session.updated fires on every change: only a new title (or a
      // session first seen here, e.g. after a restart) is worth a record.
      if (event.type === "session.updated" && prev && prev.title === info.title) return
      const out = base(info.id)
      if (info.directory) out.cwd = info.directory
      out.title = info.title
      out.parent_id = info.parentID
      out.agent = info.agent
      return out
    }

    case "message.updated": {
      const info = p.info
      if (!info?.id) return
      if (info.role === "user") {
        remember(st.users, info.id, { agent: info.agent, model: info.model })
        return
      }
      if (info.role !== "assistant" || !info.time?.completed || !remember(st.done, info.id)) return
      const out = base(info.sessionID)
      if (!out) return
      Object.assign(out, {
        message_id: info.id,
        model_id: info.modelID,
        provider_id: info.providerID,
        agent: info.agent ?? info.mode,
        tokens: info.tokens,
        cost: info.cost,
        finish: info.finish,
        error: errorInfo(info.error),
        created_at: info.time.created,
        completed_at: info.time.completed,
      })
      if (info.path?.cwd) out.cwd = info.path.cwd
      return out
    }

    case "message.part.updated": {
      const part = p.part
      if (!part?.id) return
      if (part.type === "text") {
        // The first real text of a user message is the prompt.
        const user = st.users.get(part.messageID)
        if (!user || part.synthetic || part.ignored || !remember(st.prompts, part.messageID)) return
        const out = base(part.sessionID)
        if (!out) return
        Object.assign(out, {
          part_type: "text",
          message_id: part.messageID,
          prompt: cap(part.text, MAX_TEXT),
          agent: user.agent,
          model_id: user.model?.modelID,
          provider_id: user.model?.providerID,
        })
        return out
      }
      if (part.type !== "tool") return
      const state = part.state || {}
      const phase = state.status === "running" ? "start" : state.status === "completed" || state.status === "error" ? "end" : ""
      if (!phase || !remember(st.tools, part.id + ":" + phase)) return
      const out = base(part.sessionID)
      if (!out) return
      const md = state.metadata || {}
      Object.assign(out, {
        part_type: "tool",
        message_id: part.messageID,
        call_id: part.callID,
        tool: part.tool,
        status: state.status,
        tool_input: capInput(state.input),
        title: cap(state.title, MAX_TEXT),
        started_at: state.time?.start,
        ended_at: state.time?.end,
      })
      if (phase === "end") {
        out.error = cap(state.error, MAX_TEXT)
        if (typeof md.exit === "number") out.exit_code = md.exit
        if (typeof md.sessionId === "string") out.child_session_id = md.sessionId
        if (typeof md.exists === "boolean") out.exists = md.exists
        if (md.filediff) out.file_diff = { file: md.filediff.file, additions: md.filediff.additions, deletions: md.filediff.deletions }
        if (typeof md.diff === "string" && part.tool === "edit") out.diff = cap(md.diff, MAX_PATCH)
        if (Array.isArray(md.files)) {
          out.files = md.files.map((f) => ({
            path: f.filePath,
            move_path: f.movePath,
            type: f.type,
            additions: f.additions,
            deletions: f.deletions,
          }))
          out.file_patches = md.files.map((f) => cap(f.patch, MAX_PATCH))
        }
      }
      return out
    }

    case "permission.asked":
    case "permission.updated": {
      // permission.updated is the older name, with type/pattern/callID.
      const out = base(p.sessionID)
      if (!out) return
      Object.assign(out, {
        request_id: p.id,
        permission: p.permission ?? p.type,
        patterns: p.patterns ?? (p.pattern === undefined ? undefined : [].concat(p.pattern)),
        call_id: p.tool?.callID ?? p.callID,
        message: cap(p.title, MAX_TEXT),
      })
      return out
    }

    case "permission.replied": {
      const out = base(p.sessionID)
      if (!out) return
      Object.assign(out, { request_id: p.requestID ?? p.permissionID, reply: p.reply ?? p.response })
      return out
    }

    case "question.asked": {
      const out = base(p.sessionID)
      if (!out) return
      Object.assign(out, { request_id: p.id, call_id: p.tool?.callID, message: cap(p.questions?.[0]?.question, MAX_TEXT) })
      return out
    }

    case "question.replied":
    case "question.rejected": {
      const out = base(p.sessionID)
      if (!out) return
      out.request_id = p.requestID
      return out
    }

    case "session.idle":
    case "session.compacted":
      return base(p.sessionID)

    case "session.error": {
      const out = base(p.sessionID)
      if (!out) return
      out.error = errorInfo(p.error)
      return out
    }
  }
}

export const ShiplinoPlugin = async (input) => {
  const directory = input?.directory
  const st = { sessions: new Map(), users: new Map(), done: new Map(), prompts: new Map(), tools: new Map() }
  return {
    event: async (arg) => {
      try {
        const payload = build(st, arg?.event, directory)
        if (payload) send(payload)
      } catch {}
    },
  }
}
