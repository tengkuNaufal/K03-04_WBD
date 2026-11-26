import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api, useAction, useApi } from '../../api.js'
import { Alert, Crumbs, Loading } from '../../ui.jsx'

const SSL = ['disable', 'allow', 'prefer', 'require', 'verify-ca', 'verify-full']

export default function DatabaseForm() {
  const { id } = useParams()
  const isNew = !id
  const navigate = useNavigate()
  const q = useApi(isNew ? null : `/admin/databases/${id}`)
  const [form, setForm] = useState({ name: '', host: '', port: 5432, dbname: '', username: '', password: '', sslmode: 'disable' })
  const act = useAction()

  useEffect(() => { if (q.data) setForm({ ...q.data, password: '' }) }, [q.data])

  if (!isNew && q.loading && !q.data) return <Loading />
  if (!isNew && q.error && !q.data) return <Alert error={q.error.message} />

  const set = k => e => setForm({ ...form, [k]: e.target.value })

  async function save(e) {
    e.preventDefault()
    const body = { ...form, port: Number(form.port) }
    if (isNew) {
      const r = await act.run(() => api('/admin/databases', { method: 'POST', body }))
      if (r) navigate(`/admin/databases/${r.id}`, { replace: true })
    } else {
      const r = await act.run(() => api(`/admin/databases/${id}`, { method: 'PUT', body }), 'Koneksi diperbarui.')
      if (r) setForm(f => ({ ...f, password: '' }))
    }
  }

  async function remove() {
    if (!confirm('Hapus koneksi ini? Migration dan akses group untuk database ini ikut terhapus (riwayat eksekusi tetap disimpan).')) return
    const r = await act.run(() => api(`/admin/databases/${id}`, { method: 'DELETE' }))
    if (r) navigate('/admin/databases')
  }

  return (
    <>
      <Crumbs items={[['Koneksi', '/admin/databases'], [isNew ? 'Baru' : form.name]]} />
      <h1>{isNew ? 'Koneksi baru' : form.name}</h1>
      <Alert error={act.error} message={act.message} />
      <form className="card form" onSubmit={save}>
        <fieldset disabled={act.busy}>
          <label>Nama koneksi<input value={form.name} onChange={set('name')} required /></label>
          <div className="grid2">
            <label>Host<input value={form.host} onChange={set('host')} required /></label>
            <label>Port<input type="number" value={form.port} onChange={set('port')} required min="1" max="65535" /></label>
            <label>Nama database<input value={form.dbname} onChange={set('dbname')} required /></label>
            <label>Username<input value={form.username} onChange={set('username')} required autoComplete="off" /></label>
            <label>Password<input type="password" value={form.password} onChange={set('password')} required={isNew}
              autoComplete="new-password" placeholder={isNew ? '' : 'kosongkan jika tidak diganti'} /></label>
            <label>SSL mode<select value={form.sslmode} onChange={set('sslmode')}>{SSL.map(s => <option key={s}>{s}</option>)}</select></label>
          </div>
          <p className="muted small">Koneksi diuji sebelum disimpan. Password disimpan terenkripsi dan tidak pernah ditampilkan.</p>
          <div className="row">
            <button className="btn primary">{act.busy ? 'Menguji koneksi…' : 'Simpan'}</button>
            {!isNew && <button type="button" className="btn danger ghost" onClick={remove}>Hapus</button>}
          </div>
        </fieldset>
      </form>
    </>
  )
}
