import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import Login from './pages/Login'
import Dashboard from './pages/Dashboard'
import ClusterList from './pages/ClusterList'
import ClusterDetail from './pages/ClusterDetail'
import NodeDetail from './pages/NodeDetail'
import ConfigEditor from './pages/ConfigEditor'
import ProtectedRoute from './components/ProtectedRoute'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      staleTime: 30_000,
    },
  },
})

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route
            path="/"
            element={
              <ProtectedRoute>
                <Dashboard />
              </ProtectedRoute>
            }
          />
          <Route
            path="/clusters"
            element={
              <ProtectedRoute>
                <ClusterList />
              </ProtectedRoute>
            }
          />
          <Route
            path="/clusters/:id"
            element={
              <ProtectedRoute>
                <ClusterDetail />
              </ProtectedRoute>
            }
          />
          <Route
            path="/clusters/:id/nodes/:nid"
            element={
              <ProtectedRoute>
                <NodeDetail />
              </ProtectedRoute>
            }
          />
          <Route
            path="/clusters/:id/config"
            element={
              <ProtectedRoute>
                <ConfigEditor />
              </ProtectedRoute>
            }
          />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </BrowserRouter>
    </QueryClientProvider>
  )
}
