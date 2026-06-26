package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/crypto/bcrypt"
)

var (
	usersBucket        = []byte("users")
	usernameIdxBucket  = []byte("username_index")
	refreshTokenBucket = []byte("refresh_tokens")
	settingsBucket     = []byte("settings")
	// Buckets used by other packages (cluster management, etc.) —
	// initialized here so there's a single place where DB schema is defined.
	clustersBucket         = []byte("clusters")         // uploaded kubeconfigs
	clusterDisplayBucket   = []byte("cluster_display")  // display name overrides
	clusterUIDBucket       = []byte("cluster_uid")      // kube-system UID per kubeconfig context (resolved at connect time)
	copilotSessionsBucket  = []byte("copilot_sessions") // copilot usage analytics
	copilotConvBucket      = []byte("copilot_conversations") // persistent Kobi conversation transcripts (per user)
	agentsBucket           = []byte("agents")           // persistent agent registry records
	insightsBucket         = []byte("insights")         // persistent insight records (Sprint 0)
	kobiActionsBucket      = []byte("kobi_actions")     // durable mutation audit trail (Sprint 1)
	orgSettingsBucket      = []byte("org_settings")     // per-org UI settings blobs (keyed org\x00key)
)

// orgSettingKey composes the BoltDB key for a per-org setting: the org id, a
// NUL separator, then the domain key. The NUL byte can't appear in a UUID or a
// domain key, so it's an unambiguous separator. OSS resolves org to
// DefaultTenantName, so its keys are stable single-tenant values.
func orgSettingKey(org, key string) []byte {
	if org == "" {
		org = DefaultTenantName
	}
	return []byte(org + "\x00" + key)
}

// Role represents a KubeBolt application-level role.
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

// RoleLevel returns the numeric level for role comparison (higher = more permissions).
func RoleLevel(r Role) int {
	switch r {
	case RoleAdmin:
		return 3
	case RoleEditor:
		return 2
	case RoleViewer:
		return 1
	default:
		return 0
	}
}

// ValidRole checks if a role string is valid.
func ValidRole(r Role) bool {
	return r == RoleAdmin || r == RoleEditor || r == RoleViewer
}

// User represents a KubeBolt user stored in the database.
type User struct {
	ID string `json:"id"`
	// OrgID is the tenant (organization) the user belongs to. Empty in OSS
	// (single-tenant: callers resolve it to DefaultTenantName); the EE/SaaS
	// Postgres store populates it from the users.org_id column so login can
	// stamp it into the JWT tenant claim. omitempty keeps OSS BoltDB records
	// and API payloads byte-identical to pre-seam.
	OrgID        string     `json:"orgId,omitempty"`
	Username     string     `json:"username"`
	Email        string     `json:"email"`
	Name         string     `json:"name"`
	PasswordHash string     `json:"passwordHash"`
	Role         Role       `json:"role"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	LastLoginAt  *time.Time `json:"lastLoginAt,omitempty"`
}

// UserResponse is the API-safe representation of a User (no password hash).
type UserResponse struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	Role        Role       `json:"role"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	LastLoginAt *time.Time `json:"lastLoginAt,omitempty"`
}

// ToResponse converts a User to its API-safe representation.
func (u *User) ToResponse() UserResponse {
	return UserResponse{
		ID:          u.ID,
		Username:    u.Username,
		Email:       u.Email,
		Name:        u.Name,
		Role:        u.Role,
		CreatedAt:   u.CreatedAt,
		UpdatedAt:   u.UpdatedAt,
		LastLoginAt: u.LastLoginAt,
	}
}

// RefreshToken represents a stored refresh token.
type RefreshToken struct {
	TokenHash string    `json:"tokenHash"`
	UserID    string    `json:"userId"`
	ExpiresAt time.Time `json:"expiresAt"`
	CreatedAt time.Time `json:"createdAt"`
	// OrgID is the owning user's organization, resolved at lookup time so the
	// pre-auth refresh-token rotation (where the request carries no org yet)
	// can scope the follow-up GetUser/rotation writes to the right tenant under
	// RLS. Derived on read (not a stored column); always "" in single-tenant OSS.
	OrgID string `json:"orgId,omitempty"`
}

// Store manages user and token persistence with BoltDB.
type Store struct {
	db *bolt.DB
}

