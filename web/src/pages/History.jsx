import { useState } from 'react'
import { useApi } from '../api.js'
import { Empty, Page } from '../ui.jsx'
import { Attempts } from './RequestDetail.jsx'

function stored() {
  try { return localStorage.getItem('dbmp.hideRollback') === '1' } catch { return false }
}

export default function History({ db }) {
  const [hide, setHide] = useState(stored)
  const q = useApi(`/db/${db.id}/history${hide ? '?hide_rollback=1' : ''}`)

  function toggle(e) {
    setHide(e.target.checked)
    try { localStorage.setItem('dbmp.hideRollback', e.target.checked ? '1' : '0') } catch { /* ignore */ }
  }

  return (
    <>
      <div className="page-head">
        <h2>Migration history</h2>
        <label className="check"><input type="checkbox" checked={hide} onChange={toggle} /> Hide rollback</label>
      </div>
      <p className="muted small">
        Semua attempt tercatat dan tidak pernah ditimpa, termasuk yang gagal. Hide rollback hanya menyembunyikan pasangan
        eksekusi yang dibatalkan dan rollback sukses yang membatalkannya (dicocokkan dengan execution ID); data tidak dihapus.
      </p>
      <Page q={q}>{list => list.length === 0 ? <Empty>Belum ada eksekusi.</Empty> : <Attempts list={list} showMigration />}</Page>
    </>
  )
}
