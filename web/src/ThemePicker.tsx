import { useState } from 'react'
import { Palette, ChevronDown } from 'lucide-react'
import { applyTheme, savedTheme, themes, type Theme } from './themes'

export default function ThemePicker() {
  const [theme, setTheme] = useState(savedTheme)
  return <div className="theme-picker" title="Color theme"><Palette size={14} /><select aria-label="Color theme" value={theme} onChange={event => {
    const value = event.target.value as Theme
    setTheme(value); applyTheme(value)
    try { localStorage.setItem('logstream.theme', value) } catch { /* Theme changes still work without browser storage. */ }
  }}>{themes.map(item => <option value={item.id} key={item.id}>{item.name}</option>)}</select><ChevronDown size={11} /></div>
}