// NewStore opens (or creates) the BoltDB database and initializes buckets.
func NewStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	dbPath := filepath.Join(dataDir, "kubebolt.db")
	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Create buckets (auth + cross-package state like cluster management)
	err = db.Update(func(tx *bolt.Tx) error {
		for _, bucket := range [][]byte{usersBucket, usernameIdxBucket, refreshTokenBucket, settingsBucket, clustersBucket, clusterDisplayBucket, clusterUIDBucket, copilotSessionsBucket, copilotConvBucket, agentsBucket, insightsBucket, kobiActionsBucket, orgSettingsBucket} {
			if _, err := tx.CreateBucketIfNotExists(bucket); err != nil {
				return fmt.Errorf("create bucket %s: %w", bucket, err)
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying BoltDB handle for use by other packages that
// share the same data file (e.g. cluster management). All buckets used
// across the codebase are created at Store initialization.
func (s *Store) DB() *bolt.DB {
	return s.db
}

// CopilotConversationsBucket returns the bucket name for persistent Kobi
// conversation transcripts (per-user history + resume).
func CopilotConversationsBucket() []byte {
	return copilotConvBucket
}

// CopilotSessionsBucket returns the bucket name for copilot usage records.
func CopilotSessionsBucket() []byte {
	return copilotSessionsBucket
}

// ClusterBuckets returns the bucket names used for cluster management state.
//
// uidBucket is the kube-system namespace UID per context resolved at
// connect time — persisted so the cluster selector can populate
// ClusterID for direct-kubeconfig contexts the operator has visited
// before, without requiring a re-connect to re-discover the UID.
func ClusterBuckets() (configs, displayNames, uids []byte) {
	return clustersBucket, clusterDisplayBucket, clusterUIDBucket
}

// AgentsBucket returns the bucket name used by the persistent agent
// registry store (apps/api/internal/agent/channel/registry_store.go).
// Survives backend restarts so the cluster selector keeps showing
// agent-proxy clusters before live reconnects come back in.
func AgentsBucket() []byte {
	return agentsBucket
}

// InsightsBucket returns the bucket name used by the persistent insights
// store (apps/api/internal/insights/store.go). Holds insight identities
// (active + recently-resolved) keyed by tenant/cluster/fingerprint so
// history survives restarts and feeds notifications, Kobi, and Autopilot.
func InsightsBucket() []byte {
	return insightsBucket
}

// KobiActionsBucket returns the bucket name used by the durable mutation
// audit store (apps/api/internal/audit/store.go). Records every cluster
// mutation — UI-initiated and Kobi-proposed — so the admin action-history
// view survives restarts. (Sprint 1.)
func KobiActionsBucket() []byte {
	return kobiActionsBucket
}

// UserStore is the W1 seam for the User domain
// (internal/saas/kubebolt-e1-multitenant-scoping.md §8). OSS uses the BoltDB
// *Store (every user is an org member with a set Role); EE swaps a Postgres
// impl where User.Role may be "" — a "team-only" user with no org-wide access,
// the segmentation primitive. The interface covers user management; the
// refresh-token methods on *Store are the TokenStore concern (W1 #5).
type UserStore interface {
	CreateUser(ctx context.Context, username, email, name, password string, role Role) (*User, error)
	GetUser(ctx context.Context, id string) (*User, error)
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	// GetUserByEmail resolves a user by their global-unique email — the login
	// identity for multi-org (Track D). Returns "user not found" when absent.
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	ListUsers(ctx context.Context) ([]User, error)
	UpdateUser(ctx context.Context, id, username, email, name string, role Role) (*User, error)
	UpdatePassword(ctx context.Context, id, newPassword string) error
	UpdateLastLogin(ctx context.Context, id string) error
	DeleteUser(ctx context.Context, id string) error
	// CountByRole counts users with a given role — used by the "can't delete /
	// demote the last admin" guard in the admin user handlers.
	CountByRole(ctx context.Context, role Role) (int, error)
}

// Compile-time guarantee the Bolt impl satisfies the seam.
var _ UserStore = (*Store)(nil)

// CreateUser creates a new user with a bcrypt-hashed password.
func (s *Store) CreateUser(_ context.Context, username, email, name, password string, role Role) (*User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	now := time.Now().UTC()
	user := &User{
		ID:           uuid.New().String(),
		Username:     username,
		Email:        email,
		Name:         name,
		PasswordHash: string(hash),
		Role:         role,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	err = s.db.Update(func(tx *bolt.Tx) error {
		// Check username uniqueness
		idx := tx.Bucket(usernameIdxBucket)
		if idx.Get([]byte(username)) != nil {
			return fmt.Errorf("username %q already exists", username)
		}

		data, err := json.Marshal(user)
		if err != nil {
			return err
		}

		if err := tx.Bucket(usersBucket).Put([]byte(user.ID), data); err != nil {
			return err
		}
		return idx.Put([]byte(username), []byte(user.ID))
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

// GetUser retrieves a user by ID.
func (s *Store) GetUser(_ context.Context, id string) (*User, error) {
	var user User
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(usersBucket).Get([]byte(id))
		if data == nil {
			return fmt.Errorf("user not found")
		}
		return json.Unmarshal(data, &user)
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// GetUserByUsername retrieves a user by username via the index.
func (s *Store) GetUserByUsername(_ context.Context, username string) (*User, error) {
	var user User
	err := s.db.View(func(tx *bolt.Tx) error {
		id := tx.Bucket(usernameIdxBucket).Get([]byte(username))
		if id == nil {
			return fmt.Errorf("user not found")
		}
		data := tx.Bucket(usersBucket).Get(id)
		if data == nil {
			return fmt.Errorf("user not found")
		}
		return json.Unmarshal(data, &user)
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// GetUserByEmail resolves a user by email. Bolt has no email index (single-org
// OSS keys by username), so this scans the users bucket — fine at OSS scale.
func (s *Store) GetUserByEmail(_ context.Context, email string) (*User, error) {
	if email == "" {
		return nil, fmt.Errorf("user not found")
	}
	var found *User
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(usersBucket).ForEach(func(_, data []byte) error {
			var u User
			if err := json.Unmarshal(data, &u); err != nil {
				return nil // skip corrupt rows
			}
			if u.Email == email {
				uu := u
				found = &uu
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("user not found")
	}
	return found, nil
}

// ListUsers returns all users in the store.
func (s *Store) ListUsers(_ context.Context) ([]User, error) {
	var users []User
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(usersBucket).ForEach(func(k, v []byte) error {
			var u User
			if err := json.Unmarshal(v, &u); err != nil {
				return err
			}
			users = append(users, u)
			return nil
		})
	})
	return users, err
}

// UpdateUser updates a user's mutable fields (username, email, name, role).
func (s *Store) UpdateUser(_ context.Context, id, username, email, name string, role Role) (*User, error) {
	var updated User
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(usersBucket)
		data := bucket.Get([]byte(id))
		if data == nil {
			return fmt.Errorf("user not found")
		}

		var user User
		if err := json.Unmarshal(data, &user); err != nil {
			return err
		}

		// If username changed, update index
		if username != "" && username != user.Username {
			idx := tx.Bucket(usernameIdxBucket)
			if existing := idx.Get([]byte(username)); existing != nil {
				return fmt.Errorf("username %q already exists", username)
			}
			idx.Delete([]byte(user.Username))
			idx.Put([]byte(username), []byte(id))
			user.Username = username
		}

		if email != "" {
			user.Email = email
		}
		if name != "" {
			user.Name = name
		}
		if role != "" && ValidRole(role) {
			user.Role = role
		}
		user.UpdatedAt = time.Now().UTC()

		newData, err := json.Marshal(&user)
		if err != nil {
			return err
		}
		updated = user
		return bucket.Put([]byte(id), newData)
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// UpdatePassword changes a user's password.
func (s *Store) UpdatePassword(_ context.Context, id, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(usersBucket)
		data := bucket.Get([]byte(id))
		if data == nil {
			return fmt.Errorf("user not found")
		}

		var user User
		if err := json.Unmarshal(data, &user); err != nil {
			return err
		}

		user.PasswordHash = string(hash)
		user.UpdatedAt = time.Now().UTC()

		newData, err := json.Marshal(&user)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(id), newData)
	})
}

// UpdateLastLogin sets the last login timestamp for a user.
func (s *Store) UpdateLastLogin(_ context.Context, id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(usersBucket)
		data := bucket.Get([]byte(id))
		if data == nil {
			return fmt.Errorf("user not found")
		}

		var user User
		if err := json.Unmarshal(data, &user); err != nil {
			return err
		}

		now := time.Now().UTC()
		user.LastLoginAt = &now
		user.UpdatedAt = now

		newData, err := json.Marshal(&user)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(id), newData)
	})
}

// DeleteUser removes a user and their username index entry.
func (s *Store) DeleteUser(_ context.Context, id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(usersBucket)
		data := bucket.Get([]byte(id))
		if data == nil {
			return fmt.Errorf("user not found")
		}

		var user User
		if err := json.Unmarshal(data, &user); err != nil {
			return err
		}

		if err := tx.Bucket(usernameIdxBucket).Delete([]byte(user.Username)); err != nil {
			return err
		}
		// Also delete refresh tokens for this user
		rtBucket := tx.Bucket(refreshTokenBucket)
		var tokensToDelete [][]byte
		rtBucket.ForEach(func(k, v []byte) error {
			var rt RefreshToken
			if err := json.Unmarshal(v, &rt); err == nil && rt.UserID == id {
				tokensToDelete = append(tokensToDelete, k)
			}
			return nil
		})
		for _, k := range tokensToDelete {
			rtBucket.Delete(k)
		}

		return bucket.Delete([]byte(id))
	})
}

// UserCount returns the total number of users.
func (s *Store) UserCount() (int, error) {
	var count int
	err := s.db.View(func(tx *bolt.Tx) error {
		count = tx.Bucket(usersBucket).Stats().KeyN
		return nil
	})
	return count, err
}

// CountByRole returns the number of users with the given role.
func (s *Store) CountByRole(_ context.Context, role Role) (int, error) {
	var count int
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(usersBucket).ForEach(func(k, v []byte) error {
			var u User
			if err := json.Unmarshal(v, &u); err != nil {
				return nil // skip corrupt entries
			}
			if u.Role == role {
				count++
			}
			return nil
		})
	})
	return count, err
}

// SeedAdmin creates the default admin user if none exist. Kept for existing
// callers; delegates to the package-level SeedAdmin so any UserStore impl
// (BoltDB here, Postgres in EE) seeds identically.
func (s *Store) SeedAdmin(ctx context.Context, password string) (bool, error) {
	return SeedAdmin(ctx, s, password)
}

// SeedAdmin creates the default admin user on the given UserStore if it has no
// users yet. Returns true when it seeded one. Backend-agnostic (the EE
// Postgres UserStore reuses it via the newAuthStore seam).
func SeedAdmin(ctx context.Context, s UserStore, password string) (bool, error) {
	users, err := s.ListUsers(ctx)
	if err != nil {
		return false, err
	}
	if len(users) > 0 {
		return false, nil
	}
	if _, err := s.CreateUser(ctx, "admin", "admin@localhost", "Admin", password, RoleAdmin); err != nil {
		return false, fmt.Errorf("seed admin user: %w", err)
	}
	return true, nil
}

// RefreshTokenStore is the W1 seam for refresh-token persistence — the
// rotating, hashed session tokens behind /auth/refresh (distinct from the
// long-lived ingest "kb_" and REST "kbs_/kbk_" tokens, which have their own
// stores). Split out from UserStore on purpose: a user's identity and their
// active sessions are separate concerns, and EE may back sessions with a
// different store (e.g. Redis/Postgres with TTL eviction) than user records.
// OSS uses the BoltDB *Store for both.
type RefreshTokenStore interface {
	SaveRefreshToken(ctx context.Context, rt *RefreshToken) error
	GetRefreshToken(ctx context.Context, tokenHash string) (*RefreshToken, error)
	DeleteRefreshToken(ctx context.Context, tokenHash string) error
	DeleteUserRefreshTokens(ctx context.Context, userID string) error
}

// Compile-time guarantee the Bolt impl satisfies the seam.
var _ RefreshTokenStore = (*Store)(nil)

// AuthStore is the persistence surface the auth Handlers need: user records
// (UserStore) + refresh tokens (RefreshTokenStore). *Store satisfies it
// (BoltDB); the EE build supplies a Postgres implementation behind the
// newAuthStore factory seam, so Handlers stay backend-agnostic.
type AuthStore interface {
	UserStore
	RefreshTokenStore
}

var _ AuthStore = (*Store)(nil)

// SaveRefreshToken stores a refresh token.
func (s *Store) SaveRefreshToken(_ context.Context, rt *RefreshToken) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		data, err := json.Marshal(rt)
		if err != nil {
			return err
		}
		return tx.Bucket(refreshTokenBucket).Put([]byte(rt.TokenHash), data)
	})
}

