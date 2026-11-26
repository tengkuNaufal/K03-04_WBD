import { createContext, useCallback, useContext, useEffect, useState } from 'react'
import { Navigate, NavLink, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import { api, setCsrf, setUnauthorizedHandler, useApi } from './api.js'
import { Loading } from './ui.jsx'
import Login from './pages/Login.jsx'
import Dashboard from './pages/Dashboard.jsx'
import Database from './pages/Database.jsx'
import Users from './pages/admin/Users.jsx'
import User from './pages/admin/User.jsx'
import Groups from './pages/admin/Groups.jsx'
import Group from './pages/admin/Group.jsx'
import Databases from './pages/admin/Databases.jsx'
import DatabaseForm from './pages/admin/DatabaseForm.jsx'

const SessionContext = createContext(null)
export const useSession = () => useContext(SessionContext)

export default function App() {
  const [session, setSession] = useState(undefined) // undefined: loading, null: logged out
  const navigate = useNavigate()
  const location = useLocation()

  const signIn = useCallback(s => { setCsrf(s.csrf); setSession(s) }, [])
  const signOut = useCallback(() => { setCsrf(''); setSession(null) }, [])

  useEffect(() => {
    setUnauthorizedHandler(() => {
      signOut()
      navigate('/login', { state: { from: location.pathname, expired: true } })
    })
  }, [navigate, location.pathname, signOut])

  useEffect(() => {
    api('/session').then(signIn).catch(() => setSession(null))
  }, [signIn])

  if (session === undefined) return <div className="center"><Loading /></div>

  if (!session) {
    return (
      <Routes>
        <Route path="/login" element={<Login onLogin={signIn} />} />
        <Route path="*" element={<Navigate to="/login" replace state={{ from: location.pathname }} />} />
      </Routes>
    )
  }

  const admin = session.user.is_admin
  return (
    <SessionContext.Provider value={{ ...session, signOut }}>
      <Shell admin={admin}>
        <Routes>
          <Route path="/" element={<Dashboard />} />
          <Route path="/login" element={<Navigate to="/" replace />} />
          <Route path="/db/:dbID/*" element={<Database />} />
          {admin && <>
            <Route path="/admin/users" element={<Users />} />
            <Route path="/admin/users/:id" element={<User />} />
            <Route path="/admin/groups" element={<Groups />} />
            <Route path="/admin/groups/:id" element={<Group />} />
            <Route path="/admin/databases" element={<Databases />} />
            <Route path="/admin/databases/new" element={<DatabaseForm />} />
            <Route path="/admin/databases/:id" element={<DatabaseForm />} />
          </>}
          <Route path="*" element={<div className="alert bad">Halaman tidak ditemukan atau Anda tidak memiliki akses.</div>} />
        </Routes>
      </Shell>
    </SessionContext.Provider>
  )
}

function Shell({ admin, children }) {
  const { user, signOut } = useSession()
  const [open, setOpen] = useState(false)
  const location = useLocation()
  const reviews = useApi('/reviews', { poll: 15000 })
  const pending = reviews.data?.length || 0

  useEffect(() => setOpen(false), [location.pathname])

  async function logout() {
    try { await api('/logout', { method: 'POST' }) } finally { signOut() }
  }

  return (
    <>
      <header className="topbar">
        <div className="topbar-inner">
          <NavLink to="/" className="brand">DBMP</NavLink>
          <button className="menu-toggle" aria-expanded={open} aria-label="Menu" onClick={() => setOpen(o => !o)}>
            <span /><span /><span />
          </button>
          <nav className={`nav ${open ? 'open' : ''}`}>
            <NavLink to="/" end>Dashboard{pending > 0 && <span className="count">{pending}</span>}</NavLink>
            {admin && <>
              <NavLink to="/admin/users">Users</NavLink>
              <NavLink to="/admin/groups">Groups</NavLink>
              <NavLink to="/admin/databases">Koneksi</NavLink>
            </>}
            <span className="nav-user">{user.username}{admin && ' · admin'}</span>
            <button className="btn ghost small" onClick={logout}>Logout</button>
          </nav>
        </div>
      </header>
      <main className="container">{children}</main>
    </>
  )
}
