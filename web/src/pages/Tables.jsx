import { Link } from 'react-router-dom'
import { useApi } from '../api.js'
import { Empty, Page, Table } from '../ui.jsx'

export default function Tables({ db }) {
  const q = useApi(`/db/${db.id}/tables`)
  return (
    <Page q={q}>{tables => tables.length === 0 ? <Empty>Database ini belum memiliki table atau view.</Empty> : (
      <Table>
        <thead><tr><th>Schema</th><th>Nama</th><th>Jenis</th><th className="num">Perkiraan baris</th></tr></thead>
        <tbody>
          {tables.map(t => (
            <tr key={t.schema + '.' + t.name}>
              <td>{t.schema}</td>
              <td><Link to={`/db/${db.id}/tables/${encodeURIComponent(t.schema)}/${encodeURIComponent(t.name)}`}>{t.name}</Link></td>
              <td>{t.kind}</td>
              <td className="num">{t.estimated_rows.toLocaleString('id-ID')}</td>
            </tr>
          ))}
        </tbody>
      </Table>
    )}</Page>
  )
}
