import { Link } from 'react-router-dom'

export const LEVEL = { view: 'View Only', edit: 'View + Edit', none: 'Tidak ada akses' }

export const STATUS_LABEL = {
  draft: 'Draft', applied: 'Applied', rolled_back: 'Rolled back', partial: 'Partial',
  pending_review: 'Pending review', approved: 'Approved', rejected: 'Rejected', cancelled: 'Cancelled',
  queued: 'Pending (queue)', running: 'Running', success: 'Success', failed: 'Failed',
  partially_reverted: 'Partially reverted', uncertain: 'Uncertain', superseded: 'Superseded', done: 'Done',
}

const TONE = {
  applied: 'ok', success: 'ok', done: 'ok', approved: 'info',
  failed: 'bad', rejected: 'bad',
  partial: 'warn', partially_reverted: 'warn', uncertain: 'warn',
  running: 'run', queued: 'run', pending_review: 'info',
}

export function Status({ value }) {
  return <span className={`badge ${TONE[value] || 'muted'}`}>{STATUS_LABEL[value] || value}</span>
}

export const ACTION_LABEL = { apply: 'Apply', rollback: 'Rollback', forward: 'Forward', resume: 'Resume' }

export function Loading({ text = 'Memuat…' }) {
  return <div className="loading" role="status"><span className="spinner" />{text}</div>
}

export function Alert({ error, message }) {
  if (error) return <div className="alert bad" role="alert">{error}</div>
  if (message) return <div className="alert ok" role="status">{message}</div>
  return null
}

// Page renders the loading / error state of a useApi result, then children.
export function Page({ q, children }) {
  if (q.loading && !q.data) return <Loading />
  if (q.error && !q.data) return <Alert error={q.error.message} />
  return children(q.data)
}

export function Empty({ children }) {
  return <p className="empty">{children}</p>
}

export function Table({ children }) {
  return <div className="table-wrap"><table>{children}</table></div>
}

export function time(v) {
  if (!v) return '–'
  return new Date(v).toLocaleString('id-ID', { dateStyle: 'medium', timeStyle: 'medium' })
}

export function Crumbs({ items }) {
  return (
    <nav className="crumbs" aria-label="breadcrumb">
      {items.map(([label, to], i) => (
        <span key={i}>{i > 0 && ' / '}{to ? <Link to={to}>{label}</Link> : label}</span>
      ))}
    </nav>
  )
}
