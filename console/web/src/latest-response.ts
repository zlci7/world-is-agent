/** Coordinates every request that can replace the Runtime status snapshot. */
export function createLatestResponseGate() {
  let latest = 0
  let activeMutation: number | null = null

  return {
    begin(): number {
      latest += 1
      return latest
    },
    isLatest(request: number): boolean {
      return request === latest
    },
    beginMutation(): number {
      latest += 1
      activeMutation = latest
      return latest
    },
    finishMutation(request: number): boolean {
      if (request !== activeMutation) return false
      activeMutation = null
      return request === latest
    },
    hasActiveMutation(): boolean {
      return activeMutation !== null
    },
  }
}

export interface ContextRequest {
  sequence: number
  context: string
}

/** Keeps a response bound to the game that requested it. A sequence check alone
 * cannot reject a response if the UI changes games before the next poll begins. */
export function createContextResponseGate() {
  let latest = 0

  return {
    begin(context: string): ContextRequest {
      latest += 1
      return { sequence: latest, context }
    },
    accept(request: ContextRequest, currentContext: string): boolean {
      return request.sequence === latest && request.context === currentContext
    },
    invalidate(): void {
      latest += 1
    },
  }
}

/** A failed status read makes the previous status and its turns untrustworthy. */
export function disconnectedConsoleState<TTurn>() {
  return { status: null, turns: [] as TTurn[] }
}
