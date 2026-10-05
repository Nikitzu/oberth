import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { Watched } from '../types'

const watched = atom({ plugin: 'oberth-watch', key: 'watched' } as const, [])
const isReachable = atom({ plugin: 'oberth-watch', key: 'isReachable' } as const, true)

const POLL_MS = 20_000
const CLOCK_SKEW_MS = 15_000
const FINISHED_LINGER_MS = 10 * 60_000

const OBERTH = [
  '/bin/sh',
  '-c',
  'test -n "$OBERTH_BASE_URL" || . "${XDG_CONFIG_HOME:-$HOME/.config}/oberth/env" >/dev/null 2>&1; exec oberth "$@"',
  'oberth',
]

type Run = {
  ID: string
  Ref: string
  SHA: string
  Status: string
  FailedBurn: string
  FailedStep: string
  QueuedAt: string
  FinishedAt: string | null
}

const isActive = (status?: string) => status === undefined || status === 'queued' || status === 'running'

export function pushedRef(command: string): string | undefined {
  const destination = command.match(/git\s+push\s+(?:-\S+\s+)*oberth\s+\+?\S*:(\S+)/)?.[1]
  return destination?.replace(/^refs\/(heads|tags)\//, '') || undefined
}

export function reconcile(list: Watched[], runs: Run[]): Watched[] {
  const taken = new Set(list.map(one => one.runId).filter(Boolean))
  return list.map(one => {
    let run = one.runId ? runs.find(r => r.ID === one.runId) : undefined
    if (!one.runId) {
      run = runs
        .filter(r => r.Ref === one.ref && !taken.has(r.ID) && Date.parse(r.QueuedAt) >= one.pushedAt - CLOCK_SKEW_MS)
        .sort((a, b) => Date.parse(a.QueuedAt) - Date.parse(b.QueuedAt))[0]
      if (run) taken.add(run.ID)
    }
    if (!run) return one
    return {
      ...one,
      runId: run.ID,
      ref: run.Ref,
      sha: run.SHA,
      status: run.Status,
      failedAt: run.FailedBurn ? `${run.FailedBurn}/${run.FailedStep}` : undefined,
      finishedAt: run.FinishedAt ? Date.parse(run.FinishedAt) : undefined,
    }
  })
}

async function fetchRuns($: EngineInterface): Promise<Run[] | undefined> {
  try {
    const { exitCode, stdout } = await $.process.run([...OBERTH, 'runs', '-limit', '40', '-json'], { timeoutMs: 20_000 })
    return exitCode === 0 ? (JSON.parse(stdout) as Run[]) : undefined
  } catch {
    return undefined
  }
}

let isPolling = false

async function poll($: EngineInterface) {
  const before = await read($, watched)
  if (isPolling || !before.some(one => isActive(one.status))) return
  isPolling = true
  try {
    const runs = await fetchRuns($)
    await update($, isReachable, () => runs !== undefined)
    if (!runs) return

    const now = await $.clock.now()
    const after = reconcile(before, runs).filter(
      one => isActive(one.status) || now - (one.finishedAt ?? now) < FINISHED_LINGER_MS,
    )
    await update($, watched, () => after)

    for (const one of after) {
      const was = before.find(b => (b.runId ? b.runId === one.runId : b.pushedAt === one.pushedAt))
      if (!isActive(one.status) && isActive(was?.status)) await finished($, one)
    }
  } finally {
    isPolling = false
  }
}

async function finished($: EngineInterface, one: Watched) {
  const where = one.failedAt ? ` at ${one.failedAt}` : ''
  $.ui.toast(`oberth ${one.ref} ${(one.sha ?? '').slice(0, 7)}: ${one.status}${where}`)
  void $.prompt.submit({
    text:
      `Oberth run ${one.runId} for ${one.ref} (${one.sha}) finished: ${one.status}${where}.` +
      (one.status === 'failed' ? ' Triage it with `oberth run` and a patterned `oberth log`, not the whole log.' : ''),
  })
}

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    await $.command.register({
      name: 'oberth-watch',
      description: 'Watch an Oberth run by ID in the band above the prompt',
    })
    $.clock.every(POLL_MS, () => void poll($))
    return next(e)
  })

  on('command.run', { command: 'oberth-watch' }, async ($, e) => {
    const runId = e.args.trim()
    if (!/^[0-9a-f]{6,64}$/.test(runId)) return { text: 'Usage: /oberth-watch <run-id>' }
    const now = await $.clock.now()
    await update($, watched, list => [...list, { ref: '', runId, pushedAt: now }])
    void poll($)
    return { text: `Watching Oberth run ${runId}.` }
  })

  on('tool.call', { tool: 'Bash' }, async ($, e, next) => {
    const ran = await next(e)
    const ref = pushedRef(e.command)
    if (ref && !ran.deny && !ran.isError) {
      const now = await $.clock.now()
      await update($, watched, list => [...list, { ref, pushedAt: now }])
    }
    return ran
  })

  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    const list = await read($, watched)
    if (e.props.hasSurvey || list.length === 0) return next(e)

    const { Box, Text } = $.ui.resolve(e)
    const reachable = await read($, isReachable)
    const mark = (status?: string) =>
      status === 'passed'
        ? { glyph: '✓', color: 'green' }
        : status === 'failed'
          ? { glyph: '✗', color: 'red' }
          : isActive(status)
            ? { glyph: '…', color: 'yellow' }
            : { glyph: '!', color: 'red' }

    return (
      <Box flexDirection="column">
        {await next(e)}
        {list.map(one => {
          const { glyph, color } = mark(one.status)
          return (
            <Text>
              <Text color={color}>{glyph}</Text> oberth {one.ref || one.runId?.slice(0, 12)}{' '}
              <Text dimColor>
                {(one.sha ?? '').slice(0, 7)} {one.status ?? 'waiting for the run'}
                {one.failedAt ? ` at ${one.failedAt}` : ''}
              </Text>
            </Text>
          )
        })}
        {!reachable && <Text dimColor>oberth server unreachable, retrying every {POLL_MS / 1000}s</Text>}
      </Box>
    )
  })
}