// GetRefreshToken retrieves a refresh token by its hash.
func (s *Store) GetRefreshToken(_ context.Context, tokenHash string) (*RefreshToken, error) {
	var rt RefreshToken
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(refreshTokenBucket).Get([]byte(tokenHash))
		if data == nil {
			return fmt.Errorf("refresh token not found")
		}
		return json.Unmarshal(data, &rt)
	})
	if err != nil {
		return nil, err
	}
	return &rt, nil
}

// DeleteRefreshToken removes a refresh token by its hash.
func (s *Store) DeleteRefreshToken(_ context.Context, tokenHash string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(refreshTokenBucket).Delete([]byte(tokenHash))
	})
}

// DeleteUserRefreshTokens removes all refresh tokens for a user.
func (s *Store) DeleteUserRefreshTokens(_ context.Context, userID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(refreshTokenBucket)
		var toDelete [][]byte
		bucket.ForEach(func(k, v []byte) error {
			var rt RefreshToken
			if err := json.Unmarshal(v, &rt); err == nil && rt.UserID == userID {
				toDelete = append(toDelete, k)
			}
			return nil
		})
		for _, k := range toDelete {
			bucket.Delete(k)
		}
		return nil
	})
}

// SettingStore is the seam for the global key→value install settings
// (jwt_secret + the UI-editable auth/copilot/notifications/general/ingest
// config blobs). Global, NOT tenant-scoped. OSS uses the BoltDB *Store; EE
// swaps a Postgres impl so a multi-replica Cloud deployment shares one settings
// table (otherwise each replica would sign JWTs with a different secret).
type SettingStore interface {
	GetSetting(key string) ([]byte, error)
	SetSetting(key string, value []byte) error
}

