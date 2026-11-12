import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { Activity, ArrowDown, Check, ChevronDown, ChevronRight, CircleHelp, Copy, Download, FileText, Filter, Folder, Layers, LoaderCircle, PanelLeftClose, PanelLeftOpen, Pause, Pencil, Play, Plus, Radio, Search, Server, ShieldCheck, SquareTerminal, Trash2, Unplug, X } from 'lucide-react'
import HostDialog from './HostDialog'
import SourceDialog from './SourceDialog'
import GroupDialog from './GroupDialog'
import ThemePicker from './ThemePicker'
import { api, mergeEvents, type Config, type Host, type LogEvent, type Source, type EditResult, type ServerGroup } from './types'

const empty: Config = { hosts: [], sources: [], groups: [], statuses: {}, demo: false, buffer: { count: 0, bytes: 0, maxBytes: 16777216 } }
const time = (value: string) => new Date(value).toLocaleTimeString('en-GB', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit', fractionalSecondDigits: 3 })
const formatBytes = (value: number) => value < 1048576 ? `${(value / 1024).toFixed(0)} KB` : `${(value / 1048576).toFixed(1)} MB`
const displayMessage = (event: LogEvent) => {
  if (event.fields) return String(event.fields.message ?? event.fields.msg ?? event.message)
  return event.message.replace(/^\S+\s+(?:trace|debug|info|warn(?:ing)?|error|fatal|panic)\s+/i, '')
}

function Highlight({ text, query, regex }: { text: string; query: string; regex: boolean }) {
  if (!query || regex) return <>{text}</>
  const index = text.toLowerCase().indexOf(query.toLowerCase())
  if (index < 0) return <>{text}</>
  return <>{text.slice(0, index)}<mark>{text.slice(index, index + query.length)}</mark>{text.slice(index + query.length)}</>
}

export default function App() {
  const [sidebarCollapsed, setSidebarCollapsed] = useState(() => {
    try { return window.localStorage.getItem('logstream.sidebarCollapsed') === 'true' } catch { return false }
  })
  useEffect(() => {
    try { window.localStorage.setItem('logstream.sidebarCollapsed', String(sidebarCollapsed)) } catch { /* Layout still works when browser storage is unavailable. */ }
  }, [sidebarCollapsed])
  const [config, setConfig] = useState<Config>(empty)
  const [ready, setReady] = useState(false)
  const [events, setEvents] = useState<LogEvent[]>([])
  const [history, setHistory] = useState<LogEvent[]>([])
  const [query, setQuery] = useState('')
  const [regex, setRegex] = useState(false)
  const [regexError, setRegexError] = useState('')
  const [level, setLevel] = useState('')
  const [source, setSource] = useState('')
  const [hostFilter, setHostFilter] = useState('')
  const [groupFilter, setGroupFilter] = useState('')
  const [groupDialog, setGroupDialog] = useState<ServerGroup | 'new' | null>(null)
  const [paused, setPaused] = useState(false)
  const [frozen, setFrozen] = useState<LogEvent[]>([])
  const [follow, setFollow] = useState(true)
  const [connection, setConnection] = useState('connecting')
  const [gap, setGap] = useState('')
  const [error, setError] = useState('')
  const [dialog, setDialog] = useState<Host | 'new' | null>(null)
  const [editingHost, setEditingHost] = useState<Host | null>(null)
  const [editingSource, setEditingSource] = useState<Source | null>(null)
  const [notice, setNotice] = useState('')
  const [reconnectTarget, setReconnectTarget] = useState<Host | null>(null)
  const [selected, setSelected] = useState<LogEvent | null>(null)
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  const [copied, setCopied] = useState(false)
  const [help, setHelp] = useState(false)
  const [searching, setSearching] = useState(false)
  const [cutoff, setCutoff] = useState('')
  const [tick, setTick] = useState(Date.now())
  const viewport = useRef<HTMLDivElement>(null)
  const searchInput = useRef<HTMLInputElement>(null)
  const pending = useRef<LogEvent[]>([])
  const needsFollow = useRef(false)
  const refresh = useCallback(async () => { const data = await api<Config>('/config'); setConfig({ ...data, groups: data.groups ?? [] }) }, [])

  useEffect(() => {
    let alive = true
    const init = async () => { try { await api('/bootstrap'); if (!alive) return; await refresh(); if (alive) setReady(true) } catch (error) { if (alive) setError((error as Error).message) } }
    void init()
    return () => { alive = false }
  }, [refresh])

  useEffect(() => {
    if (!ready) return
    const stream = new EventSource('/api/events')
    stream.onopen = () => setConnection('connected')
    stream.onerror = () => setConnection('reconnecting')
    stream.addEventListener('logs', event => { const batch = JSON.parse((event as MessageEvent).data) as LogEvent[]; pending.current = mergeEvents(pending.current, batch) })
    stream.addEventListener('gap', event => setGap(JSON.parse((event as MessageEvent).data).message))
    const batch = window.setInterval(() => { if (pending.current.length) { const incoming = pending.current; pending.current = []; setEvents(previous => mergeEvents(previous, incoming)) } }, 100)
    const status = window.setInterval(() => { void refresh().catch(() => setConnection('reconnecting')); setTick(Date.now()) }, 2000)
    return () => { stream.close(); clearInterval(batch); clearInterval(status) }
  }, [ready, refresh])

  useEffect(() => {
    const key = (event: KeyboardEvent) => { if ((event.metaKey || event.ctrlKey) && event.key === 'k') { event.preventDefault(); searchInput.current?.focus() } }
    window.addEventListener('keydown', key); return () => window.removeEventListener('keydown', key)
  }, [])

  const groupMembership = JSON.stringify(config.groups.find(group => group.id === groupFilter)?.hostIds ?? [])
  useEffect(() => {
    if (!ready) return
    const controller = new AbortController()
    setHistory([])
    setRegexError('')
    let inFlight = false
    const search = async () => {
      if (inFlight || controller.signal.aborted) return
      inFlight = true
      setSearching(true)
      const params = new URLSearchParams({ q: query, regex: String(regex), level, source, hostId: hostFilter, groupId: groupFilter })
      try { const result = await api<{ events: LogEvent[] }>(`/logs?${params}`, 'GET', undefined, controller.signal); setHistory(result.events) }
      catch (error) { if ((error as Error).name !== 'AbortError') { if (regex) setRegexError((error as Error).message); else setError((error as Error).message) } }
      finally { inFlight = false; if (!controller.signal.aborted) setSearching(false) }
    }
    const timer = window.setTimeout(() => void search(), 220)
    // Regex runs only in Go's bounded-time RE2 engine, never in the browser.
    const poll = regex && query ? window.setInterval(() => void search(), 750) : undefined
    return () => { clearTimeout(timer); clearInterval(poll); controller.abort() }
  }, [ready, query, regex, level, source, hostFilter, groupFilter, groupMembership])

  const currentGroup = config.groups.find(group => group.id === groupFilter)
  const groupHostIds = useMemo(() => new Set(currentGroup?.hostIds ?? []), [currentGroup])
  const visibleHosts = groupFilter ? config.hosts.filter(host => groupHostIds.has(host.id)) : config.hosts
  const visibleSources = groupFilter ? config.sources.filter(item => groupHostIds.has(item.hostId)) : config.sources
  const matches = useCallback((event: LogEvent) => {
    if (cutoff && Date.parse(event.receivedAt) <= Date.parse(cutoff)) return false
    if (source && event.sourceId !== source) return false
    if (hostFilter && config.sources.find(item => item.id === event.sourceId)?.hostId !== hostFilter) return false
    if (groupFilter && !groupHostIds.has(config.sources.find(item => item.id === event.sourceId)?.hostId ?? '')) return false
    if (level && event.level !== level) return false
    if (query && !regex && !event.message.toLowerCase().includes(query.toLowerCase())) return false
    return true
  }, [cutoff, source, hostFilter, groupFilter, groupHostIds, level, query, regex, config.sources])
  const candidates = useMemo(() => regex && query ? history : mergeEvents(history, events.filter(matches)), [regex, query, history, events, matches])
  const filtered = useMemo(() => {
    const regexMatches = regex && query ? new Set(history.map(event => event.id)) : null
    return (paused ? frozen : candidates).filter(event => matches(event) && (!regexMatches || regexMatches.has(event.id)))
  }, [paused, frozen, candidates, matches, regex, query, history])

  const virtualizer = useVirtualizer({ count: filtered.length, getScrollElement: () => viewport.current, estimateSize: () => 36, overscan: 15 })
  useEffect(() => { if (follow && !paused && filtered.length && viewport.current) { viewport.current.scrollTop = viewport.current.scrollHeight; needsFollow.current = true } }, [filtered.length, events, history, follow, paused])

  const active = config.sources.filter(item => config.statuses[item.id]?.state === 'streaming').length
  const errors = events.filter(event => event.level === 'error').length
  const warnings = events.filter(event => event.level === 'warn').length
  const rate = events.filter(event => Date.parse(event.receivedAt) > tick - 10000).length / 10
  const bars = useMemo(() => Array.from({ length: 48 }, (_, i) => events.filter(event => { const age = tick - Date.parse(event.receivedAt); return age >= (47 - i) * 2000 && age < (48 - i) * 2000 }).length), [events, tick])
  const togglePause = () => { if (!paused) setFrozen(candidates); setPaused(!paused) }
  const act = async (action: () => Promise<unknown>) => { setBusy(true); setError(''); try { await action(); await refresh() } catch (error) { setError((error as Error).message) } finally { setBusy(false) } }
  const demo = () => act(async () => { if (config.demo) { setSource(''); setHostFilter('') } await api('/demo', 'POST', { enabled: !config.demo }) })
  const exportLogs = () => {
    const blob = new Blob([filtered.map(event => JSON.stringify(event)).join('\n') + '\n'], { type: 'application/x-ndjson' })
    const url = URL.createObjectURL(blob); const link = document.createElement('a'); link.href = url; link.download = `logstream-${new Date().toISOString().replace(/[:.]/g, '-')}.ndjson`; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000)
  }
  const clear = () => { setCutoff(new Date().toISOString()); setSelected(null) }
  const resetFilters = () => { setQuery(''); setLevel(''); setSource(''); setHostFilter(''); setGroupFilter(''); setRegex(false); setCutoff('') }
  const currentName = source ? config.sources.find(item => item.id === source)?.path.split('/').pop() : hostFilter ? config.hosts.find(item => item.id === hostFilter)?.name : groupFilter ? currentGroup?.name ?? 'Deleted group' : 'All sources'
  const copy = async () => { try { await navigator.clipboard.writeText(selected?.message ?? ''); setCopied(true); setTimeout(() => setCopied(false), 1500) } catch { setError('Clipboard unavailable; select and copy the raw event below.') } }
  const saved = async (result?: EditResult) => {
    await refresh(); setCutoff('')
    setNotice(result ? (result.reconnectRequired ? `Settings saved for ${result.host.name}. Reconnect to apply your changes.` : 'Settings saved. No reconnect needed.') : '')
    setReconnectTarget(result?.reconnectRequired ? result.host : null)
  }
  const sourceEditHost = editingSource ? config.hosts.find(host => host.id === editingSource.hostId) : undefined
  const selectedSource = config.sources.find(item => item.id === source)

  const chooseGroup = (id: string) => { setGroupFilter(id); setSource(''); setHostFilter('') }
  const groupSaved = async (group: ServerGroup) => { await refresh(); chooseGroup(group.id); setNotice(`Group “${group.name}” saved.`); setReconnectTarget(null) }
  const groupDeleted = async () => { await refresh(); chooseGroup(''); setNotice('Group deleted. Servers and streams kept.'); setReconnectTarget(null) }

  return <div className={`app-shell${sidebarCollapsed ? ' sidebar-collapsed' : ''}`}>
    <aside id="source-sidebar" className="sidebar" aria-label="Sources and groups" hidden={sidebarCollapsed}>
      <a href="/" className="brand" aria-label="Logstream home"><span className="brand-icon"><i /><i /><i /></span>logstream<span className="version">BETA</span></a>
      <div className="sidebar-section"><span>SOURCES <b>{config.sources.length}</b></span><button className="icon-button" title="Connect a host" aria-label="Connect a host" onClick={() => setDialog('new')}><Plus size={16} /></button></div>
      <div className="host-list">
        <button className={`all-sources ${!source && !hostFilter && !groupFilter ? 'selected' : ''}`} onClick={() => chooseGroup('')}><Layers size={15} />All sources<span>{config.sources.length}</span></button>
        <div className="groups-heading"><span>SERVER GROUPS</span><button className="icon-button" aria-label="Create server group" title="Create server group" onClick={() => setGroupDialog('new')}><Plus size={14} /></button></div>
        {config.groups.map(group => <div className={`server-group-row ${groupFilter === group.id ? 'selected' : ''}`} key={group.id}><button className="group-filter" onClick={() => chooseGroup(group.id)}><Folder size={14} /><span>{group.name}</span><small>{group.hostIds.length}</small></button><button className="icon-button" aria-label={`Edit group ${group.name}`} title={`Edit group ${group.name}`} onClick={() => setGroupDialog(group)}><Pencil size={11} /></button></div>)}
        {!config.groups.length && <button className="add-source create-group-empty" onClick={() => setGroupDialog('new')}><Folder size={13} />Create your first group</button>}
        <div className="group-host-heading">{groupFilter ? currentGroup?.name ?? 'Deleted group' : 'ALL SERVERS'}<span>{visibleHosts.length}</span></div>
        {groupFilter && !visibleHosts.length && <p className="sidebar-empty">No servers in this group.{currentGroup && <button className="text-button" onClick={() => setGroupDialog(currentGroup)}>Choose servers</button>}</p>}
        {visibleHosts.map(host => {
          const sources = config.sources.filter(item => item.hostId === host.id)
          const connected = sources.some(item => config.statuses[item.id]?.state === 'streaming')
          const isDemo = host.id.startsWith('demo-')
          return <div className="host-group" key={host.id}>
            <div className={`host-title ${hostFilter === host.id ? 'selected' : ''}`}>
              <button className="collapse-button" aria-label={`${collapsed.has(host.id) ? 'Expand' : 'Collapse'} ${host.name}`} onClick={() => setCollapsed(previous => { const next = new Set(previous); if (next.has(host.id)) next.delete(host.id); else next.add(host.id); return next })}>{collapsed.has(host.id) ? <ChevronRight size={13} /> : <ChevronDown size={13} />}</button>
              <button className="host-name" onClick={() => { setHostFilter(host.id); setSource('') }}><Server size={14} /><span>{host.name}</span></button><span className={`status-dot ${connected ? 'green' : ''}`} />
            </div>
            {!collapsed.has(host.id) && <>
              <div className="host-address">{host.username}@{host.address}{isDemo && <span>DEMO</span>}</div>
              {sources.map(item => <div key={item.id} className="source-row">
                <button className={`source-item ${source === item.id ? 'selected' : ''}`} onClick={() => { setSource(item.id); setHostFilter('') }} title={`${item.path}\n${config.statuses[item.id]?.detail ?? 'Not connected'}`}><FileText size={13} /><span>{item.path.split('/').pop()}</span><span className={`status-dot small ${config.statuses[item.id]?.state === 'streaming' ? 'green' : config.statuses[item.id]?.state === 'error' ? 'red' : ''}`} /></button>
                {!isDemo && <button className="source-edit icon-button" title={`Edit source ${item.path}`} aria-label={`Edit source ${item.path} on ${host.name}`} onClick={() => setEditingSource(item)}><Pencil size={11} /></button>}
              </div>)}
              {!isDemo && <div className="host-actions">
                <button title={`Edit connection ${host.name}`} aria-label={`Edit connection ${host.name}`} onClick={() => setEditingHost(host)}><Pencil size={11} />Edit</button>
                <button title={`Duplicate connection ${host.name}`} aria-label={`Duplicate connection ${host.name}`} disabled={busy} onClick={() => void act(async () => {
                  const result = await api<{ host: Host }>(`/hosts/${host.id}/duplicate`, 'POST', {})
                  setNotice(`Created ${result.host.name}. Edit its settings or reconnect to start streaming.`); setReconnectTarget(result.host)
                })}><Copy size={12} /></button>
                <button title={`Reconnect ${host.name}`} onClick={() => setDialog(host)}>Reconnect</button>
                <button title={`Stop ${host.name}`} aria-label={`Stop ${host.name}`} disabled={busy} onClick={() => void act(() => api(`/hosts/${host.id}/stop`, 'POST', {}))}><Unplug size={12} /></button>
                <button title={`Remove ${host.name}`} aria-label={`Remove ${host.name}`} disabled={busy} onClick={() => { if (window.confirm(`Remove ${host.name} and stop its collectors?`)) void act(() => api(`/hosts/${host.id}`, 'DELETE', {})) }}><Trash2 size={12} /></button>
              </div>}
            </>}
          </div>
        })}
        {!config.hosts.length && <p className="sidebar-empty">Connect a server to start following its log files.</p>}
        <button className="add-source" onClick={() => setDialog('new')}><Plus size={14} />Connect a host</button>
      </div>
      <div className="sidebar-bottom"><div className="profile"><span className="avatar">L</span><div>Local workspace<span>Personal · on this computer</span></div><span className={`status-dot ${connection === 'connected' ? 'green' : ''}`} /></div></div>
    </aside>

    <main className="main-workspace">
      <header className="topbar"><div className="breadcrumb"><button className="icon-button sidebar-toggle" title={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'} aria-label={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'} aria-expanded={!sidebarCollapsed} aria-controls="source-sidebar" onClick={() => setSidebarCollapsed(previous => !previous)}>{sidebarCollapsed ? <PanelLeftOpen size={17} /> : <PanelLeftClose size={17} />}</button><SquareTerminal size={15} /><span>Workspace</span><span className="slash">/</span><strong>Live cockpit</strong></div><div className="topbar-right"><ThemePicker /><span className={`connection-pill ${connection === 'connected' ? '' : 'offline'}`}><span className="status-dot green" />{connection === 'connected' ? 'Backend connected' : 'Reconnecting'}</span><button className="icon-button" title="Quick guide" aria-label="Open quick guide" onClick={() => setHelp(!help)}><CircleHelp size={17} /></button></div></header>
      <div className="page-heading"><div><div className="eyebrow"><Radio size={12} />LIVE OBSERVABILITY</div><h1>Live cockpit<span className="heading-dot" /></h1></div><div className="page-heading-actions"><button className="button secondary" onClick={() => setGroupDialog('new')}><Plus size={14} />New group</button><button className="button primary" onClick={() => setDialog('new')}><Plus size={16} />Connect host</button></div></div>

      {help && <div className="guide-panel"><div><h3>From server to stream</h3><p>Connect a Linux server, inspect and verify its host key, then choose readable absolute log file paths. Search covers the recent backend buffer. Pause freezes the view; collection continues. SSH outages can leave gaps. Saved hosts need fresh credentials after a restart.</p></div><button className="icon-button" aria-label="Close guide" onClick={() => setHelp(false)}><X size={16} /></button></div>}
      {error && <div className="notice error-notice" role="alert">{error}<button className="icon-button" aria-label="Dismiss error" onClick={() => setError('')}><X size={15} /></button></div>}
      {notice && <div className="notice saved-notice" role="status"><span>{notice}</span><div>{reconnectTarget && <button className="text-button" onClick={() => { setDialog(reconnectTarget); setNotice('') }}>Reconnect</button>}<button className="icon-button" aria-label="Dismiss saved notice" onClick={() => setNotice('')}><X size={15} /></button></div></div>}
      {gap && <div className="notice gap-notice" role="status">{gap}<button className="icon-button" aria-label="Dismiss gap notice" onClick={() => setGap('')}><X size={15} /></button></div>}
      {config.demo && <div className="demo-banner"><span><span className="demo-label">DEMO MODE</span>You're viewing simulated traffic. Connect a host to stream real logs.</span><button disabled={busy} onClick={() => void demo()}>Exit demo<X size={13} /></button></div>}

      <div className="group-toolbar"><div className="select-wrap"><Folder size={13} /><select aria-label="Filter server group" value={groupFilter} onChange={event => chooseGroup(event.target.value)}><option value="">All server groups</option>{config.groups.map(group => <option key={group.id} value={group.id}>{group.name} ({group.hostIds.length})</option>)}</select><ChevronDown size={12} /></div>{currentGroup && <button className="text-button" onClick={() => setGroupDialog(currentGroup)}><Pencil size={12} />Edit group</button>}</div>
      <section className="metrics" aria-label="Stream statistics"><div className="metric"><span>Connected sources</span><div>{active}<small>/ {config.sources.length}</small><span className="metric-note"><span className="status-dot green" />{active ? 'Streaming' : 'No active streams'}</span></div></div><div className="metric"><span>Buffered events</span><div>{config.buffer.count.toLocaleString()}<span className="metric-note">{formatBytes(config.buffer.bytes)} / 16 MB</span></div></div><div className="metric"><span>Errors <span className="muted">/ warnings</span></span><div><b className={errors ? 'error-text' : ''}>{errors}</b><small>/</small><b className={warnings ? 'warn-text' : ''}>{warnings}</b><span className="metric-note">In browser buffer</span></div></div><div className="metric throughput"><div><span>Incoming logs</span><div>{rate.toFixed(1)}<small>events / s</small></div></div><svg viewBox="0 0 144 36" aria-label="Incoming event activity over the last 96 seconds">{bars.map((value, i) => <rect key={i} x={i * 3} y={36 - Math.max(2, value / Math.max(1, ...bars) * 34)} width="2" height={Math.max(2, value / Math.max(1, ...bars) * 34)} rx=".6" />)}</svg></div></section>

      <section className="stream-panel" aria-label="Live log stream">
        <div className="stream-heading"><div className="stream-title"><Activity size={16} /><h2>Log stream</h2><span>{currentName}</span></div><div className="stream-heading-actions">{selectedSource && !selectedSource.id.startsWith('demo-') && <button className="icon-button" title="Edit selected source" aria-label="Edit selected source" onClick={() => setEditingSource(selectedSource)}><Pencil size={14} /></button>}<button className={`button subtle ${paused ? 'paused' : ''}`} onClick={togglePause}>{paused ? <Play size={13} /> : <Pause size={13} />}{paused ? 'Resume' : 'Pause'}</button><span className="toolbar-separator"/><button className="icon-button" title="Export visible logs as NDJSON" aria-label="Export visible logs" disabled={!filtered.length} onClick={exportLogs}><Download size={16} /></button><button className="icon-button" title="Clear view" aria-label="Clear view" disabled={!filtered.length} onClick={clear}><Trash2 size={15} /></button></div></div>
        <div className="filter-toolbar"><div className={`search-box ${regexError ? 'invalid' : ''}`}><Search size={16} /><input ref={searchInput} aria-label="Search logs" placeholder="Search logs…" value={query} maxLength={1000} onChange={event => setQuery(event.target.value)} />{searching && <LoaderCircle size={13} className="spin" />}{query && <button className="icon-button" aria-label="Clear search" onClick={() => setQuery('')}><X size={12} /></button>}<button className={`regex-button ${regex ? 'active' : ''}`} aria-label="Toggle regular expression search" aria-pressed={regex} title="Regular expression" onClick={() => setRegex(!regex)}>.*</button><kbd>⌘ K</kbd></div><div className="select-wrap"><Filter size={13} /><select aria-label="Filter log level" value={level} onChange={event => setLevel(event.target.value)}><option value="">All levels</option><option value="error">Error</option><option value="warn">Warning</option><option value="info">Info</option><option value="debug">Debug</option></select><ChevronDown size={12} /></div><div className="select-wrap source-select"><Server size={13} /><select aria-label="Filter source" value={source} onChange={event => { setSource(event.target.value); setHostFilter('') }}><option value="">All sources</option>{visibleSources.map(item => <option key={item.id} value={item.id}>{config.hosts.find(host => host.id === item.hostId)?.name} · {item.path.split('/').pop()}</option>)}</select><ChevronDown size={12} /></div></div>
        {(query || level || source || hostFilter || groupFilter || cutoff || regexError) && <div className="filter-summary"><span>{regexError || `${filtered.length.toLocaleString()} matching events${query ? ` for “${query}”` : ''}${cutoff ? ' · view cleared' : ''}`}</span><button onClick={resetFilters}>Reset filters<X size={11} /></button></div>}
        {visibleSources.some(item => ['error', 'waiting', 'reconnecting'].includes(config.statuses[item.id]?.state)) && <div className="source-alerts">{visibleSources.filter(item => ['error', 'waiting', 'reconnecting'].includes(config.statuses[item.id]?.state)).map(item => <div key={item.id}><span className="status-dot red" /><strong>{config.hosts.find(host => host.id === item.hostId)?.name} · {item.path.split('/').pop()}</strong>{config.statuses[item.id]?.detail}</div>)}</div>}
        <div className="log-columns"><span>TIME <ChevronDown size={10} /></span><span>LEVEL</span><span>HOST</span><span>MESSAGE</span><span /></div>
        <div className="log-viewport" ref={viewport} onScroll={() => { if (needsFollow.current) { needsFollow.current = false; return } const element = viewport.current; if (element && element.scrollHeight - element.scrollTop - element.clientHeight > 80) setFollow(false) }}>
          {filtered.length > 0 ? <div className="virtual-content" style={{ height: `${virtualizer.getTotalSize()}px` }}>{virtualizer.getVirtualItems().map(row => { const event = filtered[row.index]; return <button key={event.id} className={`log-row ${selected?.id === event.id ? 'selected' : ''} level-${event.level}`} style={{ transform: `translateY(${row.start}px)` }} onClick={() => { setSelected(event); setCopied(false) }}><span className="log-time">{time(event.receivedAt)}</span><span><span className={`level-label ${event.level}`}>{event.level.toUpperCase()}</span></span><span className="log-host" title={event.path}>{event.host}</span><span className="log-message"><Highlight text={displayMessage(event)} query={query} regex={regex} /></span><ChevronRight className="row-arrow" size={13} /></button> })}</div> : <div className="empty-state"><div className="empty-terminal">{!ready ? <LoaderCircle className="spin" size={26} /> : <SquareTerminal size={28} />}</div><h3>{!ready ? 'Connecting to your workspace' : config.sources.length ? (query || level || source || hostFilter || groupFilter ? 'No matching events' : 'Waiting for logs') : 'Your stream starts here'}</h3><p>{config.sources.length ? 'New matching events will appear here as they arrive.' : 'Connect your first server, or explore the cockpit with simulated logs.'}</p>{!config.sources.length && ready && <div><button className="button primary" onClick={() => setDialog('new')}><Plus size={15} />Connect a host</button><button className="button secondary" disabled={busy} onClick={() => void demo()}><Play size={14} />Try demo</button></div>}{(query || level || source || hostFilter || groupFilter || cutoff) && <button className="text-button" onClick={resetFilters}>Reset filters</button>}</div>}
        </div>
        <div className="stream-footer"><span><span className={`status-dot ${paused ? 'amber' : connection === 'connected' ? 'green' : ''}`} />{paused ? 'Display paused · collecting continues' : connection === 'connected' ? 'Live stream' : 'Reconnecting to backend'}<span className="footer-divider">·</span>{filtered.length.toLocaleString()} events in view</span><button className={follow && !paused ? 'following' : ''} onClick={() => { setFollow(!follow); if (paused) setPaused(false) }}><ArrowDown size={12} />{follow ? 'Auto-scroll on' : 'Auto-scroll off'}</button></div>
        {!follow && filtered.length > 0 && <button className="jump-latest button" onClick={() => { setFollow(true); setPaused(false) }}><ArrowDown size={13} />Jump to latest</button>}
      </section>
      <footer className="page-footer"><span><ShieldCheck size={12} />Local workspace · SSH transport</span></footer>
    </main>

    {selected && <aside className="event-inspector"><div className="inspector-header"><span><FileText size={16} />Event details</span><button className="icon-button" aria-label="Close event details" onClick={() => setSelected(null)}><X size={18} /></button></div><div className="inspector-content"><span className={`level-label ${selected.level}`}>{selected.level.toUpperCase()}</span><h3>{displayMessage(selected)}</h3><dl><dt>Host</dt><dd>{selected.host}</dd><dt>Source file</dt><dd>{selected.path}</dd><dt>Received at</dt><dd>{new Date(selected.receivedAt).toISOString()}</dd>{selected.timestamp && <><dt>Log timestamp</dt><dd>{selected.timestamp}</dd></>}<dt>Event ID</dt><dd>{selected.id}</dd></dl>{selected.fields && <><div className="inspector-label">STRUCTURED FIELDS</div><pre>{JSON.stringify(selected.fields, null, 2)}</pre></>}<div className="inspector-label raw-label">RAW EVENT<button className="text-button" onClick={() => void copy()}>{copied ? <Check size={13} /> : <Copy size={13} />}{copied ? 'Copied' : 'Copy'}</button></div><pre>{selected.message}</pre></div></aside>}
    {(dialog || editingHost) && <HostDialog key={editingHost ? `edit-${editingHost.id}` : dialog === 'new' ? 'new' : dialog?.id} host={editingHost ?? (dialog === 'new' ? undefined : dialog ?? undefined)} editing={Boolean(editingHost)} onClose={() => { setDialog(null); setEditingHost(null) }} onSaved={saved} />}
    {editingSource && sourceEditHost && <SourceDialog key={editingSource.id} source={editingSource} host={sourceEditHost} onClose={() => setEditingSource(null)} onSaved={saved} onEditHost={() => { setEditingSource(null); setEditingHost(sourceEditHost) }} />}
    {groupDialog && <GroupDialog key={groupDialog === 'new' ? 'new-group' : groupDialog.id} group={groupDialog === 'new' ? undefined : groupDialog} hosts={config.hosts} onClose={() => setGroupDialog(null)} onSaved={groupSaved} onDeleted={groupDeleted} />}
    <button className="mobile-connect" aria-label="Connect a host" onClick={() => setDialog('new')}><Plus size={20} /></button>
  </div>
}
