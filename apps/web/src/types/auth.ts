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
  /** Present only for email: "instant" | "hourly" | "daily". */
  digestMode?: string
}

export interface NotificationsConfig {
  enabled: boolean         // masterEnabled AND at-least-one-channel
  masterEnabled: boolean   // global kill switch state
  minSeverity: 'critical' | 'warning' | 'info'
  cooldown: string
  baseUrl: string
  includeResolved: boolean
  channels: NotificationChannel[]
}
