import { Navigate } from 'react-router-dom'
import { useAuthStore } from '../store/auth'
import type { ReactNode } from 'react'

interface Props {
  children: ReactNode
}

export default function ProtectedRoute({ children }: Props) {
  const accessToken = useAuthStore((s) => s.accessToken)
  if (!accessToken) {
    return <Navigate to="/login" replace />
  }
  return <>{children}</>
}
