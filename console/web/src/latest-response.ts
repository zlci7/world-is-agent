/** Coordinates every request that can replace the Runtime status snapshot. */
export function createLatestResponseGate() {
  let latest = 0

  return {
    begin(): number {
      latest += 1
      return latest
    },
    isLatest(request: number): boolean {
      return request === latest
    },
  }
}
