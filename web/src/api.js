import { useCallback, useEffect, useRef, useState } from 'react'

let csrf = ''
let onUnauthorized = () => {}

export function setCsrf(token) { csrf = token }
export function setUnauthorizedHandler(fn) { onUnauthorized = fn }

export class ApiError extends Error {
  constructor(status, message) { super(message); this.status = status }
}

// api calls the Go backend. Every state-changing request sends JSON and the
// session's CSRF token; the session itself is an HttpOnly cookie.
export async function api(path, { method = 'GET', body } = {}) {
  const opts = { method, credentials: 'same-origin', headers: {} }
  if (method !== 'GET') {
    opts.headers['Content-Type'] = 'application/json'
    opts.headers['X-CSRF-Token'] = csrf
    opts.body = JSON.stringify(body ?? {})
  }
  const res = await fetch('/api' + path, opts)
  let data = null
  try { data = await res.json() } catch { /* empty body */ }
  if (res.status === 401 && path !== '/login' && path !== '/session') onUnauthorized()
  if (!res.ok) throw new ApiError(res.status, data?.error || `Permintaan gagal (HTTP ${res.status}).`)
  return data
}

// useApi loads a GET endpoint. With `poll` (ms) it refreshes while
// `shouldPoll(data)` is true, e.g. while a job is running.
export function useApi(path, { poll = 0, shouldPoll = () => true } = {}) {
  const [state, setState] = useState({ data: null, error: null, loading: true })
  const keep = useRef(shouldPoll)
  keep.current = shouldPoll

  const load = useCallback(async (quiet = false) => {
    if (!path) return
    if (!quiet) setState(s => ({ ...s, loading: true }))
    try {
      const data = await api(path)
      setState({ data, error: null, loading: false })
      return data
    } catch (error) {
      setState(s => ({ ...s, error, loading: false }))
    }
  }, [path])

  useEffect(() => { load() }, [load])

  useEffect(() => {
    if (!poll || !state.data || !keep.current(state.data)) return
    const t = setTimeout(() => load(true), poll)
    return () => clearTimeout(t)
  }, [poll, state.data, load])

  return { ...state, reload: load }
}

// useAction wraps a mutation with busy/error/message state.
export function useAction() {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const run = useCallback(async (fn, okMessage) => {
    setBusy(true); setError(''); setMessage('')
    try {
      const r = await fn()
      if (okMessage) setMessage(typeof okMessage === 'function' ? okMessage(r) : okMessage)
      return r
    } catch (e) {
      setError(e.message)
      return undefined
    } finally {
      setBusy(false)
    }
  }, [])
  return { busy, error, message, run, setError, setMessage }
}
