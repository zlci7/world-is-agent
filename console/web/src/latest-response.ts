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
