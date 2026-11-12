export function confirmDiscardChanges(dirty: boolean): boolean {
  return !dirty || window.confirm('Discard unsaved changes?')
}
