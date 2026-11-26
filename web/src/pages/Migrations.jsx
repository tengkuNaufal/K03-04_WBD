import { Link } from 'react-router-dom'
import { useApi } from '../api.js'
import { ACTION_LABEL, Empty, Page, Status, Table, time } from '../ui.jsx'

export default function Migrations({ db }) {
  const q = useApi(`/db/${db.id}/migrations`)
  return (
    <>
      <div className="page-head">
        <h2>Migration</h2>
        {db.can_edit && <Link className="btn primary" to={`/db/${db.id}/migrations/new`}>+ Migration baru</Link>}
      </div>
      <p className="muted small">
        Riwayat linear: versi diberikan saat migration pertama kali berhasil di-commit. Rollback hanya untuk migration
        aktif terakhir; forward mengikuti urutan versi. Setiap perubahan melewati review dan approval leader setiap group
        yang memiliki akses, lalu dijalankan worker melalui queue.
      </p>
      <Page q={q}>{ms => ms.length === 0 ? <Empty>Belum ada migration.</Empty> : (
        <Table>
          <thead><tr><th>Versi</th><th>Nama</th><th>Mode</th><th>Status</th><th>Dibuat</th><th>Aksi tersedia</th></tr></thead>
          <tbody>{ms.map(m => (
            <tr key={m.id}>
              <td>{m.version ?? '–'}</td>
              <td><Link to={`/db/${db.id}/migrations/${m.id}`}>{m.name}</Link>{m.irreversible_reason && <span className="badge muted">irreversible</span>}</td>
              <td>{m.mode === 'batch' ? `batch (${m.batch_size})` : 'normal'}</td>
              <td><Status value={m.status} /></td>
              <td className="small">{m.created_by}<br />{time(m.created_at)}</td>
              <td>{m.actions.map(a => ACTION_LABEL[a]).join(', ') || '–'}</td>
            </tr>
          ))}</tbody>
        </Table>
      )}</Page>
    </>
  )
}
