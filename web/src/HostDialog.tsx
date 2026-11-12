import { useEffect, useRef, useState } from 'react'
import { ArrowRight, Check, Eye, FileKey2, Fingerprint, KeyRound, LoaderCircle, Save, Server, X } from 'lucide-react'
import { api, type Credentials, type Host, type EditResult, type ParserMode } from './types'
import ParserSelect from './ParserSelect'
import { confirmDiscardChanges } from './dialogs'

export default function HostDialog({ host, editing = false, onClose, onSaved }: { host?: Host; editing?: boolean; onClose: () => void; onSaved: (result?: EditResult) => void | Promise<void> }) {
  const dialog = useRef<HTMLDialogElement>(null)
  const [name, setName] = useState(host?.name ?? '')
  const [address, setAddress] = useState(host?.address ?? '')
  const [port, setPort] = useState(host?.port ?? 22)
  const [username, setUsername] = useState(host?.username ?? 'deploy')
  const [paths, setPaths] = useState('/var/log/app/app.log')
  const [parser, setParser] = useState<ParserMode>('regular')
  const [credentials, setCredentials] = useState<Credentials>({ method: 'agent' })
  const [fingerprint, setFingerprint] = useState(host?.fingerprint ?? '')
  const [trusted, setTrusted] = useState(Boolean(host))
  const [busy, setBusy] = useState(false)
  const [probing, setProbing] = useState(false)
  const [error, setError] = useState('')
  const [visible, setVisible] = useState(false)
  const draft = JSON.stringify({ name, address, port, username, paths, parser, credentials, fingerprint, trusted })
  const initialDraft = useRef(draft)
  const close = () => { if (!busy && confirmDiscardChanges(draft !== initialDraft.current)) onClose() }
  const probeAbort = useRef<AbortController | null>(null)
  useEffect(() => { dialog.current?.showModal(); dialog.current?.querySelector<HTMLInputElement>('input')?.focus(); const element = dialog.current; return () => { probeAbort.current?.abort(); element?.close() } }, [])
  const resetTrust = () => { setFingerprint(''); setTrusted(false) }
  const inspect = async () => {
    setProbing(true); setError('')
    const controller = new AbortController(); probeAbort.current = controller
    try {
      const result = await api<{ fingerprint: string }>('/hosts/probe', 'POST', { id: '', name: name || address, address, port, username, fingerprint: '' }, controller.signal)
      setFingerprint(result.fingerprint); setTrusted(false)
    } catch (error) { if ((error as Error).name !== 'AbortError') setError((error as Error).message) } finally { setProbing(false) }
  }
  const submit = async (event: React.FormEvent) => {
    event.preventDefault(); setBusy(true); setError('')
    try {
      let result: EditResult | undefined
      if (editing && host) result = await api<EditResult>(`/hosts/${host.id}`, 'PUT', { host: { id: host.id, name, address, port, username, fingerprint }, trustHostKey: trusted })
      else if (host) await api(`/hosts/${host.id}/start`, 'POST', credentials)
      else await api('/hosts', 'POST', { host: { id: '', name, address, port, username, fingerprint }, paths: paths.split('\n').map(path => path.trim()).filter(Boolean), parser, credentials })
      await onSaved(result); onClose()
    } catch (error) { setError((error as Error).message) } finally { setBusy(false) }
  }
  return <dialog ref={dialog} className="host-dialog" aria-labelledby="host-dialog-title" onCancel={event => { event.preventDefault(); close() }}>
    <form onSubmit={submit}>
      <div className="dialog-heading"><div className="dialog-icon"><Server size={20} /></div><button type="button" className="icon-button" aria-label="Close dialog" disabled={busy} onClick={close}><X size={19} /></button></div>
      <h2 id="host-dialog-title">{editing ? 'Edit connection' : host ? 'Reconnect host' : 'Connect a host'}</h2>
      <p className="muted dialog-intro">{editing ? 'Update this host while keeping its log sources.' : host ? `Provide SSH credentials for ${host.name}.` : 'Stream log files securely over SSH.'}</p>
      <fieldset className="dialog-fields" disabled={busy || probing}>
      {(!host || editing) && <>
        <label>Host label<input required maxLength={80} placeholder="api-production" value={name} onChange={event => setName(event.target.value)} autoFocus /></label>
        <div className="form-row"><label>Hostname or IP<input required placeholder="10.0.1.12" value={address} onChange={event => { setAddress(event.target.value); resetTrust() }} /></label><label className="port-input">Port<input type="number" required min={1} max={65535} value={port} onChange={event => { setPort(Number(event.target.value)); resetTrust() }} /></label></div>
        <label>SSH username<input required maxLength={80} placeholder="deploy" value={username} onChange={event => setUsername(event.target.value)} /></label>
        {!editing && <label>Log file paths<span className="label-hint">One absolute path per line · up to 16</span><textarea rows={2} required value={paths} onChange={event => setPaths(event.target.value)} spellCheck={false} /></label>}
      </>}
      {!host && <ParserSelect value={parser} onChange={setParser} />}
      {!editing && <>
      <label>Authentication</label>
      <div className="auth-tabs" role="group" aria-label="Authentication method">{[{ id: 'agent', icon: KeyRound, text: 'SSH agent' }, { id: 'key', icon: FileKey2, text: 'Private key' }, { id: 'password', icon: Eye, text: 'Password' }].map(item => <button type="button" key={item.id} className={credentials.method === item.id ? 'active' : ''} onClick={() => setCredentials({ method: item.id })}><item.icon size={14} />{item.text}</button>)}</div>
      {credentials.method === 'agent' && <p className="form-note">Uses the SSH agent available to the Go backend on this computer.</p>}
      {credentials.method === 'key' && <><label className="file-label">Open private key file<input type="file" onChange={async event => { const file = event.target.files?.[0]; if (file && file.size > 65536) { setError('Private key file is too large'); return } if (file) setCredentials({ ...credentials, privateKey: await file.text() }) }} /></label><label>Private key<textarea rows={3} required placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" value={credentials.privateKey ?? ''} onChange={event => setCredentials({ ...credentials, privateKey: event.target.value })} spellCheck={false} /></label><label>Passphrase <span className="optional">optional</span><input type="password" autoComplete="off" value={credentials.passphrase ?? ''} onChange={event => setCredentials({ ...credentials, passphrase: event.target.value })} /></label></>}
      {credentials.method === 'password' && <label>Password<div className="password-field"><input type={visible ? 'text' : 'password'} autoComplete="off" required value={credentials.password ?? ''} onChange={event => setCredentials({ ...credentials, password: event.target.value })} /><button type="button" className="icon-button" aria-label="Toggle password visibility" onClick={() => setVisible(!visible)}><Eye size={16} /></button></div></label>}
      {credentials.method === 'password' && <><label>Connection reason <span className="optional">if requested by your SSH proxy</span><input maxLength={1024} autoComplete="off" placeholder="Reason for this connection" value={credentials.reason ?? ''} onChange={event => setCredentials({ ...credentials, reason: event.target.value })} /></label><p className="form-note">Supports SSH password prompts and CyberArk connection-reason prompts. The reason stays in memory for this session.</p></>}
      </>}
      {(!host || editing) && <div className="trust-section"><div className="trust-heading"><span><Fingerprint size={16} />Server identity</span><button type="button" className="text-button" disabled={!address || !username || probing} onClick={inspect}>{probing ? <><LoaderCircle size={13} className="spin" />Inspecting…</> : 'Inspect host key'}</button></div>{fingerprint ? <><code className="fingerprint">{fingerprint}</code><label className="checkbox-label"><input type="checkbox" checked={trusted} onChange={event => setTrusted(event.target.checked)} />I verified this fingerprint and trust this server.</label></> : <p className="form-note">Inspect the fingerprint, then verify it with your server administrator.</p>}</div>}
      {editing && <p className="edit-impact">Saving changed settings stops this host’s collectors. Reconnect afterward to apply them. Sources and buffered logs are kept.</p>}
      </fieldset>
      {error && <div className="form-error" role="alert">{error}</div>}
      <div className="dialog-footer"><span><Check size={13} />{editing ? 'No credentials required to save' : 'Credentials stay in memory'}</span><button type="submit" className="button primary" disabled={busy || probing || ((!host || editing) && !trusted)}>{busy ? <LoaderCircle size={15} className="spin" /> : editing ? <Save size={15} /> : <ArrowRight size={15} />}{busy ? (editing ? 'Saving…' : 'Connecting…') : editing ? 'Save changes' : 'Connect & stream'}</button></div>
    </form>
  </dialog>
}
