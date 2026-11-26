import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useApi } from '../api.js'
import { ACTION_LABEL, Empty, Page, Status, Table, time } from '../ui.jsx'

const ACTIVE = ['pending_review', 'approved', 'queued', 'running', 'partial', 'partially_reverted', 'uncertain']

export default function Requests({ db }) {
  const [onlyActive, setOnlyActive] = useState(false)
  const q = useApi(`/db/${db.id}/requests`, { poll: 3000, shouldPoll: rs => rs.some(r => ['queued', 'running'].includes(r.status)) })
  return (
    <>
      <div className="page-head">
        <h2>Pengajuan perubahan</h2>
        <label className="check"><input type="checkbox" checked={onlyActive} onChange={e => setOnlyActive(e.target.checked)} /> Hanya yang aktif</label>
      </div>
      <Page q={q}>{all => {
        const rs = onlyActive ? all.filter(r => ACTIVE.includes(r.status)) : all
        return rs.length === 0 ? <Empty>Belum ada pengajuan.</Empty> : (
          <Table>
            <thead><tr><th>#</th><th>Migration</th><th>Aksi</th><th>Status</th><th>Batch / row</th><th>Oleh</th><th>Waktu</th></tr></thead>
            <tbody>{rs.map(r => (
              <tr key={r.id}>
                <td><Link to={`/db/${db.id}/requests/${r.id}`}>#{r.id}</Link></td>
                <td>{r.migration_name}</td>
                <td>{ACTION_LABEL[r.action]}{r.target_request_id && <span className="muted small"> (#{r.target_request_id})</span>}</td>
                <td><Status value={r.status} /></td>
                <td>{r.batches_done} / {r.rows_done.toLocaleString('id-ID')}</td>
                <td>{r.requested_by}</td>
                <td className="small">{time(r.updated_at)}</td>
              </tr>
            ))}</tbody>
          </Table>
        )
      }}</Page>
    </>
  )
}
