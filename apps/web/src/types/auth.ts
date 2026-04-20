export type UserRole = 'admin' | 'editor' | 'viewer'

export interface AuthUser {
  id: string
  username: string
  email: string
  name: string
  role: UserRole
  createdAt: string
  updatedAt: string
  lastLoginAt?: string
}

export interface AuthConfig {
  enabled: boolean
}

export interface LoginResponse {
  accessToken: string
  user: AuthUser
}

export interface RefreshResponse {
  accessToken: string
}

export interface NotificationChannel {
  name: string
  enabled: boolean
}

export interface NotificationsConfig {
  enabled: boolean
  minSeverity: 'critical' | 'warning' | 'info'
  cooldown: string
  channels: NotificationChannel[]
}
