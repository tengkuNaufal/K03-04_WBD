import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, useAction, useApi } from '../api.js'
import { ACTION_LABEL, Alert, Crumbs, Page, Status, Table, time } from '../ui.jsx'

const LIVE = ['queued', 'running', 'uncertain']

const STEPS = ['pending_review', 'approved', 'queued', 'running', 'done']

function step(status) {
  if (['success', 'failed', 'partial', 'partially_reverted', 'superseded'].includes(status)) return 4
  if (status === 'uncertain') return 3
  return STEPS.indexOf(status)
}

export default function RequestDetail({ db }) {
  const { rID } = useParams()
  const navigate = useNavigate()
  const q = useApi(`/db/${db.id}/requests/${rID}`, {
    poll: 1500, shouldPoll: d => LIVE.includes(d.request.status) || ['queued', 'running'].includes(d.job?.status),
  })
  const [comment, setComment] = useState('')
  const act = useAction()

  async function call(path, body, msg) {
    const r = await act.run(() => api(`/db/${db.id}/requests/${rID}/${path}`, { method: 'POST', body }), msg)
    if (r) q.reload(true)
    return r
  }

  async function follow(action, target) {
    const d = q.data
    const r = await act.run(() => api(`/db/${db.id}/migrations/${d.migration.id}/requests`, {
      method: 'POST', body: { action, target_request_id: target, note: `Lanjutan dari pengajuan #${target}` },
    }))
    if (r) navigate(`/db/${db.id}/requests/${r.id}`)
  }

  return (
    <Page q={q}>{d => {
      const r = d.request
      const m = d.migration
      const stopped = r.status === 'partial' || r.status === 'partially_reverted'
      return (
        <>
          <Crumbs items={[['Pengajuan', `/db/${db.id}/requests`], [`#${r.id}`]]} />
          <div className="page-head">
            <h2>#{r.id} {ACTION_LABEL[r.action]}: <Link to={`/db/${db.id}/migrations/${m.id}`}>{m.name}</Link></h2>
            <Status value={r.status} />
          </div>

          <ol className="steps" aria-label="Alur">
            {['Pending review', 'Approved', 'Pending (queue)', 'Running', 'Selesai'].map((s, i) => (
              <li key={s} className={i < step(r.status) ? 'done' : i === step(r.status) ? 'current' : ''}>{s}</li>
            ))}
          </ol>

          <Alert error={act.error} message={act.message} />
          {r.error && <div className={`alert ${r.status === 'queued' ? 'warn' : 'bad'}`}>{r.error}</div>}

          <div className="grid2">
            <div className="card">
              <dl className="facts">
                <dt>Arah</dt><dd>{r.direction === 'up' ? 'up (SQL up)' : 'down (SQL down)'}</dd>
                <dt>Mode</dt><dd>{m.mode === 'batch' ? `per batch: ${m.batch_table}.${m.batch_column}, ${m.batch_size} baris/batch` : 'normal'}</dd>
                <dt>Diajukan</dt><dd>{r.requested_by}, {time(r.created_at)}</dd>
                {r.target_request_id && <><dt>Melanjutkan</dt><dd><Link to={`/db/${db.id}/requests/${r.target_request_id}`}>#{r.target_request_id}</Link></dd></>}
                {r.note && <><dt>Catatan</dt><dd>{r.note}</dd></>}
                <dt>Batch committed</dt><dd>{r.batches_done.toLocaleString('id-ID')}</dd>
                <dt>Row committed</dt><dd>{r.rows_done.toLocaleString('id-ID')}</dd>
                {r.scope_max != null && <><dt>Cakupan cursor</dt><dd>sampai {m.batch_column} ≤ {r.scope_max.toLocaleString('id-ID')}</dd></>}
              </dl>
              {stopped && <p className="alert warn">Status {r.status}: batch yang sudah committed tetap tersimpan. Database ditahan sampai pengajuan ini dilanjutkan atau dibatalkan.</p>}
            </div>
            {d.job && (
              <div className="card">
                <h3>Job #{d.job.id}</h3>
                <dl className="facts">
                  <dt>Status job</dt><dd><Status value={d.job.status} /></dd>
                  <dt>Attempt</dt><dd>{d.job.attempts} / {d.job.max_attempts}</dd>
                  <dt>Worker</dt><dd>{d.job.locked_by || '–'}</dd>
                  <dt>Masuk queue</dt><dd>{time(d.job.enqueued_at)}</dd>
                  <dt>Mulai</dt><dd>{time(d.job.started_at)}</dd>
                  <dt>Selesai</dt><dd>{time(d.job.finished_at)}</dd>
                </dl>
                {LIVE.includes(r.status) && <p className="muted small"><span className="spinner" /> Memantau progres…</p>}
              </div>
            )}
          </div>

          <h3>Approval</h3>
          <Table>
            <thead><tr><th>Group</th><th>Leader</th><th>Keputusan</th><th>Oleh</th><th>Komentar</th></tr></thead>
            <tbody>{r.approvals.map(a => (
              <tr key={a.group_id}>
                <td>{a.group_name}</td>
                <td>{a.leader || <span className="bad-text">belum ada leader</span>}</td>
                <td>{a.decision ? <Status value={a.decision} /> : <span className="muted">menunggu</span>}</td>
                <td className="small">{a.decided_by}{a.decided_at && <><br />{time(a.decided_at)}</>}</td>
                <td>{a.comment}</td>
              </tr>
            ))}</tbody>
          </Table>

          <div className="actions-bar">
            {d.can_approve && (
              <div className="card">
                <label>Komentar review<textarea rows={2} value={comment} onChange={e => setComment(e.target.value)} /></label>
                <div className="row">
                  <button className="btn primary" disabled={act.busy} onClick={() => call('approve', { comment }, 'Disetujui.')}>Approve</button>
                  <button className="btn danger" disabled={act.busy} onClick={() => call('reject', { comment }, 'Ditolak.')}>Reject</button>
                </div>
              </div>
            )}
            <div className="row">
              {d.can_execute && <button className="btn primary" disabled={act.busy}
                onClick={() => call('execute', {}, x => `Job #${x.job_id} masuk queue.`)}>Jalankan (kirim ke queue)</button>}
              {d.can_cancel && <button className="btn ghost" disabled={act.busy} onClick={() => confirm('Batalkan pengajuan?') && call('cancel', {}, 'Pengajuan dibatalkan.')}>Batalkan pengajuan</button>}
              {d.can_resume && <button className="btn primary" disabled={act.busy} onClick={() => follow('resume', r.id)}>Ajukan resume dari checkpoint</button>}
              {d.can_resume && r.status === 'partial' && r.direction === 'up' && m.down_sql &&
                <button className="btn danger" disabled={act.busy} onClick={() => follow('rollback', r.id)}>Ajukan pembatalan batch committed</button>}
            </div>
          </div>

          <h3>Attempt eksekusi</h3>
          {d.executions.length === 0 ? <p className="empty">Belum ada attempt.</p> : <Attempts list={d.executions} />}
        </>
      )
    }}</Page>
  )
}

export function Attempts({ list, showMigration }) {
  return (
    <Table>
      <thead><tr>
        <th>Exec</th>{showMigration && <th>Migration</th>}<th>Aksi</th><th>Status</th><th>Attempt</th><th>Batch / row</th><th>Oleh</th><th>Waktu</th><th>SQL</th>
      </tr></thead>
      <tbody>{list.map(e => (
        <tr key={e.id}>
          <td>E{e.id}{e.reverts_execution_id && <div className="small muted">batalkan E{e.reverts_execution_id}</div>}</td>
          {showMigration && <td>{e.migration_version ? `v${e.migration_version} ` : ''}{e.migration_name}</td>}
          <td>{ACTION_LABEL[e.action]} <span className="muted small">({e.direction})</span></td>
          <td><Status value={e.status} />{e.error && <div className="small bad-text">{e.error}</div>}</td>
          <td>{e.attempt}</td>
          <td>{e.batches} / {e.rows.toLocaleString('id-ID')}</td>
          <td>{e.executed_by}</td>
          <td className="small">{time(e.started_at)}<br />{e.finished_at ? time(e.finished_at) : 'berjalan'}</td>
          <td><details><summary>lihat</summary><pre className="stmt">{e.sql}</pre>{e.trace_id && <div className="small muted">trace {e.trace_id}</div>}</details></td>
        </tr>
      ))}</tbody>
    </Table>
  )
}
