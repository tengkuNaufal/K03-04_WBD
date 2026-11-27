import { Link } from 'react-router-dom'
import { useApi } from '../../api.js'
import { Empty, Page, Table } from '../../ui.jsx'

export default function Databases() {
  const q = useApi('/admin/databases')
  return (
    <>
      <div className="page-head">
        <h1>Koneksi database</h1>
        <Link className="btn primary" to="/admin/databases/new">+ Koneksi baru</Link>
      </div>
      <Page q={q}>{dbs => dbs.length === 0 ? <Empty>Belum ada koneksi.</Empty> : (
        <Table>
          <thead><tr><th>Nama</th><th>Host</th><th>Database</th><th>User</th></tr></thead>
          <tbody>{dbs.map(d => (
            <tr key={d.id}>
              <td><Link to={`/admin/databases/${d.id}`}>{d.name}</Link></td>
              <td>{d.host}:{d.port}</td>
              <td>{d.dbname}</td>
              <td>{d.username}</td>
            </tr>
          ))}</tbody>
        </Table>
      )}</Page>
    </>
  )
}
