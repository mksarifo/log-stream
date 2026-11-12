import { useEffect, useRef, useState } from 'react'
import { Folder, LoaderCircle, Save, Trash2, X } from 'lucide-react'
import { api, type Host, type ServerGroup } from './types'

export default function GroupDialog({ group, hosts, onClose, onSaved, onDeleted }: { group?: ServerGroup; hosts: Host[]; onClose: () => void; onSaved: (group: ServerGroup) => Promise<void>; onDeleted: () => Promise<void> }) {
  const dialog = useRef<HTMLDialogElement>(null)
  const [name, setName] = useState(group?.name ?? '')
  const [members, setMembers] = useState(new Set(group?.hostIds ?? []))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const savedHosts = hosts.filter(host => !host.id.startsWith('demo-'))
  useEffect(() => { const element = dialog.current; element?.showModal(); element?.querySelector<HTMLInputElement>('input')?.focus(); return () => element?.close() }, [])
  const submit = async (event: React.FormEvent) => {
    event.preventDefault(); setBusy(true); setError('')
    try {
      const result = await api<ServerGroup>(group ? `/groups/${group.id}` : '/groups', group ? 'PUT' : 'POST', { name, hostIds: savedHosts.filter(host => members.has(host.id)).map(host => host.id) })
      await onSaved(result); onClose()
    } catch (error) { setError((error as Error).message) } finally { setBusy(false) }
  }
  const remove = async () => {
    if (!group || !window.confirm(`Delete group “${group.name}”? Its servers and streams will be kept.`)) return
    setBusy(true); setError('')
    try { await api(`/groups/${group.id}`, 'DELETE', {}); await onDeleted(); onClose() }
    catch (error) { setError((error as Error).message) } finally { setBusy(false) }
  }
  return <dialog ref={dialog} className="host-dialog group-dialog" aria-labelledby="group-dialog-title" onCancel={event => { if (busy) event.preventDefault(); else onClose() }} onClick={event => { if (event.target === dialog.current && !busy) onClose() }}><form onSubmit={submit}>
    <div className="dialog-heading"><div className="dialog-icon"><Folder size={20} /></div><button type="button" className="icon-button" aria-label="Close group editor" disabled={busy} onClick={onClose}><X size={19} /></button></div>
    <h2 id="group-dialog-title">{group ? 'Edit server group' : 'Create server group'}</h2><p className="dialog-intro">Organize servers and view their logs together.</p>
    <fieldset className="dialog-fields" disabled={busy}>
      <label>Group name<input required maxLength={80} placeholder="Production" value={name} onChange={event => setName(event.target.value)} /></label>
      <div className="group-members-heading"><span>Servers</span><span>{savedHosts.filter(host => members.has(host.id)).length} selected</span></div>
      <div className="group-members">{savedHosts.map(host => <label className="checkbox-label group-member" key={host.id}><input type="checkbox" checked={members.has(host.id)} onChange={event => setMembers(previous => { const next = new Set(previous); if (event.target.checked) next.add(host.id); else next.delete(host.id); return next })} /><span><strong>{host.name}</strong><small>{host.address}:{host.port}</small></span></label>)}{!savedHosts.length && <p className="form-note">No saved servers yet. Create this group, then add servers after connecting a host.</p>}</div>
      <p className="form-note">Servers can belong to multiple groups. Membership changes apply immediately while streams continue.</p>
    </fieldset>
    {error && <div className="form-error" role="alert">{error}</div>}
    <div className="dialog-footer">{group ? <button type="button" className="text-button delete-group" disabled={busy} onClick={() => void remove()}><Trash2 size={13} />Delete group</button> : <span>Saved in this workspace</span>}<button type="submit" className="button primary" disabled={busy}>{busy ? <LoaderCircle size={15} className="spin" /> : <Save size={15} />}{busy ? 'Saving…' : group ? 'Save group' : 'Create group'}</button></div>
  </form></dialog>
}