// Compile-time guarantee the Bolt impl satisfies the seam.
var _ SettingStore = (*Store)(nil)

// OrgSettingStore is the seam for PER-ORG UI settings blobs (Copilot today;
// general/notifications later). Unlike SettingStore (global install settings:
// jwt_secret etc.), these are tenant-scoped: each org configures its own. The
// org is taken from ctx (auth.TenantIDFromContext), like every other ctx-scoped
// EE store. OSS uses the BoltDB *Store (single default tenant → one row per
// key, identical to the old global behavior); EE swaps a Postgres impl backed
// by an RLS table so a second org's settings are invisible at the engine level.
type OrgSettingStore interface {
	GetOrgSetting(ctx context.Context, key string) ([]byte, error)
	SetOrgSetting(ctx context.Context, key string, value []byte) error
}

// Compile-time guarantee the Bolt impl satisfies the per-org seam.
var _ OrgSettingStore = (*Store)(nil)

// GetOrgSetting retrieves a per-org setting value, scoped to the org resolved
// from ctx. Mirrors GetSetting's not-found error so the settings Runtime treats
// a miss as "no override → env baseline".
func (s *Store) GetOrgSetting(ctx context.Context, key string) ([]byte, error) {
	org := TenantIDFromContext(ctx)
	var val []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(orgSettingsBucket).Get(orgSettingKey(org, key))
		if v == nil {
			return fmt.Errorf("setting %q not found", key)
		}
		val = make([]byte, len(v))
		copy(val, v)
		return nil
	})
	return val, err
}

