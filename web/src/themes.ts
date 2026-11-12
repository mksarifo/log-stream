export const themes = [
  { id: 'forest', name: 'Forest', background: '#101211' },
  { id: 'ocean', name: 'Ocean', background: '#0d141e' },
  { id: 'violet', name: 'Violet', background: '#14111c' },
  { id: 'graphite', name: 'Graphite', background: '#141414' },
  { id: 'light', name: 'Light', background: '#f5f7fa' },
] as const

export type Theme = typeof themes[number]['id']

export function savedTheme(): Theme {
  try {
    const value = localStorage.getItem('logstream.theme')
    return themes.find(theme => theme.id === value)?.id ?? 'forest'
  } catch { return 'forest' }
}

export function applyTheme(value: Theme) {
  document.documentElement.dataset.theme = value
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', themes.find(theme => theme.id === value)!.background)
}
