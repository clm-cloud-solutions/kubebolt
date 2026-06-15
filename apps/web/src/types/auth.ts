export type UserRole = 'admin' | 'editor' | 'viewer'

/** The organization (tenant) a user belongs to. OSS: always the single default org. */
export interface OrgBrief {
  id: string
  name: string
}

/** The team a user belongs to, with their effective role in it. OSS: the single default team. */
export interface TeamBrief {
  id: string
  name: string
  /** Effective role = max(org role, team role). OSS teams never elevate, so this equals the org role. */
  role: UserRole
}

export interface AuthUser {
  id: string
  username: string
  email: string
  name: string
  role: UserRole
  createdAt: string
  updatedAt: string
  lastLoginAt?: string
  /** Present once the backend has the org→team→user hierarchy wired (W1). Absent on auth-disabled installs. */
  org?: OrgBrief
  team?: TeamBrief
}

/** A team in the org → team → user hierarchy. */
export interface Team {
  id: string
  name: string
  orgId: string
  memberCount: number
}

/** A team member joined with their user record + effective role. */
export interface TeamMember {
  userId: string
  username: string
  name: string
  /** Effective role in the team — max(org role, team role). */
  role: UserRole
  /** Raw team-level elevation; "" (omitted) in OSS — inherits the org role. */
  teamRole?: UserRole
}

export interface AuthConfig {
  enabled: boolean
  /** True only on the multi-org edition (EE/Cloud) — gates the self-service
   *  "Create an organization" link. Absent/false on OSS, where signup 409s. */
  signupEnabled?: boolean
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