// SetOrgSetting stores a per-org setting value, scoped to the org resolved from
// ctx.
func (s *Store) SetOrgSetting(ctx context.Context, key string, value []byte) error {
	org := TenantIDFromContext(ctx)
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(orgSettingsBucket).Put(orgSettingKey(org, key), value)
	})
}

// AllSettings returns every key→value pair in the settings bucket. Used by the
// Bolt→Postgres migration to copy settings without enumerating known keys.
func (s *Store) AllSettings() (map[string][]byte, error) {
	out := map[string][]byte{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(settingsBucket).ForEach(func(k, v []byte) error {
			cp := make([]byte, len(v))
			copy(cp, v)
			out[string(k)] = cp
			return nil
		})
	})
	return out, err
}

// GetSetting retrieves a setting value by key from the settings bucket.
func (s *Store) GetSetting(key string) ([]byte, error) {
	var val []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(settingsBucket).Get([]byte(key))
		if v == nil {
			return fmt.Errorf("setting %q not found", key)
		}
		val = make([]byte, len(v))
		copy(val, v)
		return nil
	})
	return val, err
}

// SetSetting stores a setting value by key in the settings bucket.
func (s *Store) SetSetting(key string, value []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(settingsBucket).Put([]byte(key), value)
	})
}

// CheckPassword verifies a plaintext password against a user's stored hash.
func CheckPassword(user *User, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) == nil
}
