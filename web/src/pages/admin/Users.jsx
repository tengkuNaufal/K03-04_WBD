import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api, useAction, useApi } from '../../api.js'
import { Alert, Page, Table, time } from '../../ui.jsx'

export default function Users() {
  const q = useApi('/admin/users')
  const [form, setForm] = useState({ username: '', password: '', is_admin: false })
  const act = useAction()

  async function create(e) {
    e.preventDefault()
    const r = await act.run(() => api('/admin/users', { method: 'POST', body: form }), 'User dibuat.')
    if (r) { setForm({ username: '', password: '', is_admin: false }); q.reload(true) }
  }

  return (
    <>
      <h1>Users</h1>
      <Alert error={act.error} message={act.message} />
      <form className="card form inline-form" onSubmit={create}>
        <h3>User baru</h3>
        <div className="grid3">
          <label>Username<input value={form.username} onChange={e => setForm({ ...form, username: e.target.value })} required autoComplete="off" /></label>
          <label>Password (min. 8)<input type="password" value={form.password} onChange={e => setForm({ ...form, password: e.target.value })} required autoComplete="new-password" /></label>
          <label className="check"><input type="checkbox" checked={form.is_admin} onChange={e => setForm({ ...form, is_admin: e.target.checked })} /> Platform Admin</label>
        </div>
        <button className="btn primary" disabled={act.busy}>Buat user</button>
      </form>
      <Page q={q}>{users => (
        <Table>
          <thead><tr><th>Username</th><th>Peran</th><th>Status</th><th>Dibuat</th></tr></thead>
          <tbody>{users.map(u => (
            <tr key={u.id}>
              <td><Link to={`/admin/users/${u.id}`}>{u.username}</Link></td>
              <td>{u.is_admin ? <span className="badge info">Platform Admin</span> : 'User'}</td>
              <td>{u.is_active ? <span className="badge ok">Aktif</span> : <span className="badge bad">Nonaktif</span>}</td>
              <td className="small">{time(u.created_at)}</td>
            </tr>
          ))}</tbody>
        </Table>
      )}</Page>
    </>
  )
}
