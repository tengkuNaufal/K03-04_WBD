import { NavLink, Route, Routes, useParams } from 'react-router-dom'
import { useApi } from '../api.js'
import { Crumbs, LEVEL, Page } from '../ui.jsx'
import Tables from '../Tables.jsx'
import TableView from '../TableView.jsx'
import Migrations from '../Migrations.jsx'
import MigrationEdit from '../MigrationEdit.jsx'
import Requests from '../Requests.jsx'
import RequestDetail from '../RequestDetail.jsx'
import History from '../History.jsx'

export default function Database() {
  const { dbID } = useParams()
  const q = useApi(`/db/${dbID}`)
  return (
    <Page q={q}>{db => (
      <>
        <Crumbs items={[['Dashboard', '/'], [db.name]]} />
        <div className="page-head">
          <h1>{db.name}</h1>
          <span className={`badge ${db.level === 'edit' ? 'ok' : 'info'}`}>{LEVEL[db.level]}</span>
        </div>
        <nav className="tabs">
          <NavLink to={`/db/${dbID}`} end>Tabel</NavLink>
          <NavLink to={`/db/${dbID}/migrations`}>Migration</NavLink>
          <NavLink to={`/db/${dbID}/requests`}>Pengajuan</NavLink>
          <NavLink to={`/db/${dbID}/history`}>Riwayat</NavLink>
        </nav>
        <Routes>
          <Route index element={<Tables db={db} />} />
          <Route path="tables/:schema/:table" element={<TableView db={db} />} />
          <Route path="migrations" element={<Migrations db={db} />} />
          <Route path="migrations/new" element={<MigrationEdit db={db} />} />
          <Route path="migrations/:mID" element={<MigrationEdit db={db} />} />
          <Route path="requests" element={<Requests db={db} />} />
          <Route path="requests/:rID" element={<RequestDetail db={db} />} />
          <Route path="history" element={<History db={db} />} />
        </Routes>
      </>
    )}</Page>
  )
}
