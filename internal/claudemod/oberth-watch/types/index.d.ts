export type Watched = {
  ref: string
  pushedAt: number
  runId?: string
  sha?: string
  status?: string
  failedAt?: string
  finishedAt?: number
}

declare module 'claude-code' {
  interface PluginState {
    'oberth-watch': { watched: Watched[]; isReachable: boolean }
  }
}
