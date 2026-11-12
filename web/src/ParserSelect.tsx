import type { ParserMode } from './types'

export default function ParserSelect({ value, onChange, disabled = false }: { value: ParserMode; onChange: (value: ParserMode) => void; disabled?: boolean }) {
  return <div className="parser-setting"><label>Log parser<select aria-label="Log parser" disabled={disabled} value={value} onChange={event => onChange(event.target.value as ParserMode)}><option value="regular">Regular logs · text / JSON</option><option value="tx">TX application · workers / transactions</option></select></label><p className="form-note">{value === 'tx' ? 'Groups worker payloads and transaction blocks. Extracts transaction IDs, threads, status, and timings. Original log text is kept.' : 'Reads each line as a separate event, using the existing text and JSON parser.'}</p></div>
}
