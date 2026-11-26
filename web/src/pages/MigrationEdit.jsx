import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, useAction, useApi } from '../api.js'
import { ACTION_LABEL, Alert, Crumbs, Loading, Status, Table, time } from '../ui.jsx'

const EMPTY = {
  name: '', up_sql: '', down_sql: '', irreversible: false, irreversible_reason: '',
  mode: 'normal', batch_table: '', batch_column: 'id', batch_size: 10000,
}

export default function MigrationEdit({ db }) {
  const { mID } = useParams()
  const isNew = !mID
  const navigate = useNavigate()
  const q = useApi(isNew ? null : `/db/${db.id}/migrations/${mID}`)
  const [form, setForm] = useState(EMPTY)
  const [validation, setValidation] = useState(null)
  const [note, setNote] = useState('')
  const act = useAction()

  const m = q.data?.migration
  useEffect(() => {
    if (!m) return
    setForm({
      name: m.name, up_sql: m.up_sql, down_sql: m.down_sql, irreversible: !!m.irreversible_reason,
      irreversible_reason: m.irreversible_reason, mode: m.mode, batch_table: m.batch_table,
      batch_column: m.batch_column || 'id', batch_size: m.batch_size || 10000,
    })
  }, [m])

  if (!isNew && q.loading && !q.data) return <Loading />
  if (!isNew && q.error && !q.data) return <Alert error={q.error.message} />

  const editable = isNew ? db.can_edit : m.editable
  const set = k => e => setForm({ ...form, [k]: e.target.type === 'checkbox' ? e.target.checked : e.target.value })
  const body = { ...form, batch_size: Number(form.batch_size) || 0, down_sql: form.irreversible ? '' : form.down_sql }

  async function save(e) {
    e.preventDefault()
    if (isNew) {
      const r = await act.run(() => api(`/db/${db.id}/migrations`, { method: 'POST', body }))
      if (r) navigate(`/db/${db.id}/migrations/${r.id}`, { replace: true })
    } else {
      await act.run(() => api(`/db/${db.id}/migrations/${mID}`, { method: 'PUT', body }), 'Migration tersimpan.')
      q.reload(true)
    }
  }

  async function validate(analyze) {
    setValidation(null)
    const r = await act.run(() => api(`/db/${db.id}/migrations/${mID}/validate`, { method: 'POST', body: { analyze } }))
    if (r) setValidation(r)
  }

  async function remove() {
    if (!confirm('Hapus draft migration ini?')) return
    const r = await act.run(() => api(`/db/${db.id}/migrations/${mID}`, { method: 'DELETE' }))
    if (r) navigate(`/db/${db.id}/migrations`)
  }

  async function submit(action) {
    const r = await act.run(() => api(`/db/${db.id}/migrations/${mID}/requests`, { method: 'POST', body: { action, note } }))
    if (r) navigate(`/db/${db.id}/requests/${r.id}`)
  }

  return (
    <>
      <Crumbs items={[['Migration', `/db/${db.id}/migrations`], [isNew ? 'Baru' : m.name]]} />
      <div className="page-head">
        <h2>{isNew ? 'Migration baru' : m.name}</h2>
        {m && <><Status value={m.status} />{m.version && <span className="badge muted">versi {m.version}</span>}</>}
      </div>
      {m?.locked && <p className="alert info">SQL migration ini sudah pernah dieksekusi sehingga immutable. Untuk perbaikan, buat migration baru.</p>}
      {m && !m.locked && !editable && db.can_edit && <p className="alert info">Migration sedang diajukan; batalkan pengajuan untuk mengubahnya.</p>}
      <Alert error={act.error} message={act.message} />

      <form onSubmit={save} className="form">
        <fieldset disabled={!editable || act.busy}>
          <label>Nama<input value={form.name} onChange={set('name')} required maxLength={200} /></label>
          <div className="grid2">
            <label>Mode eksekusi
              <select value={form.mode} onChange={set('mode')}>
                <option value="normal">Normal: satu transaction</option>
                <option value="batch">Per batch: satu transaction per batch</option>
              </select>
            </label>
            {form.mode === 'batch' && (
              <div className="grid3">
                <label>Table<input value={form.batch_table} onChange={set('batch_table')} placeholder="big_orders" required /></label>
                <label>Kolom cursor<input value={form.batch_column} onChange={set('batch_column')} required /></label>
                <label>Batch size<input type="number" min="1" max="100000" value={form.batch_size} onChange={set('batch_size')} required /></label>
              </div>
            )}
          </div>
          {form.mode === 'batch' && (
            <p className="hint">
              Batch memakai cursor pada kolom unik yang tidak berubah: worker mengisi <code>$1</code> (cursor awal, eksklusif)
              dan <code>$2</code> (cursor akhir, inklusif). Contoh: <code>UPDATE big_orders SET amount = amount * 2 WHERE id &gt; $1 AND id &lt;= $2</code>.
              Batas atas ditetapkan saat eksekusi dimulai.
            </p>
          )}
          <label>SQL up
            <textarea className="sql" rows={8} value={form.up_sql} onChange={set('up_sql')} spellCheck={false} required />
          </label>
          <label className="check"><input type="checkbox" checked={form.irreversible} onChange={set('irreversible')} /> Irreversible (tidak dapat di-rollback)</label>
          {form.irreversible
            ? <label>Alasan irreversible<input value={form.irreversible_reason} onChange={set('irreversible_reason')} required
              placeholder="mis. menghapus data yang tidak dapat dipulihkan" /></label>
            : <label>SQL down (rollback)
              <textarea className="sql" rows={6} value={form.down_sql} onChange={set('down_sql')} spellCheck={false} required />
            </label>}
        </fieldset>
        {editable && (
          <div className="row">
            <button className="btn primary" disabled={act.busy}>{isNew ? 'Simpan draft' : 'Simpan perubahan'}</button>
            {!isNew && <button type="button" className="btn danger ghost" onClick={remove} disabled={act.busy}>Hapus</button>}
          </div>
        )}
      </form>

      {!isNew && db.can_edit && (
        <section>
          <h3>Validasi</h3>
          <p className="muted small">Validasi aturan migration lalu dry-run SQL up kemudian down dalam satu transaction yang selalu di-rollback.</p>
          <div className="row">
            <button className="btn" onClick={() => validate(false)} disabled={act.busy}>Validasi + EXPLAIN</button>
            <button className="btn" onClick={() => validate(true)} disabled={act.busy}>Validasi + EXPLAIN ANALYZE</button>
          </div>
          {validation && <Validation v={validation} />}
        </section>
      )}

      {!isNew && m.actions.length > 0 && (
        <section className="card">
          <h3>Ajukan perubahan</h3>
          <p className="muted small">Pengajuan perlu approval leader setiap group yang memiliki akses ke database ini, lalu dijalankan melalui queue.</p>
          <label>Catatan untuk reviewer<textarea rows={2} value={note} onChange={e => setNote(e.target.value)} /></label>
          <div className="row">
            {m.actions.map(a => (
              <button key={a} className={`btn ${a === 'rollback' ? 'danger' : 'primary'}`} disabled={act.busy} onClick={() => submit(a)}>
                Ajukan {ACTION_LABEL[a]}
              </button>
            ))}
          </div>
        </section>
      )}

      {!isNew && q.data.requests.length > 0 && (
        <section>
          <h3>Pengajuan migration ini</h3>
          <Table>
            <thead><tr><th>#</th><th>Aksi</th><th>Status</th><th>Batch / row</th><th>Oleh</th><th>Waktu</th></tr></thead>
            <tbody>{q.data.requests.map(r => (
              <tr key={r.id}>
                <td><Link to={`/db/${db.id}/requests/${r.id}`}>#{r.id}</Link></td>
                <td>{ACTION_LABEL[r.action]}</td>
                <td><Status value={r.status} /></td>
                <td>{r.batches_done} / {r.rows_done.toLocaleString('id-ID')}</td>
                <td>{r.requested_by}</td>
                <td className="small">{time(r.created_at)}</td>
              </tr>
            ))}</tbody>
          </Table>
        </section>
      )}
    </>
  )
}

function Validation({ v }) {
  return (
    <div className="validation">
      {v.valid ? <div className="alert ok">Validasi lolos.</div> : <div className="alert bad">{v.error || 'Ada statement yang gagal.'}</div>}
      {[['SQL up', v.up], ['SQL down', v.down]].map(([title, list]) => list.length > 0 && (
        <div key={title}>
          <h4>{title}</h4>
          {list.map((r, i) => (
            <div className="plan" key={i}>
              <pre className="stmt">{r.statement}</pre>
              {r.error && <div className="alert bad">{r.error}</div>}
              {r.note && <p className="muted small">{r.note}</p>}
              {r.plan && <pre>{r.plan}</pre>}
            </div>
          ))}
        </div>
      ))}
    </div>
  )
}
