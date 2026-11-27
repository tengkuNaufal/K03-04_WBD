import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, useAction, useApi } from '../../api.js'
import { Alert, Empty, Page, Table } from '../../ui.jsx'

export default function Groups() {
  const q = useApi('/admin/groups')
  const [name, setName] = useState('')
  const act = useAction()
  const navigate = useNavigate()

  async function create(e) {
    e.preventDefault()
    const r = await act.run(() => api('/admin/groups', { method: 'POST', body: { name } }))
    if (r) navigate(`/admin/groups/${r.id}`)
  }

  return (
    <>
      <h1>Groups</h1>
      <p className="muted small">Akses database hanya diberikan ke group. Leader group menyetujui setiap perubahan pada database yang dapat diakses group-nya.</p>
      <Alert error={act.error} />
      <form className="card form inline-form" onSubmit={create}>
        <label>Group baru<input value={name} onChange={e => setName(e.target.value)} required maxLength={64} /></label>
        <button className="btn primary" disabled={act.busy}>Buat group</button>
      </form>
      <Page q={q}>{gs => gs.length === 0 ? <Empty>Belum ada group.</Empty> : (
        <Table>
          <thead><tr><th>Nama</th><th>Leader</th><th className="num">Anggota</th></tr></thead>
          <tbody>{gs.map(g => (
            <tr key={g.id}>
              <td><Link to={`/admin/groups/${g.id}`}>{g.name}</Link></td>
              <td>{g.leader || <span className="bad-text">belum ada</span>}</td>
              <td className="num">{g.member_count}</td>
            </tr>
          ))}</tbody>
        </Table>
      )}</Page>
    </>
  )
}
