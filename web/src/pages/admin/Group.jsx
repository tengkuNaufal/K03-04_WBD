import { useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api, useAction, useApi } from '../../api.js'
import { Alert, Crumbs, Empty, LEVEL, Page, Table } from '../../ui.jsx'

export default function Group() {
  const { id } = useParams()
  const navigate = useNavigate()
  const q = useApi(`/admin/groups/${id}`)
  const [member, setMember] = useState('')
  const [grant, setGrant] = useState({ database_id: '', level: 'view' })
  const act = useAction()

  async function call(path, method, body, msg) {
    const r = await act.run(() => api(`/admin/groups/${id}${path}`, { method, body }), msg)
    if (r) q.reload(true)
    return r
  }

  async function remove(g) {
    if (!confirm(`Hapus group ${g.name}? Session semua anggota akan diakhiri.`)) return
    const r = await act.run(() => api(`/admin/groups/${id}`, { method: 'DELETE' }))
    if (r) navigate('/admin/groups')
  }

  return (
    <Page q={q}>{d => {
      const memberIDs = new Set(d.members.map(m => m.id))
      return (
        <>
          <Crumbs items={[['Groups', '/admin/groups'], [d.group.name]]} />
          <h1>{d.group.name}</h1>
          <Alert error={act.error} message={act.message} />

          <section className="card">
            <h3>Leader</h3>
            <p className="muted small">Leader menyetujui (approve) setiap perubahan pada database yang dapat diakses group ini. Leader harus anggota group.</p>
            <select value={d.group.leader_id ?? ''} disabled={act.busy}
              onChange={e => call('/leader', 'PUT', { user_id: e.target.value ? Number(e.target.value) : null }, 'Leader disimpan.')}>
              <option value="">(belum ada leader)</option>
              {d.members.map(m => <option key={m.id} value={m.id}>{m.username}</option>)}
            </select>
          </section>

          <section>
            <h3>Anggota</h3>
            <form className="row" onSubmit={e => { e.preventDefault(); call('/members', 'POST', { user_id: Number(member) }, 'Anggota ditambahkan.').then(r => r && setMember('')) }}>
              <select value={member} onChange={e => setMember(e.target.value)} required>
                <option value="">Pilih user…</option>
                {d.users.filter(u => !memberIDs.has(u.id)).map(u => <option key={u.id} value={u.id}>{u.username}</option>)}
              </select>
              <button className="btn" disabled={act.busy || !member}>Tambah</button>
            </form>
            {d.members.length === 0 ? <Empty>Belum ada anggota.</Empty> : (
              <Table>
                <thead><tr><th>Username</th><th></th></tr></thead>
                <tbody>{d.members.map(m => (
                  <tr key={m.id}>
                    <td>{m.username}{d.group.leader_id === m.id && <span className="badge info">leader</span>}</td>
                    <td className="num"><button className="btn small ghost" disabled={act.busy}
                      onClick={() => call(`/members/${m.id}`, 'DELETE', undefined, 'Anggota dikeluarkan dan session-nya diakhiri.')}>Keluarkan</button></td>
                  </tr>
                ))}</tbody>
              </Table>
            )}
          </section>

          <section>
            <h3>Akses database</h3>
            <form className="row" onSubmit={e => { e.preventDefault(); call('/grants', 'PUT', { database_id: Number(grant.database_id), level: grant.level }, 'Akses disimpan.') }}>
              <select value={grant.database_id} onChange={e => setGrant({ ...grant, database_id: e.target.value })} required>
                <option value="">Pilih database…</option>
                {d.databases.map(db => <option key={db.id} value={db.id}>{db.name}</option>)}
              </select>
              <select value={grant.level} onChange={e => setGrant({ ...grant, level: e.target.value })}>
                <option value="view">View Only</option>
                <option value="edit">View + Edit</option>
              </select>
              <button className="btn" disabled={act.busy || !grant.database_id}>Simpan</button>
            </form>
            {d.grants.length === 0 ? <Empty>Group ini belum memiliki akses database.</Empty> : (
              <Table>
                <thead><tr><th>Database</th><th>Tingkat</th><th></th></tr></thead>
                <tbody>{d.grants.map(g => (
                  <tr key={g.database_id}>
                    <td>{g.database_name}</td>
                    <td>{LEVEL[g.level]}</td>
                    <td className="num"><button className="btn small ghost" disabled={act.busy}
                      onClick={() => call(`/grants/${g.database_id}`, 'DELETE', undefined, 'Akses dicabut.')}>Cabut</button></td>
                  </tr>
                ))}</tbody>
              </Table>
            )}
          </section>
          <button className="btn danger ghost" onClick={() => remove(d.group)} disabled={act.busy}>Hapus group</button>
        </>
      )
    }}</Page>
  )
}
