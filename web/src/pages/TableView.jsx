import { useState } from 'react'
import { useParams } from 'react-router-dom'
import { useApi } from '../api.js'
import { Empty, Loading, Page, Table } from '../ui.jsx'

export default function TableView({ db }) {
  const { schema, table } = useParams()
  const base = `/db/${db.id}/tables/${encodeURIComponent(schema)}/${encodeURIComponent(table)}`
  const detail = useApi(base)
  const [tab, setTab] = useState('structure')

  return (
    <>
      <h2>{schema}.{table}</h2>
      <div className="segmented">
        <button className={tab === 'structure' ? 'active' : ''} onClick={() => setTab('structure')}>Struktur</button>
        <button className={tab === 'data' ? 'active' : ''} onClick={() => setTab('data')}>Data</button>
      </div>
      {tab === 'structure'
        ? <Page q={detail}>{d => <Structure d={d} />}</Page>
        : <Rows base={base} />}
    </>
  )
}

function Structure({ d }) {
  return (
    <>
      <p className="muted">{d.kind}</p>
      <h3>Kolom</h3>
      <Table>
        <thead><tr><th>Nama</th><th>Tipe</th><th>Not null</th><th>Default</th></tr></thead>
        <tbody>{d.columns.map(c => (
          <tr key={c.name}><td><code>{c.name}</code></td><td>{c.type}</td><td>{c.not_null ? 'ya' : ''}</td><td><code>{c.default}</code></td></tr>
        ))}</tbody>
      </Table>
      <h3>Constraint</h3>
      {d.constraints.length === 0 ? <Empty>Tidak ada constraint.</Empty> : (
        <Table>
          <thead><tr><th>Nama</th><th>Jenis</th><th>Definisi</th></tr></thead>
          <tbody>{d.constraints.map(c => (
            <tr key={c.name}><td>{c.name}</td><td>{c.type}</td><td><code>{c.definition}</code></td></tr>
          ))}</tbody>
        </Table>
      )}
      <h3>Index</h3>
      {d.indexes.length === 0 ? <Empty>Tidak ada index.</Empty> : (
        <Table>
          <thead><tr><th>Nama</th><th>Definisi</th></tr></thead>
          <tbody>{d.indexes.map(i => (
            <tr key={i.name}><td>{i.name}</td><td><code>{i.definition}</code></td></tr>
          ))}</tbody>
        </Table>
      )}
    </>
  )
}

function Rows({ base }) {
  const [offset, setOffset] = useState(0)
  const q = useApi(`${base}/rows?offset=${offset}`)
  if (q.loading && !q.data) return <Loading />
  return (
    <Page q={q}>{p => (
      <>
        {q.loading && <Loading text="Memuat halaman…" />}
        {p.rows?.length ? (
          <Table>
            <thead><tr>{p.columns.map(c => <th key={c}>{c}</th>)}</tr></thead>
            <tbody>{p.rows.map((r, i) => (
              <tr key={offset + i}>{r.map((v, j) => <td key={j}>{v === null ? <span className="null">NULL</span> : v}</td>)}</tr>
            ))}</tbody>
          </Table>
        ) : <Empty>Tidak ada baris.</Empty>}
        <div className="pager">
          <button className="btn small" disabled={offset === 0} onClick={() => setOffset(Math.max(offset - p.limit, 0))}>‹ Sebelumnya</button>
          <span className="muted">Baris {p.rows?.length ? offset + 1 : 0}–{offset + (p.rows?.length || 0)}</span>
          <button className="btn small" disabled={!p.has_more} onClick={() => setOffset(offset + p.limit)}>Berikutnya ›</button>
        </div>
      </>
    )}</Page>
  )
}
