import { expect, mock, test } from 'claude-code/testing'

import { pushedRef } from './register'

const pushedAt = Date.parse('2026-10-05T10:00:00Z')

function run(status: string, failed = false) {
  return {
    ID: 'b009869eb223f0e9898fbeac6841f4f0',
    Ref: 'feat',
    SHA: '123e34f4ba3e1b19a047ab44d3a0461b7eb0bc25',
    Status: status,
    FailedBurn: failed ? 'test' : '',
    FailedStep: failed ? 'unit' : '',
    QueuedAt: '2026-10-05T10:00:02Z',
    FinishedAt: failed ? '2026-10-05T10:05:00Z' : null,
  }
}

test('pushedRef reads the destination branch of a push to oberth', () => {
  expect(pushedRef('git push oberth HEAD:refs/heads/ARG-176')).toBe('ARG-176')
  expect(pushedRef('OBERTH_SKIP="x" git push -f oberth +HEAD:refs/heads/a/b')).toBe('a/b')
  expect(pushedRef('git push oberth HEAD:refs/tags/v1')).toBe('v1')
  expect(pushedRef('git push oberth HEAD')).toBeUndefined()
  expect(pushedRef('git push origin HEAD:refs/heads/x')).toBeUndefined()
})

test('a pushed run that fails raises a toast and one prompt', async ($, on) => {
  const clock = mock.clock(on, { now: pushedAt })
  let runs = [run('running')]
  const toasts: string[] = []
  const prompts: string[] = []

  on('session.start', async () => ({ cwd: '/' }))
  on('command.register', async () => ({ value: { command: 'oberth-watch' } }))
  on('tool.call', { tool: 'Bash' }, async () => ({ result: { stdout: '', stderr: '', interrupted: false } }) as never)
  on('process.run', async () => ({ value: { exitCode: 0, stdout: JSON.stringify(runs), stderr: '' } }) as never)
  on('ui.toast', async (_$, e) => {
    toasts.push(e.text)
    return { value: undefined }
  })
  on('prompt.submit', async (_$, e) => {
    prompts.push(e.text)
    return { text: e.text }
  })

  await $.session.start({} as never)
  await $.tool.call({ tool: 'Bash', command: 'git push oberth HEAD:refs/heads/feat' } as never)

  await clock.advance(20_000)
  expect(prompts).toEqual([])

  runs = [run('failed', true)]
  await clock.advance(20_000)
  expect(toasts).toEqual(['oberth feat 123e34f: failed at test/unit'])
  expect(prompts.length).toBe(1)
  expect(prompts[0]).toContain('finished: failed at test/unit')

  await clock.advance(20_000)
  expect(prompts.length).toBe(1)
})
