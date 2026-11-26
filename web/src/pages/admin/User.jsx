import { useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api, useAction, useApi } from '../../api.js'
import { Alert, Crumbs, Page } from '../../ui.jsx'

export default function User() {
  const { id } = useParams()
  const navigate = useNavigate()
  const q = useApi(`/admin/users/${id}`)
  const [password, setPassword] = useState('')
  const act = useAction()

  async function setFlags(u, patch) {
    await act.run(() => api(`/admin/users/${id}`, { method: 'PUT', body: { is_admin: u.is_admin, is_active: u.is_active, ...patch } }), 'User diperbarui.')
    q.reload(true)
  }

  async function reset(e) {
    e.preventDefault()
    const r = await act.run(() => api(`/admin/users/${id}/password`, { method: 'POST', body: { password } }),
      'Password diganti dan semua session user diakhiri.')
    if (r) setPassword('')
  }

  async function remove(u) {
    if (!confirm(`Hapus user ${u.username}?`)) return
    const r = await act.run(() => api(`/admin/users/${id}`, { method: 'DELETE' }))
    if (r) navigate('/admin/users')
  }

  return (
    <Page q={q}>{u => (
      <>
        <Crumbs items={[['Users', '/admin/users'], [u.username]]} />
        <h1>{u.username}</h1>
        <Alert error={act.error} message={act.message} />
        <div className="card">
          <label className="check"><input type="checkbox" checked={u.is_admin} disabled={act.busy}
            onChange={e => setFlags(u, { is_admin: e.target.checked })} /> Platform Admin</label>
          <label className="check"><input type="checkbox" checked={u.is_active} disabled={act.busy}
            onChange={e => setFlags(u, { is_active: e.target.checked })} /> Aktif</label>
          <p className="muted small">Mencabut admin atau menonaktifkan user langsung mengakhiri semua session-nya.</p>
        </div>
        <form className="card form" onSubmit={reset}>
          <h3>Reset password</h3>
          <label>Password baru<input type="password" value={password} onChange={e => setPassword(e.target.value)} required autoComplete="new-password" /></label>
          <button className="btn" disabled={act.busy}>Ganti password</button>
        </form>
        <button className="btn danger ghost" onClick={() => remove(u)} disabled={act.busy}>Hapus user</button>
      </>
    )}</Page>
  )
}
