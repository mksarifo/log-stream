import { useEffect, useRef, useState } from 'react'
import { Check, FileText, LoaderCircle, Save, X } from 'lucide-react'
import { api, type EditResult, type Host, type Source, type ParserMode } from './types'
import ParserSelect from './ParserSelect'
import { confirmDiscardChanges } from './dialogs'

export default function SourceDialog({ source, host, onClose, onSaved, onEditHost }: { source: Source; host: Host; onClose: () => void; onSaved: (result: EditResult) => Promise<void>; onEditHost: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null)
  const [path, setPath] = useState(source.path)
  const [parser, setParser] = useState<ParserMode>(source.parser || 'regular')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const dirty = path !== source.path || parser !== (source.parser || 'regular')
  const close = () => { if (!busy && confirmDiscardChanges(dirty)) onClose() }
  useEffect(() => { const element = dialog.current; element?.showModal(); element?.querySelector<HTMLInputElement>('input')?.focus(); return () => element?.close() }, [])
  const submit = async (event: React.FormEvent) => {
    event.preventDefault(); setBusy(true); setError('')
    try { const result = await api<EditResult>(`/sources/${source.id}`, 'PUT', { path, parser }); await onSaved(result); onClose() }
    catch (error) { setError((error as Error).message) }
    finally { setBusy(false) }
  }
  return <dialog ref={dialog} className="host-dialog" aria-labelledby="source-dialog-title" onCancel={event => { event.preventDefault(); close() }}><form onSubmit={submit}>
    <div className="dialog-heading"><div className="dialog-icon"><FileText size={20} /></div><button type="button" className="icon-button" aria-label="Close source editor" disabled={busy} onClick={close}><X size={19} /></button></div>
    <h2 id="source-dialog-title">Edit source</h2><p className="muted dialog-intro">Choose the log file and parser for {host.name}.</p>
    <div className="source-host-context"><span>{host.name}</span><button type="button" className="text-button" disabled={busy} onClick={() => { if (confirmDiscardChanges(dirty)) onEditHost() }}>Edit connection</button></div>
    <label>Log file path<input required maxLength={4096} disabled={busy} value={path} onChange={event => setPath(event.target.value)} spellCheck={false} /></label>
    <p className="form-note">Use a unique absolute file path on this host.</p>
    <ParserSelect value={parser} onChange={setParser} disabled={busy} />
    <p className="edit-impact">Changing the path or parser stops collection for this host. Reconnect to apply it. Other source settings and buffered logs are kept.</p>
    {error && <div className="form-error" role="alert">{error}</div>}
    <div className="dialog-footer"><span><Check size={13} />No credentials required to save</span><button type="submit" className="button primary" disabled={busy}>{busy ? <LoaderCircle size={15} className="spin" /> : <Save size={15} />}{busy ? 'Saving…' : 'Save changes'}</button></div>
  </form></dialog>
}
