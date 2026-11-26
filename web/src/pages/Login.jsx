import { useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { api } from '../api.js'
import { Alert } from '../ui.jsx'

export default function Login({ onLogin }) {
  const [form, setForm] = useState({ username: '', password: '' })
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const navigate = useNavigate()
  const location = useLocation()
  const from = location.state?.from
  const expired = location.state?.expired

  async function submit(e) {
    e.preventDefault()
    setBusy(true); setError('')
    try {
      const s = await api('/login', { method: 'POST', body: form })
      onLogin(s)
      navigate(from && from.startsWith('/') && !from.startsWith('//') ? from : '/', { replace: true })
    } catch (err) {
      setError(err.message)
      setForm(f => ({ ...f, password: '' }))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="center">
      <form className="card login" onSubmit={submit}>
        <h1>DBMP</h1>
        <p className="muted">Database Management Platform</p>
        {expired && !error && <Alert error="Sesi berakhir. Silakan login kembali." />}
        <Alert error={error} />
        <label>Username
          <input autoComplete="username" value={form.username} required autoFocus
            onChange={e => setForm({ ...form, username: e.target.value })} />
        </label>
        <label>Password
          <input type="password" autoComplete="current-password" value={form.password} required
            onChange={e => setForm({ ...form, password: e.target.value })} />
        </label>
        <button className="btn primary block" disabled={busy}>{busy ? 'Masuk…' : 'Login'}</button>
      </form>
    </div>
  )
}
