import { Link } from 'react-router-dom'
import { useApi } from '../api.js'
import { useSession } from '../App.jsx'
import { ACTION_LABEL, Empty, LEVEL, Page, Status, time } from '../ui.jsx'

export default function Dashboard() {
  const { user } = useSession()
  const dbs = useApi('/databases')
  const reviews = useApi('/reviews', { poll: 10000 })

  return (
    <>
      <h1>Dashboard</h1>
      <section>
        <h2>Menunggu approval Anda</h2>
        <Page q={reviews}>{rs => rs.length === 0
          ? <Empty>Tidak ada pengajuan yang menunggu approval Anda sebagai leader group.</Empty>
          : <ul className="list">
            {rs.map(r => (
              <li key={r.id}>
                <Link to={`/db/${r.database_id}/requests/${r.id}`}>
                  <strong>#{r.id} {ACTION_LABEL[r.action]}</strong> {r.migration_name}
                </Link>
                <span className="muted small"> oleh {r.requested_by} · {time(r.created_at)}</span> <Status value={r.status} />
              </li>
            ))}
          </ul>}
        </Page>
      </section>
      <section>
        <h2>Database</h2>
        <Page q={dbs}>{list => list.length === 0
          ? <Empty>
            Anda belum memiliki akses ke database mana pun. Akses diberikan melalui group
            {user.is_admin && ' (Platform Admin juga perlu masuk group untuk melihat isi database)'}.
          </Empty>
          : <div className="cards">
            {list.map(d => (
              <div className="card" key={d.id}>
                <h3><Link to={`/db/${d.id}`}>{d.name}</Link></h3>
                <p><span className={`badge ${d.level === 'edit' ? 'ok' : 'info'}`}>{LEVEL[d.level]}</span></p>
                <div className="row">
                  <Link className="btn small" to={`/db/${d.id}`}>Tabel</Link>
                  <Link className="btn small" to={`/db/${d.id}/migrations`}>Migration</Link>
                  <Link className="btn small" to={`/db/${d.id}/history`}>Riwayat</Link>
                </div>
              </div>
            ))}
          </div>}
        </Page>
      </section>
    </>
  )
}
