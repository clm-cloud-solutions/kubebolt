import { useAuth } from '@/contexts/AuthContext'

// Rutas de administración, en el orden en que se ofrecen. Es la MISMA lista que
// pinta la sección Administration del Sidebar; vive aquí y no allí porque el
// Sidebar importa el Topbar, así que un hook exportado desde el Sidebar y
// consumido por el Topbar cerraría un ciclo de imports.
//
// OSS: sólo hay administradores de organización (no hay equipos), así que la
// marca `teamAdminOk` de la edición Enterprise no aplica y los hubs EE-only
// (`eeAdminNavItems`) no existen.
export const ADMIN_ROUTES: { path: string }[] = [
  { path: '/admin/access' },
  { path: '/admin/agents' },
  { path: '/admin/ai' },
  { path: '/admin/insights' },
  { path: '/admin/system' },
  { path: '/admin/api-tokens' },
]

// useAdminLanding — el primer hub de administración que ESTE usuario puede
// abrir, o null si ninguno.
//
// Existe para dar un único destino donde la sección completa no está visible: el
// pie del Sidebar dentro de un cluster y el menú del avatar. El criterio se
// deriva una sola vez a propósito — dos filtros distintos acabarían ofreciendo
// una entrada que lleva a un 403, y el gateo del nav es cosmético: el backend
// exige el rol en cada endpoint pase lo que pase.
export function useAdminLanding(): string | null {
  const { hasRole } = useAuth()
  return hasRole('admin') ? (ADMIN_ROUTES[0]?.path ?? null) : null
}
